package tessaridb

// The topic consumer against a running node (consumer contract §7). The waits
// are real: a group's deadline is an instant the NODE compares with its own
// clock.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const consumerUse = "USE NAMESPACE goconsumer; USE DATABASE app;"

// consumerTopic declares a fresh topic per run, so a rerun never meets the last
// run's messages or group.
func consumerTopic(t *testing.T, conn *Conn, stem string, count int, deadline string) string {
	t.Helper()
	name := fmt.Sprintf("%s_%d", stem, time.Now().UnixNano())
	if _, err := conn.Execute("DEFINE NAMESPACE IF NOT EXISTS goconsumer; USE NAMESPACE goconsumer; "+
		"DEFINE DATABASE IF NOT EXISTS app; USE DATABASE app; DEFINE TOPIC "+name+";", nil); err != nil {
		t.Fatal(err)
	}
	var creates strings.Builder
	for n := 1; n <= count; n++ {
		fmt.Fprintf(&creates, " CREATE %s:'m%d' = { n: %d };", name, n, n)
	}
	if _, err := conn.Execute(consumerUse+creates.String()+
		" DEFINE GROUP 'workers' ON TOPIC "+name+" ACK DEADLINE "+deadline+";", nil); err != nil {
		t.Fatal(err)
	}
	return name
}

func nOf(t *testing.T, message Message) int64 {
	t.Helper()
	object, ok := message.Value.(Object)
	if !ok {
		t.Fatalf("%#v", message.Value)
	}
	held, ok := object.Fields["n"].(Integer)
	if !ok {
		t.Fatalf("%#v", object.Fields)
	}
	return held.Value
}

func TestConsumerAutoHandsEveryMessageInOrderAndLeavesNothingInFlight(t *testing.T) {
	conn := node(t)
	name := consumerTopic(t, conn, "auto_jobs", 12, "30s")
	consumer, err := NewConsumer(conn, "goconsumer", "app", name, "workers")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var seen []int64
	err = consumer.Batch(5).RunAuto(ctx, func(_ context.Context, message Message) error {
		seen = append(seen, nOf(t, message))
		if len(seen) == 12 {
			stop()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("handled %v, want %v", seen, want)
	}
	reply, err := conn.Execute(consumerUse+" INFO FOR TOPIC "+name+";", nil)
	if err != nil {
		t.Fatal(err)
	}
	report := last(reply).(ValueOutcome).Value.(Object)
	workers := report.Fields["groups"].(Object).Fields["workers"].(Object)
	if got := workers.Fields["in_flight"]; got != (Integer{Value: 0}) {
		t.Fatalf("in flight %#v", got)
	}
}

func TestConsumerAFailingHandlerSeesTheSameMessageAgainOneDeliveryLater(t *testing.T) {
	conn := node(t)
	name := consumerTopic(t, conn, "flaky_jobs", 2, "30s")
	consumer, err := NewConsumer(conn, "goconsumer", "app", name, "workers")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var seen [][2]uint64
	err = consumer.RunAuto(ctx, func(_ context.Context, message Message) error {
		seen = append(seen, [2]uint64{message.Position, message.Deliveries})
		if len(seen) == 3 {
			stop()
		}
		if message.Position == 1 && message.Deliveries == 1 {
			return errors.New("the first delivery fails once")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][2]uint64{{1, 1}, {1, 2}, {2, 1}}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("handled %v, want %v", seen, want)
	}
}

func TestConsumerManualLeavesAMessageAndTheGroupHandsItOutAgain(t *testing.T) {
	conn := node(t)
	name := consumerTopic(t, conn, "left_jobs", 1, "300ms")
	consumer, err := NewConsumer(conn, "goconsumer", "app", name, "workers")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	var seen []uint64
	err = consumer.RunManual(ctx, func(_ context.Context, message Message) Settle {
		seen = append(seen, message.Deliveries)
		if message.Deliveries == 1 {
			return Leave{}
		}
		stop()
		return Ack{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{1, 2}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("deliveries %v, want %v", seen, want)
	}
}

func TestConsumerRefusesNamesItCannotWriteIntoAStatement(t *testing.T) {
	var refused *BuilderError
	if _, err := NewConsumer(nil, "goconsumer", "app", "jobs; DROP", "workers"); !errors.As(err, &refused) {
		t.Fatalf("a bad topic was accepted: %v", err)
	}
	if _, err := NewConsumer(nil, "goconsumer", "app", "jobs", "it's"); !errors.As(err, &refused) {
		t.Fatalf("a bad group was accepted: %v", err)
	}
}
