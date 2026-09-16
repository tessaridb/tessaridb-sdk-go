package tessaridb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"unicode/utf8"
)

// ErrProtocol is returned when bytes do not conform to the specification. It is
// never a retry.
var ErrProtocol = errors.New("tessaridb: protocol")

func protocolf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrProtocol}, args...)...)
}

// The protocol has two primitive layers and they do not agree.
//
// At the frame layer a number is plain big-endian. At the VALUE layer a signed
// 64-bit integer is written big-endian with the top bit of its first byte
// inverted, so that the byte order of the encoding matches the numeric order of
// the value. Integer(1) is 80 00 00 00 00 00 00 01, not 00 00 00 00 00 00 00 01.
//
// This is the rule a new client gets wrong while every one of its own tests
// passes: writing plain big-endian makes every integer, duration, datetime and
// integer record id wrong, and round-trips perfectly against itself. Only the
// shared corpus catches it.
const inversion = 0x80

type writer struct{ buf []byte }

func (w *writer) u8(b byte) { w.buf = append(w.buf, b) }

func (w *writer) u32(n uint32) {
	w.buf = binary.BigEndian.AppendUint32(w.buf, n)
}

func (w *writer) u64(n uint64) {
	w.buf = binary.BigEndian.AppendUint64(w.buf, n)
}

func (w *writer) i64Inverted(n int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(n))
	b[0] ^= inversion
	w.buf = append(w.buf, b[:]...)
}

func (w *writer) f64Bits(f float64) {
	w.u64(math.Float64bits(f))
}

// i128 writes a decimal's mantissa: sixteen bytes, two's complement, and PLAIN.
// An Integer is inverted and this is not — three numeric encodings, two
// conventions, in one protocol.
func (w *writer) i128(n *big.Int) {
	v := new(big.Int).Set(n)
	if v.Sign() < 0 {
		v.Add(v, new(big.Int).Lsh(big.NewInt(1), 128))
	}
	var b [16]byte
	v.FillBytes(b[:])
	w.buf = append(w.buf, b[:]...)
}

func (w *writer) fixed(b []byte) { w.buf = append(w.buf, b...) }

// lenbytes is a u32 length then the bytes.
func (w *writer) lenbytes(b []byte) {
	w.u32(uint32(len(b)))
	w.fixed(b)
}

// text is a u32 byte-length then UTF-8. Not NUL-terminated.
func (w *writer) text(s string) { w.lenbytes([]byte(s)) }

// varbytes is escaped and terminated: 0x00 becomes 0x00 0xFF, then 0x00 0x01
// ends it. The escape is byte-local, which is what makes the encoding of a
// prefix a byte prefix of the encoding of the whole.
func (w *writer) varbytes(b []byte) {
	for _, c := range b {
		w.u8(c)
		if c == 0x00 {
			w.u8(0xff)
		}
	}
	w.u8(0x00)
	w.u8(0x01)
}

type reader struct {
	buf []byte
	at  int
}

func (r *reader) remaining() int { return len(r.buf) - r.at }

func (r *reader) need(n int, what string) error {
	if r.remaining() < n {
		return protocolf("%s needs %d bytes, %d remain", what, n, r.remaining())
	}
	return nil
}

func (r *reader) u8(what string) (byte, error) {
	if err := r.need(1, what); err != nil {
		return 0, err
	}
	b := r.buf[r.at]
	r.at++
	return b, nil
}

func (r *reader) u32(what string) (uint32, error) {
	if err := r.need(4, what); err != nil {
		return 0, err
	}
	n := binary.BigEndian.Uint32(r.buf[r.at:])
	r.at += 4
	return n, nil
}

func (r *reader) u64(what string) (uint64, error) {
	if err := r.need(8, what); err != nil {
		return 0, err
	}
	n := binary.BigEndian.Uint64(r.buf[r.at:])
	r.at += 8
	return n, nil
}

func (r *reader) i64Inverted(what string) (int64, error) {
	if err := r.need(8, what); err != nil {
		return 0, err
	}
	var b [8]byte
	copy(b[:], r.buf[r.at:r.at+8])
	b[0] ^= inversion
	r.at += 8
	return int64(binary.BigEndian.Uint64(b[:])), nil
}

func (r *reader) i128(what string) (*big.Int, error) {
	if err := r.need(16, what); err != nil {
		return nil, err
	}
	b := r.buf[r.at : r.at+16]
	v := new(big.Int).SetBytes(b)
	if b[0]&0x80 != 0 {
		v.Sub(v, new(big.Int).Lsh(big.NewInt(1), 128))
	}
	r.at += 16
	return v, nil
}

func (r *reader) fixed(n int, what string) ([]byte, error) {
	if err := r.need(n, what); err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, r.buf[r.at:r.at+n])
	r.at += n
	return out, nil
}

func (r *reader) lenbytes(what string) ([]byte, error) {
	n, err := r.u32(what + " length")
	if err != nil {
		return nil, err
	}
	return r.fixed(int(n), what)
}

// text reads UTF-8, and invalid UTF-8 is an error rather than a replacement
// character: a name that silently became U+FFFD would compare unequal to itself.
func (r *reader) text(what string) (string, error) {
	b, err := r.lenbytes(what)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", protocolf("%s is not valid UTF-8", what)
	}
	return string(b), nil
}

func (r *reader) varbytes(what string) ([]byte, error) {
	out := []byte{}
	for {
		b, err := r.u8(what)
		if err != nil {
			return nil, err
		}
		if b != 0x00 {
			out = append(out, b)
			continue
		}
		next, err := r.u8(what + " escape")
		if err != nil {
			return nil, err
		}
		switch next {
		case 0xff:
			out = append(out, 0x00)
		case 0x01:
			return out, nil
		default:
			return nil, protocolf("%s: 0x00 followed by 0x%02x, which is neither an escape nor a terminator", what, next)
		}
	}
}
