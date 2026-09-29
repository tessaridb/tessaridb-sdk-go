package tessaridb

// A space as a cache against a running node (cache contract §6). The release
// test waits on the wall clock: an expiry is an instant the NODE compares with
// its own clock.

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func liveCache(t *testing.T) *Cache {
	t.Helper()
	conn := node(t)
	space := fmt.Sprintf("cache_%d", time.Now().UnixNano())
	if _, err := conn.Execute("DEFINE NAMESPACE IF NOT EXISTS gocache; USE NAMESPACE gocache; "+
		"DEFINE DATABASE IF NOT EXISTS app; USE DATABASE app; DEFINE SPACE "+space+";", nil); err != nil {
		t.Fatal(err)
	}
	cache, err := NewCache(conn, "gocache", "app", space)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

// must answers value, or fails the test by panicking with err: Go will not pass
// a two-value call beside another argument, so a *testing.T cannot ride along.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func TestACacheValueIsStoredReadCountedExpiredAndDeleted(t *testing.T) {
	c := liveCache(t)
	at := Datetime{Seconds: 1_790_000_000, Nanos: 5}
	if err := c.Set("user:42", at, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get("user:42")
	if err != nil || !ok || got != at {
		t.Fatalf("got %v %v %v", got, ok, err)
	}
	if ttl := must(c.TTL("user:42")); ttl.Kind != TTLExpires || ttl.Left > 30*time.Second {
		t.Fatalf("ttl %v", ttl)
	}
	if err := c.Set("user:42", Integer{Value: 1}, 0); err != nil {
		t.Fatal(err)
	}
	if ttl := must(c.TTL("user:42")); ttl.Kind != TTLNever {
		t.Fatalf("a plain set kept the expiry: %v", ttl)
	}
	if !must(c.Expire("user:42", time.Minute)) || !must(c.Persist("user:42")) {
		t.Fatal("expire or persist found no key")
	}
	if ttl := must(c.TTL("nobody")); ttl.Kind != TTLAbsent {
		t.Fatalf("ttl %v", ttl)
	}
	if must(c.Incr("hits", 5)) != 5 || must(c.Incr("hits", 1)) != 6 {
		t.Fatal("incr")
	}
	if !must(c.SetIfAbsent("once", Integer{Value: 1}, 0)) || must(c.SetIfAbsent("once", Integer{Value: 2}, 0)) {
		t.Fatal("set if absent")
	}
	if must(c.SetIfPresent("never", Integer{Value: 1}, 0)) {
		t.Fatal("set if present wrote a missing key")
	}
	if must(c.CompareAndSet("once", Integer{Value: 9}, Integer{Value: 3}, 0)) ||
		!must(c.CompareAndSet("once", Integer{Value: 1}, Integer{Value: 3}, 0)) {
		t.Fatal("compare and set")
	}
	key := `it's\here`
	if err := c.Set(key, Null{}, 0); err != nil {
		t.Fatal(err)
	}
	if keys := must(c.Keys("it", nil, 10)); !reflect.DeepEqual(keys, []string{key}) {
		t.Fatalf("a quoted key did not come back as the string: %q", keys)
	}
	if keys := must(c.Keys("", &key, 1)); !reflect.DeepEqual(keys, []string{"once"}) {
		t.Fatalf("after: %q", keys)
	}
	if !must(c.Delete(key)) || must(c.Delete(key)) {
		t.Fatal("delete: a key holding NULL is a key, and a second delete finds none")
	}
	var refused *CacheArgumentError
	if _, err := c.Keys("", nil, 0); !errors.As(err, &refused) {
		t.Fatalf("limit 0: %v", err)
	}
}

func TestACacheGetOrSetLoadsOnce(t *testing.T) {
	c := liveCache(t)
	load := func(v string) func() (Value, error) { return func() (Value, error) { return Text{Value: v}, nil } }
	first := must(c.GetOrSet("page", 30*time.Second, load("rendered")))
	second := must(c.GetOrSet("page", 30*time.Second, load("again")))
	if first != (Text{Value: "rendered"}) || second != first {
		t.Fatalf("%v %v", first, second)
	}
	if ttl := must(c.TTL("page")); ttl.Kind != TTLExpires {
		t.Fatal("stored without its ttl")
	}
}

func TestACacheLeaseIsExtendedByItsHolderAndReleasedSoTheNextCanTakeIt(t *testing.T) {
	c := liveCache(t)
	lease := must(c.Lock("report", 30*time.Second, ""))
	if lease == nil || len(lease.Holder) != 32 {
		t.Fatalf("lease %v", lease)
	}
	if other := must(c.Lock("report", 30*time.Second, "other")); other != nil {
		t.Fatal("a held lock was taken")
	}
	if !must(lease.Extend(0)) {
		t.Fatal("its holder could not extend it")
	}
	if must(c.LeaseOf("report", "other", 30*time.Second).Release()) {
		t.Fatal("another holder released it")
	}
	if !must(lease.Release()) {
		t.Fatal("release")
	}
	time.Sleep(20 * time.Millisecond)
	if next := must(c.Lock("report", 30*time.Second, "next")); next == nil {
		t.Fatal("a released lock could not be taken again, so the release left it permanent")
	}
	if got, _, _ := c.Get("report"); got != (Text{Value: "next"}) {
		t.Fatalf("holder %v", got)
	}
}
