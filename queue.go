package tessaridb

// Taking work from a queue — queue contract 1.0 (spec/queue-v1.md in the
// protocol repository).
//
// A Queue claims records from a queue the store holds, as one named worker,
// and settles each one: Done deletes it, Release hands it back, now or not
// before a delay. A worker that dies says nothing — its hold lapses after the
// queue's TIMEOUT and the next claim takes the record — so the work is at least
// once; key an outside effect on the queue name and the job's Identity.
//
// The queue itself is declared in the store (DEFINE QUEUE), never by this type:
// declaring it chooses a timeout no client can guess.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Job is one record a claim took. Identity is the store's own spelling — 7,
// 'a7', uuid '…' — written back unchanged when the record is settled (§2), so
// settle a job only through the Queue that claimed it.
type Job struct {
	Identity string
	// Value is the record's own fields, without the engine's hold.
	Value Value
	// Attempts is how many times it has been handed out, 1 the first time.
	Attempts uint64
}

// Handled is what a manual handler decided about a record: Finish, Release or
// Leave.
type Handled interface{ isHandled() }

// Finish: done, the record is deleted.
type Finish struct{}

// Release: hand it back — now, or not before After (the queue must declare a
// NOT BEFORE field for a delay).
type Release struct{ After time.Duration }

func (Finish) isHandled()  {}
func (Release) isHandled() {}
func (Leave) isHandled()   {}

// Queue is a worker taking records from one queue over one connection. Like
// Conn, it is not safe for concurrent use.
type Queue struct {
	conn       *Conn
	statements queueStatements
	batch      int
	where      Filter
}

// NewQueue is the worker named worker taking from queue in namespace/database.
// Names are checked before anything is sent and refused with a *BuilderError.
func NewQueue(conn *Conn, namespace, database, queue, worker string) (*Queue, error) {
	statements, err := newQueueStatements(namespace, database, queue, worker)
	if err != nil {
		return nil, err
	}
	return &Queue{conn: conn, statements: statements, batch: 10}, nil
}

// Batch claims up to n records per claim in the run loops (1 to 500).
func (q *Queue) Batch(n int) *Queue {
	q.batch = n
	return q
}

// Only takes only records matching where in the run loops; its values are bound.
func (q *Queue) Only(where Filter) *Queue {
	q.where = where
	return q
}

// Claim takes up to count records, narrowed by where when it is not nil; an
// empty answer is an empty queue.
func (q *Queue) Claim(count int, where Filter) ([]Job, error) {
	script, parameters, err := q.statements.claim(count, where)
	if err != nil {
		return nil, err
	}
	reply, err := q.conn.Execute(script, parameters)
	if err != nil {
		return nil, err
	}
	answered, ok := last(reply).(Records)
	if !ok {
		return nil, fmt.Errorf("tessaridb: a claim answered %T", last(reply))
	}
	jobs := make([]Job, 0, len(answered.Rows))
	for _, row := range answered.Rows {
		job, err := asJob(row)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// Done says the work is done: the record is deleted.
func (q *Queue) Done(job Job) error {
	_, err := q.conn.Execute(q.statements.done(job.Identity), nil)
	return err
}

// Release hands the record back, now or not before after.
func (q *Queue) Release(job Job, after time.Duration) error {
	_, err := q.conn.Execute(q.statements.release(job.Identity, after), nil)
	return err
}

// RunAuto calls handler for each record: nil marks it done, an error hands it
// back at once. It returns nil when ctx is done — after the running handler has
// finished, its settlement was sent and the rest of the batch handed back — and
// the first refusal or transport failure otherwise.
func (q *Queue) RunAuto(ctx context.Context, handler func(context.Context, Job) error) error {
	return q.run(ctx, func(job Job) error {
		if handler(ctx, job) != nil {
			return q.Release(job, 0)
		}
		return q.Done(job)
	})
}

// RunManual calls handler for each record and does what it returns.
func (q *Queue) RunManual(ctx context.Context, handler func(context.Context, Job) Handled) error {
	return q.run(ctx, func(job Job) error {
		switch decided := handler(ctx, job).(type) {
		case Finish:
			return q.Done(job)
		case Release:
			return q.Release(job, decided.After)
		}
		return nil
	})
}

func (q *Queue) run(ctx context.Context, each func(Job) error) error {
	for {
		jobs, err := q.next(ctx)
		if err != nil || jobs == nil {
			return err
		}
		for i, job := range jobs {
			if err := each(job); err != nil {
				return err
			}
			if ctx.Err() != nil {
				// One statement each (§4.6): RELEASE ALL would reach live siblings.
				for _, unseen := range jobs[i+1:] {
					if err := q.Release(unseen, 0); err != nil {
						return err
					}
				}
				return nil
			}
		}
	}
}

// next answers the next records, waiting while there are none (§4.5), and nil
// once ctx is done. A claim refused with class retry is waited on like an
// empty one (§6).
func (q *Queue) next(ctx context.Context) ([]Job, error) {
	wait := consumerFirstWait
	for ctx.Err() == nil {
		jobs, err := q.Claim(q.batch, q.where)
		var refusal *Refusal
		if err != nil && !(errors.As(err, &refusal) && refusal.Class == RefusalRetry) {
			return nil, err
		}
		if len(jobs) > 0 {
			return jobs, nil
		}
		timer := time.NewTimer(wait)
		// Cancellation first, so a stop during the wait is not held back.
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		wait = min(wait*2, consumerLongestWait)
	}
	return nil, nil
}

func asJob(row Row) (Job, error) {
	object, ok := row.Value.(Object)
	if !ok {
		return Job{}, fmt.Errorf("tessaridb: a claim answered %T", row.Value)
	}
	attempts, err := whole(object.Fields["attempts"])
	if err != nil {
		return Job{}, err
	}
	work := make(map[string]Value, len(object.Fields))
	for name, held := range object.Fields {
		switch name {
		case "attempts", "claimed_until", "claimed_by":
		default:
			work[name] = held
		}
	}
	return Job{Identity: row.Identity, Value: Object{Fields: work}, Attempts: attempts}, nil
}
