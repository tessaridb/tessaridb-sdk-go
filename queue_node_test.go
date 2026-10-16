package tessaridb

// The queue worker against a running node (queue contract §7). The one real
// wait is a release's NOT BEFORE, an instant the NODE compares with its own
// clock.

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const queueUse = "USE NAMESPACE goqueue; USE DATABASE app;"

// liveQueue declares a fresh queue per run, so a rerun never meets the last
// run's work.
func liveQueue(t *testing.T, conn *Conn, stem string, count int, clauses string) string {
	t.Helper()
	name := fmt.Sprintf("%s_%d", stem, time.Now().UnixNano())
	if _, err := conn.Execute("DEFINE NAMESPACE IF NOT EXISTS goqueue; USE NAMESPACE goqueue; "+
		"DEFINE DATABASE IF NOT EXISTS app; USE DATABASE app; DEFINE QUEUE "+name+" "+clauses+";", nil); err != nil {
		t.Fatal(err)
	}
	var creates strings.Builder
	for n := 1; n <= count; n++ {
		kind := "video"
		if n%2 == 0 {
			kind = "image"
		}
		fmt.Fprintf(&creates, " CREATE %s:%d = { n: %d, kind: '%s' };", name, n, n, kind)
	}
	if _, err := conn.Execute(queueUse+creates.String(), nil); err != nil {
		t.Fatal(err)
	}
	return name
}

func jobN(t *testing.T, job Job) int64 {
	t.Helper()
	object, ok := job.Value.(Object)
	if !ok {
		t.Fatalf("%#v", job.Value)
	}
	held, ok := object.Fields["n"].(Integer)
	if !ok {
		t.Fatalf("%#v", object.Fields)
	}
	return held.Value
}

func queueLeft(t *testing.T, conn *Conn, name string) int {
	t.Helper()
	reply, err := conn.Execute(queueUse+" SELECT * FROM "+name+";", nil)
	if err != nil {
		t.Fatal(err)
	}
	answered, ok := last(reply).(Records)
	if !ok {
		t.Fatalf("%#v", last(reply))
	}
	return len(answered.Rows)
}

func newLiveWorker(t *testing.T, conn *Conn, name, worker string) *Queue {
	t.Helper()
	queue, err := NewQueue(conn, "goqueue", "app", name, worker)
	if err != nil {
		t.Fatal(err)
	}
	return queue
}

func TestQueueAutoTakesEveryRecordOnceAndLeavesTheQueueEmpty(t *testing.T) {
	conn := node(t)
	name := liveQueue(t, conn, "auto_jobs", 12, "TIMEOUT 30s")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var seen []int64
	err := newLiveWorker(t, conn, name, "resizer").Batch(5).RunAuto(ctx, func(_ context.Context, job Job) error {
		seen = append(seen, jobN(t, job))
		if len(seen) == 12 {
			stop()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("handled %v, want %v", seen, want)
	}
	if left := queueLeft(t, conn, name); left != 0 {
		t.Fatalf("%d records left", left)
	}
}

func TestQueueAFailingHandlerSeesTheSameRecordAgainOneAttemptLater(t *testing.T) {
	conn := node(t)
	name := liveQueue(t, conn, "flaky_jobs", 2, "TIMEOUT 30s ATTEMPTS 3")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var seen [][2]int64
	err := newLiveWorker(t, conn, name, "resizer").RunAuto(ctx, func(_ context.Context, job Job) error {
		seen = append(seen, [2]int64{jobN(t, job), int64(job.Attempts)})
		if len(seen) == 3 {
			stop()
		}
		if jobN(t, job) == 1 && job.Attempts == 1 {
			return fmt.Errorf("the first attempt fails once")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][2]int64{{1, 1}, {2, 1}, {1, 2}}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("handled %v, want %v", seen, want)
	}
}

func TestQueueAConditionTakesOnlyTheRecordsThatMatch(t *testing.T) {
	conn := node(t)
	name := liveQueue(t, conn, "mixed_jobs", 6, "TIMEOUT 30s")
	worker := newLiveWorker(t, conn, name, "resizer")
	taken, err := worker.Claim(10, Compare("kind", Eq, Text{Value: "image"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, job := range taken {
		got = append(got, jobN(t, job))
		if err := worker.Done(job); err != nil {
			t.Fatal(err)
		}
	}
	if want := []int64{2, 4, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claimed %v, want %v", got, want)
	}
	if left := queueLeft(t, conn, name); left != 3 {
		t.Fatalf("%d records left, want 3", left)
	}
}

func TestQueueAReleaseWithADelayHandsTheRecordOutOnlyAfterIt(t *testing.T) {
	conn := node(t)
	name := liveQueue(t, conn, "later_jobs", 1, "TIMEOUT 30s NOT BEFORE due")
	worker := newLiveWorker(t, conn, name, "resizer")
	taken, err := worker.Claim(1, nil)
	if err != nil || len(taken) != 1 {
		t.Fatalf("%v %v", taken, err)
	}
	if err := worker.Release(taken[0], 400*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if early, err := worker.Claim(1, nil); err != nil || len(early) != 0 {
		t.Fatalf("handed out before its delay: %v %v", early, err)
	}
	time.Sleep(600 * time.Millisecond)
	again, err := worker.Claim(1, nil)
	if err != nil || len(again) != 1 || jobN(t, again[0]) != 1 || again[0].Attempts != 2 {
		t.Fatalf("%v %v", again, err)
	}
}

func TestQueueStopHandsBackWhatTheHandlerNeverSaw(t *testing.T) {
	conn := node(t)
	name := liveQueue(t, conn, "stopped_jobs", 3, "TIMEOUT 30s")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	err := newLiveWorker(t, conn, name, "resizer").Batch(3).RunManual(ctx, func(_ context.Context, _ Job) Handled {
		stop()
		return Leave{}
	})
	if err != nil {
		t.Fatal(err)
	}
	free, err := newLiveWorker(t, conn, name, "other").Claim(3, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, job := range free {
		got = append(got, jobN(t, job))
	}
	if want := []int64{2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claimed %v, want %v", got, want)
	}
}
