package tessaridb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// The frame kinds a client meets. Six through twelve belong to the link between
// nodes and never reach a client.
const (
	frameRequest   = 1
	frameAnswer    = 2
	frameRefusal   = 3
	frameSubscribe = 4
	frameChange    = 5
	frameElsewhere = 13
)

const headerBytes = 5

// Ceiling is the largest frame body this protocol carries, enforced in BOTH
// directions and checked before anything is allocated — a length field is an
// allocation instruction from the other end of a socket, and a client that
// trusts it hands its memory to whoever it dialled.
const Ceiling = 16 * 1024 * 1024

// ErrTooLarge is returned when a frame body exceeds Ceiling.
var ErrTooLarge = errors.New("tessaridb: frame body exceeds the 16 MiB ceiling")

// ErrUnknownFrame is returned for a frame kind this build does not know.
//
// An unknown kind CLOSES the connection rather than being skipped. The frame's
// length says how far the body runs, so stepping over it looks safe — but a kind
// this build does not understand may have changed what the following bytes mean,
// and continuing would be reading the rest of the conversation under an
// assumption the peer does not share.
var ErrUnknownFrame = errors.New("tessaridb: unknown frame kind")

// ErrTruncated is returned when the peer stopped mid-frame.
//
// Zero bytes BETWEEN frames is a clean goodbye. Zero bytes INSIDE one is a
// truncation, and the two must not be reported the same way: one is the peer
// hanging up and the other is the peer dying.
var ErrTruncated = errors.New("tessaridb: the peer stopped in the middle of a frame")

var (
	magic = [4]byte{'T', 'E', 'S', 'S'}
	// The version this client speaks.
	major byte = 1
	minor byte = 1
)

func greeting() []byte {
	return []byte{magic[0], magic[1], magic[2], magic[3], major, minor}
}

func writeFrame(w io.Writer, kind byte, body []byte) error {
	if len(body) > Ceiling {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, len(body))
	}
	header := make([]byte, 0, headerBytes+len(body))
	header = append(header, kind)
	header = append(header, byte(len(body)>>24), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	header = append(header, body...)
	_, err := w.Write(header)
	return err
}

// readGreeting checks the magic on its own four bytes BEFORE looking at the
// version.
//
// The order is the whole point: something that is not this protocol at all —
// a TLS ClientHello, an HTTP request, a port scanner — must be reported as
// "this is not a TessariDB node", not as "this node speaks a version I do not".
// The second message sends an operator looking for an upgrade that does not
// exist.
func readGreeting(r io.Reader) error {
	var head [6]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return fmt.Errorf("tessaridb: no greeting from the peer: %w", err)
	}
	if !bytes.Equal(head[:4], magic[:]) {
		return fmt.Errorf("tessaridb: the peer is not speaking this protocol (magic %q)", head[:4])
	}
	if head[4] != major {
		// A differing major is a refusal. A differing minor is not: the minor
		// exists so that a node and a client may disagree about it and still
		// talk.
		return fmt.Errorf("tessaridb: the peer speaks major %d and this client speaks %d", head[4], major)
	}
	return nil
}

// readFrame reads one frame and returns its kind and body.
//
// known says which kinds are expected at this point in the conversation. A kind
// outside it is drained and then reported, so the caller can close cleanly
// rather than leaving a half-read body in the stream.
func readFrame(r io.Reader, known map[byte]bool) (byte, []byte, error) {
	var header [headerBytes]byte
	n, err := io.ReadFull(r, header[:])
	if err != nil {
		if n == 0 && (errors.Is(err, io.EOF)) {
			// A clean boundary: the peer said goodbye between frames.
			return 0, nil, io.EOF
		}
		return 0, nil, fmt.Errorf("%w: a frame header", ErrTruncated)
	}

	kind := header[0]
	length := int(header[1])<<24 | int(header[2])<<16 | int(header[3])<<8 | int(header[4])
	if length > Ceiling {
		// Refused before allocating, which is the point of the ceiling.
		return kind, nil, fmt.Errorf("%w: %d bytes announced", ErrTooLarge, length)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return kind, nil, fmt.Errorf("%w: a frame body of %d bytes", ErrTruncated, length)
	}
	if !known[kind] {
		return kind, nil, fmt.Errorf("%w %d", ErrUnknownFrame, kind)
	}
	return kind, body, nil
}
