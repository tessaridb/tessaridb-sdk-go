package tessaridb

// Consuming a topic as a member of a consumer group — consumer contract 1.0
// (spec/consumer-v1.md in the protocol repository).
//
// A Consumer reads a topic under a group the store holds, and calls a handler
// once per message in the order the group hands them out. The group, not the
// connection, keeps the state — the last position handed out and what is in
// flight — so a process that crashes loses nothing it had not acknowledged, and
// another under the same group name carries on.
//
// The group itself is declared in the store (DEFINE GROUP), never by this type:
// declaring it is a schema act that chooses a deadline no client can guess.

import (
	"context"
	"fmt"
	"time"
)

const (
	// consumerFirstWait is the first wait after a read that answered nothing
	// (§4.5).
	consumerFirstWait = 50 * time.Millisecond
	// consumerLongestWait is the longest wait between reads that answer nothing.
	consumerLongestWait = time.Second
)

// Message is one message, as the group handed it out.
type Message struct {
	// Position in the topic, from 1 — with the topic and group names, a stable
	// key for making an outside effect idempotent.
	Position   uint64
	Value      Value
	Deliveries uint64
}

// Settle is what a manual handler decided about a message: Ack, Nack or Leave.
type Settle interface{ isSettle() }

// Ack: done, never handed out to this group again.
type Ack struct{}

// Nack: hand it out again — now, or after Delay.
type Nack struct{ Delay time.Duration }

// Leave: neither; the group hands it out again when its deadline passes.
type Leave struct{}

func (Ack) isSettle()   {}
func (Nack) isSettle()  {}
func (Leave) isSettle() {}

// Consumer is a member of a consumer group reading one topic over one
// connection. Like Conn, it is not safe for concurrent use.
type Consumer struct {
	conn       *Conn
	statements consumerStatements
	batch      int
}

// NewConsumer is a member of group reading topic in namespace/database. The
// connection should already carry its credentials when the store is closed: a
// wire connection proves who it is once and keeps that identity. Names are
// checked before anything is sent and refused with a *BuilderError.
func NewConsumer(conn *Conn, namespace, database, topic, group string) (*Consumer, error) {
	statements, err := newConsumerStatements(namespace, database, topic, group)
	if err != nil {
		return nil, err
	}
	return &Consumer{conn: conn, statements: statements, batch: 10}, nil
}

// Batch asks for up to n messages per read (at least one).
func (c *Consumer) Batch(n int) *Consumer {
	c.batch = max(n, 1)
	return c
}

// RunAuto calls handler for each message: nil acknowledges it, an error hands it
// back at once. It returns nil when ctx is done — after the running handler has
// finished and its acknowledgement was sent — and the first refusal or
// transport failure otherwise.
func (c *Consumer) RunAuto(ctx context.Context, handler func(context.Context, Message) error) error {
	return c.run(ctx, func(message Message) error {
		if handler(ctx, message) != nil {
			_, err := c.Nack(0, message.Position)
			return err
		}
		_, err := c.Ack(message.Position)
		return err
	})
}

// RunManual calls handler for each message and does what it returns.
func (c *Consumer) RunManual(ctx context.Context, handler func(context.Context, Message) Settle) error {
	return c.run(ctx, func(message Message) error {
		var err error
		switch decided := handler(ctx, message).(type) {
		case Ack:
			_, err = c.Ack(message.Position)
		case Nack:
			_, err = c.Nack(decided.Delay, message.Position)
		}
		return err
	})
}

func (c *Consumer) run(ctx context.Context, each func(Message) error) error {
	for {
		messages, err := c.next(ctx)
		if err != nil || messages == nil {
			return err
		}
		for _, message := range messages {
			if err := each(message); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return nil
			}
		}
	}
}

// Ack acknowledges these positions and answers how many were in flight. A
// position that was not counts nothing and is not an error.
func (c *Consumer) Ack(positions ...uint64) (uint64, error) {
	if len(positions) == 0 {
		return 0, nil
	}
	return c.settle(c.statements.ack(positions))
}

// Nack hands these positions back, now or after delay, and answers how many
// were in flight.
func (c *Consumer) Nack(delay time.Duration, positions ...uint64) (uint64, error) {
	if len(positions) == 0 {
		return 0, nil
	}
	return c.settle(c.statements.nack(delay, positions))
}

func (c *Consumer) settle(script string, parameters map[string]Value, err error) (uint64, error) {
	if err != nil {
		return 0, err
	}
	reply, err := c.conn.Execute(script, parameters)
	if err != nil {
		return 0, err
	}
	answered, ok := last(reply).(ValueOutcome)
	if !ok {
		return 0, fmt.Errorf("tessaridb: an acknowledgement answered %T", last(reply))
	}
	return whole(answered.Value)
}

// next answers the next messages, waiting while there are none (§4.5), and nil
// once ctx is done.
func (c *Consumer) next(ctx context.Context) ([]Message, error) {
	wait := consumerFirstWait
	for ctx.Err() == nil {
		reply, err := c.conn.Execute(c.statements.read(c.batch), nil)
		if err != nil {
			return nil, err
		}
		answered, ok := last(reply).(Records)
		if !ok {
			return nil, fmt.Errorf("tessaridb: a group read answered %T", last(reply))
		}
		if len(answered.Rows) > 0 {
			messages := make([]Message, 0, len(answered.Rows))
			for _, row := range answered.Rows {
				message, err := asMessage(row.Value)
				if err != nil {
					return nil, err
				}
				messages = append(messages, message)
			}
			return messages, nil
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

func last(reply *Reply) Outcome {
	if reply == nil || len(reply.Outcomes) == 0 {
		return nil
	}
	return reply.Outcomes[len(reply.Outcomes)-1]
}

func whole(value Value) (uint64, error) {
	if held, ok := value.(Integer); ok && held.Value >= 0 {
		return uint64(held.Value), nil
	}
	return 0, fmt.Errorf("tessaridb: expected a whole number, got %T", value)
}

func asMessage(body Value) (Message, error) {
	object, ok := body.(Object)
	if !ok {
		return Message{}, fmt.Errorf("tessaridb: a message answered %T", body)
	}
	position, err := whole(object.Fields["position"])
	if err != nil {
		return Message{}, err
	}
	deliveries, err := whole(object.Fields["deliveries"])
	if err != nil {
		return Message{}, err
	}
	value, ok := object.Fields["value"]
	if !ok {
		value = None{}
	}
	return Message{Position: position, Value: value, Deliveries: deliveries}, nil
}
