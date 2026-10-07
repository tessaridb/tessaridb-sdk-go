package tessaridb

import (
	"errors"
	"fmt"
)

// A Narrowing names a feed over one table that delivers only the records a
// condition holds for (§3.7, protocol 1.4).
//
// A write that matches arrives as it is. A write that no longer matches, and a
// removal, arrive as a removal when the record matched just before — so a mirror
// applying the feed holds exactly the matching records.
type Narrowing struct {
	// Table is the one table the feed follows; a condition over a whole
	// database is the node's to refuse.
	Table string
	// Condition is TessariQL without the WHERE, judged on the record as this
	// connection's user may see it.
	Condition string
	// Parameters are bound after the node reads the condition, as a request's
	// are, so a value never becomes syntax.
	Parameters map[string]Value
}

// Progress says how far a narrowed feed read past changes it did not send
// (§3.15). Store it as a change's position: resume after Sequence, or from
// Cursor on a split table. Without it a feed whose condition matched nothing
// for a long run would hold a resume point the log may have pruned.
type Progress struct {
	Sequence uint64
	// Cursor is set on a feed over a split table, as Change.Cursor is.
	Cursor string
}

// An Arrival is what a narrowed feed delivers: a Change or a Progress.
type Arrival interface{ arrival() }

func (Change) arrival()   {}
func (Progress) arrival() {}

var narrowedFrames = map[byte]bool{
	frameChange:   true,
	frameProgress: true,
	frameRefusal:  true,
}

// ChangesWhere subscribes this connection to a narrowed feed, resuming after
// resumeAfter (or from the start) exactly as Changes does. It consumes the
// connection, and the returned channel must be drained.
//
// A node whose greeting announced a minor below 4 would read past the condition
// and deliver every change, so nothing is sent to one: the call returns
// ErrNodeTooOld.
func (c *Conn) ChangesWhere(resumeAfter uint64, fromStart bool, narrowing Narrowing) (<-chan Arrival, <-chan error, error) {
	from := resumeAfter + 1
	if fromStart {
		from = 0
	}
	return c.narrowed(from, "", narrowing)
}

// ChangesWhereAt resumes a narrowed feed over a split table after the change or
// progress that carried cursor, as ChangesAt does.
func (c *Conn) ChangesWhereAt(cursor string, narrowing Narrowing) (<-chan Arrival, <-chan error, error) {
	return c.narrowed(0, cursor, narrowing)
}

func (c *Conn) narrowed(from uint64, cursor string, narrowing Narrowing) (<-chan Arrival, <-chan error, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subscribed {
		return nil, nil, errors.New("tessaridb: this connection is already subscribed")
	}
	if c.peerMinor < conditionMinor {
		return nil, nil, fmt.Errorf("%w: it speaks minor %d and a feed's condition needs %d",
			ErrNodeTooOld, c.peerMinor, conditionMinor)
	}
	body, err := subscribeBody(from, narrowing.Table, cursor, &narrowing)
	if err != nil {
		return nil, nil, err
	}
	if err := writeFrame(c.conn, frameSubscribe, body); err != nil {
		return nil, nil, err
	}
	c.subscribed = true
	arrivals, fail := deliver(c, narrowedFrames, func(kind byte, body []byte) (Arrival, error) {
		if kind == frameProgress {
			return readProgress(body)
		}
		return readChange(body)
	})
	return arrivals, fail, nil
}

func readProgress(body []byte) (Progress, error) {
	r := &reader{buf: body}
	sequence, err := r.u64("a progress sequence")
	if err != nil {
		return Progress{}, err
	}
	progress := Progress{Sequence: sequence}
	// §3.15: bytes after the sequence are its cursor; none means the feed has none.
	if r.remaining() > 0 {
		if progress.Cursor, err = r.text("a progress cursor"); err != nil {
			return Progress{}, err
		}
	}
	if r.remaining() > 0 {
		return Progress{}, protocolf("%d bytes after a progress", r.remaining())
	}
	return progress, nil
}
