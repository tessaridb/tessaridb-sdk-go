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

func TestAKeyHoldsAListAHashASetAndARanking(t *testing.T) {
	c := liveCache(t)
	words := func(items ...string) []Value { return texts(items) }
	if n := must(c.ListPush("l", Back, words("a", "b", "c", "d")...)); n != 4 {
		t.Fatalf("push %d", n)
	}
	if last, ok, err := c.ListAt("l", -1); err != nil || !ok || last != (Text{Value: "d"}) {
		t.Fatalf("at -1: %v %v %v", last, ok, err)
	}
	if got := must(c.ListRange("l", 1, 2)); !reflect.DeepEqual(got, words("b", "c")) {
		t.Fatalf("range %v", got)
	}
	if first, ok, err := c.ListPop("l", Front); err != nil || !ok || first != (Text{Value: "a"}) {
		t.Fatalf("pop %v %v %v", first, ok, err)
	}
	if got := must(c.ListPopMany("l", Back, 2)); !reflect.DeepEqual(got, words("d", "c")) {
		t.Fatalf("pop many %v", got)
	}
	if must(c.ListRemove("l", Text{Value: "b"}, nil)) != 1 || must(c.ListLen("l")) != 0 {
		t.Fatal("an emptied list is no key")
	}
	if _, ok, err := c.ListPop("l", Front); err != nil || ok {
		t.Fatalf("a pop of no list found something: %v %v", ok, err)
	}
	if got := must(c.ListRange("nothing", 0, 5)); len(got) != 0 {
		t.Fatalf("range of no key %v", got)
	}
	must(c.ListPush("t", Back, words("1", "2", "3")...))
	if err := c.ListTrim("t", 0, 1); err != nil || must(c.ListLen("t")) != 2 {
		t.Fatalf("trim %v", err)
	}

	if must(c.HashSet("h", HashField{"x", Integer{Value: 1}}, HashField{"y", Text{Value: "why"}})) != 2 {
		t.Fatal("hash set")
	}
	if y, ok, err := c.HashGet("h", "y"); err != nil || !ok || y != (Text{Value: "why"}) {
		t.Fatalf("hash get %v %v %v", y, ok, err)
	}
	if _, ok, err := c.HashGet("h", "z"); err != nil || ok {
		t.Fatalf("an absent field was found: %v %v", ok, err)
	}
	if must(c.HashIncr("h", "x", 4)) != 5 || must(c.HashLen("h")) != 2 || must(c.HashRemove("h", "y")) != 1 {
		t.Fatal("hash incr, len or remove")
	}
	if got := must(c.HashGetAll("h")); !reflect.DeepEqual(got, map[string]Value{"x": Integer{Value: 5}}) {
		t.Fatalf("get all %v", got)
	}
	if got := must(c.HashGetAll("nothing")); len(got) != 0 {
		t.Fatalf("get all of no key %v", got)
	}

	if must(c.MembersAdd("s", words("p", "q")...)) != 2 || !must(c.MembersHas("s", Text{Value: "p"})) {
		t.Fatal("members add or has")
	}
	if must(c.MembersHas("nothing", Text{Value: "p"})) || must(c.MembersLen("s")) != 2 {
		t.Fatal("members of no key, or len")
	}
	if must(c.MembersRemove("s", Text{Value: "q"})) != 1 {
		t.Fatal("members remove")
	}
	if got := must(c.MembersAll("s")); !reflect.DeepEqual(got, words("p")) {
		t.Fatalf("members all %v", got)
	}

	if must(c.ScoresAdd("r", Scored{"ann", 3}, Scored{"bob", 1}, Scored{"cid", 2})) != 3 {
		t.Fatal("scores add")
	}
	if must(c.ScoresIncr("r", "bob", 0.5)) != 1.5 {
		t.Fatal("scores incr")
	}
	if score, ok, err := c.ScoresScore("r", "ann"); err != nil || !ok || score != 3 {
		t.Fatalf("score %v %v %v", score, ok, err)
	}
	if place, ok, err := c.ScoresRank("r", "ann"); err != nil || !ok || place != 2 {
		t.Fatalf("rank %v %v %v", place, ok, err)
	}
	if _, ok, err := c.ScoresRank("r", "eve"); err != nil || ok {
		t.Fatalf("an unranked member has a place: %v %v", ok, err)
	}
	if got := must(c.ScoresRange("r", 0, 1)); !reflect.DeepEqual(got, []Scored{{"bob", 1.5}, {"cid", 2}}) {
		t.Fatalf("scores range %v", got)
	}
	low := 2.0
	if got := must(c.ScoresBetween("r", &low, nil)); !reflect.DeepEqual(got, []Scored{{"cid", 2}, {"ann", 3}}) {
		t.Fatalf("scores between %v", got)
	}
	if must(c.ScoresRemove("r", "ann")) != 1 {
		t.Fatal("scores remove")
	}
}
