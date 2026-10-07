package tessaridb

// A feed narrowed by a condition (§3.7) and the Progress it sends (§3.15):
// the bytes against the corpus and the specification, and the minor-4 gate
// against scripted nodes on loopback.

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestProgressCorpus(t *testing.T) {
	var vectors []struct {
		Name    string `json:"name"`
		BodyHex string `json:"body_hex"`
		Decoded *struct {
			Sequence string  `json:"sequence"`
			Cursor   *string `json:"cursor"`
		} `json:"decoded"`
		Malformed string `json:"malformed"`
	}
	if err := json.Unmarshal(readCorpus(t, "frames-v1.json")["progress"], &vectors); err != nil {
		t.Fatalf("the progress vectors do not parse: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("the corpus carries no progress vectors")
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			body, err := hex.DecodeString(vector.BodyHex)
			if err != nil {
				t.Fatalf("body_hex: %v", err)
			}
			read, err := readProgress(body)
			if vector.Malformed != "" {
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("%s: read %+v, %v; want a protocol error", vector.Malformed, read, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			want := Progress{}
			if want.Sequence, err = strconv.ParseUint(vector.Decoded.Sequence, 10, 64); err != nil {
				t.Fatalf("sequence %q: %v", vector.Decoded.Sequence, err)
			}
			if vector.Decoded.Cursor != nil {
				want.Cursor = *vector.Decoded.Cursor
			}
			if read != want {
				t.Fatalf("read %+v, want %+v", read, want)
			}
		})
	}
}

// The body carries the cursor's place before the condition, as empty text when
// there is no cursor, then the condition, then ONE encoded object (§3.7).
func TestANarrowedSubscribeWritesTheConditionAfterTheCursorsPlace(t *testing.T) {
	parameters := map[string]Value{"least": Integer{Value: 10}}
	encoded, err := Encode(Object{Fields: parameters})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cursor string
	}{
		{"no cursor", ""},
		{"a split table's cursor", "1.1:d=12,7.2=30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := subscribeBody(7, "orders", tc.cursor, &Narrowing{
				Table: "orders", Condition: "total >= $least", Parameters: parameters,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := &writer{}
			want.u64(7)
			want.u8(1)
			want.text("orders")
			want.text(tc.cursor)
			want.text("total >= $least")
			want.lenbytes(encoded)
			if !bytes.Equal(body, want.buf) {
				t.Fatalf("body %x, want %x", body, want.buf)
			}
		})
	}
	// Without a condition the body is the one every earlier node reads.
	plain, err := subscribeBody(7, "orders", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0, 0, 0, 0, 0, 0, 0, 7, 1, 0, 0, 0, 6, 'o', 'r', 'd', 'e', 'r', 's'}; !bytes.Equal(plain, want) {
		t.Fatalf("plain body %x, want %x", plain, want)
	}
}

// feedNode greets with minor, reports the first frame it receives on got, and
// answers a Subscribe with said before ending its side — so a client that sent
// what it should not have reads every change rather than hanging.
func feedNode(t *testing.T, minor byte, said []byte) (string, <-chan []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	got := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		var hello [6]byte
		if _, err := io.ReadFull(r, hello[:]); err != nil {
			return
		}
		_, _ = conn.Write([]byte{'T', 'E', 'S', 'S', 1, minor})
		_, body, err := readFrame(r, map[byte]bool{frameSubscribe: true})
		if err != nil {
			close(got)
			return
		}
		got <- body
		_, _ = conn.Write(said)
	}()
	return listener.Addr().String(), got
}

func frameOf(kind byte, body []byte) []byte {
	var out bytes.Buffer
	_ = writeFrame(&out, kind, body)
	return out.Bytes()
}

func TestAConditionIsNotSentToANodeBelowMinor4(t *testing.T) {
	address, got := feedNode(t, 3, frameOf(frameChange, removedChange("")))
	conn, err := Dial(address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _, err = conn.ChangesWhere(0, true, Narrowing{Table: "orders", Condition: "total > 10"})
	if !errors.Is(err, ErrNodeTooOld) {
		t.Fatalf("subscribed to a minor-3 node: %v", err)
	}
	_ = conn.Close()
	if body, sent := <-got; sent {
		t.Fatalf("a Subscribe reached the minor-3 node: %x", body)
	}
}

func TestANarrowedFeedDeliversItsChangesAndItsProgressApart(t *testing.T) {
	progress := &writer{}
	progress.u64(41)
	said := append(frameOf(frameChange, removedChange("")), frameOf(frameProgress, progress.buf)...)
	address, got := feedNode(t, 4, said)
	conn, err := Dial(address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	arrivals, fail, err := conn.ChangesWhere(40, false, Narrowing{Table: "orders", Condition: "total > 10"})
	if err != nil {
		t.Fatal(err)
	}
	if body := <-got; !bytes.HasPrefix(body, []byte{0, 0, 0, 0, 0, 0, 0, 41}) {
		t.Fatalf("the feed did not resume after 40: %x", body)
	}
	var seen []Arrival
	deadline := time.After(5 * time.Second)
	for len(seen) < 2 {
		select {
		case arrival, open := <-arrivals:
			if !open {
				t.Fatalf("the feed ended after %v", seen)
			}
			seen = append(seen, arrival)
		case err := <-fail:
			t.Fatalf("the feed failed after %v: %v", seen, err)
		case <-deadline:
			t.Fatalf("only %v arrived", seen)
		}
	}
	if change, ok := seen[0].(Change); !ok || !change.Removed || change.Sequence != 7 {
		t.Fatalf("first arrival %+v, want the removal at 7", seen[0])
	}
	if p, ok := seen[1].(Progress); !ok || p != (Progress{Sequence: 41}) {
		t.Fatalf("second arrival %+v, want progress at 41", seen[1])
	}
}

// A plain feed never receives Progress (§3.3): one arriving there is an unknown
// frame, and the feed fails rather than skipping it.
func TestProgressOnAPlainFeedIsAnUnknownFrame(t *testing.T) {
	progress := &writer{}
	progress.u64(41)
	address, _ := feedNode(t, 4, frameOf(frameProgress, progress.buf))
	conn, err := Dial(address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	changes, fail, err := conn.Changes(0, true, "")
	if err != nil {
		t.Fatal(err)
	}
	for change := range changes {
		t.Fatalf("a change arrived: %+v", change)
	}
	if err := <-fail; !errors.Is(err, ErrUnknownFrame) {
		t.Fatalf("the feed ended with %v, want an unknown frame", err)
	}
}
