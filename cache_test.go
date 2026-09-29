package tessaridb

// The cache handle's statements, byte for byte, as cache contract §2 writes them.

import (
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestEveryCacheStatementIsTheOneTheContractWrites(t *testing.T) {
	s, err := newCacheStatements("app", "main", "cache")
	if err != nil {
		t.Fatal(err)
	}
	ttl := Duration{Seconds: 30}
	after := "user:1"
	type rendered struct {
		script string
		given  map[string]Value
	}
	r := func(script string, given map[string]Value) rendered { return rendered{script, given} }
	cases := []struct {
		got       rendered
		statement string
		bound     []string
	}{
		{r(s.get("k")), "GET cache:$k;", []string{"k"}},
		{r(s.set("k", Null{}, "", nil, nil)), "SET cache:$k = $v;", []string{"k", "v"}},
		{r(s.set("k", Null{}, "", nil, &ttl)), "SET cache:$k = $v EXPIRE $t;", []string{"k", "t", "v"}},
		{r(s.set("k", Null{}, " IF ABSENT", nil, &ttl)), "SET cache:$k = $v IF ABSENT EXPIRE $t;", []string{"k", "t", "v"}},
		{r(s.set("k", Null{}, " IF PRESENT", nil, nil)), "SET cache:$k = $v IF PRESENT;", []string{"k", "v"}},
		{r(s.set("k", Null{}, " IF = $e", Null{}, nil)), "SET cache:$k = $v IF = $e;", []string{"e", "k", "v"}},
		{r(s.delete("k")), "DELETE cache:$k RETURN BEFORE;", []string{"k"}},
		{r(s.incr("k", 5)), "INCR cache:$k BY $n;", []string{"k", "n"}},
		{r(s.ttl("k")), "RETURN TTL cache:$k;", []string{"k"}},
		{r(s.expire("k", ttl)), "EXPIRE cache:$k $t;", []string{"k", "t"}},
		{r(s.persist("k")), "PERSIST cache:$k;", []string{"k"}},
		{r(s.keys("", nil, 100)), "KEYS FROM cache LIMIT 100;", []string{}},
		{r(s.keys("user:", &after, 10)), "KEYS FROM cache PREFIX $p AFTER $a LIMIT 10;", []string{"a", "p"}},
		{r(s.lock("k", "w1", ttl)), "SET cache:$k = $h IF ABSENT EXPIRE $t;", []string{"h", "k", "t"}},
		{r(s.extend("k", "w1", ttl)), "SET cache:$k = $h IF = $h EXPIRE $t;", []string{"h", "k", "t"}},
		{r(s.release("k", "w1")), "SET cache:$k = 'free' IF = $h EXPIRE 1ms;", []string{"h", "k"}},
	}
	for _, each := range cases {
		t.Run(each.statement, func(t *testing.T) {
			if want := "USE NAMESPACE app; USE DATABASE main; " + each.statement; each.got.script != want {
				t.Fatalf("got %q, want %q", each.got.script, want)
			}
			names := make([]string, 0, len(each.got.given))
			for name := range each.got.given {
				names = append(names, name)
			}
			sort.Strings(names)
			if !reflect.DeepEqual(names, each.bound) {
				t.Fatalf("bound %v, want %v", names, each.bound)
			}
		})
	}
}

func TestACacheNameThatIsNotOneIsRefused(t *testing.T) {
	_, err := newCacheStatements("app", "main", "ca-che")
	var refused *BuilderError
	if !errors.As(err, &refused) {
		t.Fatalf("got %v", err)
	}
}

func TestACacheTTLIsPositiveAndExact(t *testing.T) {
	cases := []struct {
		ttl  time.Duration
		want Duration
		ok   bool
	}{
		{1500 * time.Millisecond, Duration{Seconds: 1, Nanos: 500_000_000}, true},
		{0, Duration{}, false},
		{-time.Second, Duration{}, false},
	}
	for _, each := range cases {
		got, ok := cacheDuration(each.ttl)
		if got != each.want || ok != each.ok {
			t.Fatalf("%v: got %v %v", each.ttl, got, ok)
		}
	}
}

func TestAQuotedKeyIsTheStringAgain(t *testing.T) {
	cases := map[string]string{`'user:1'`: "user:1", `'it\'s'`: "it's", `'a\\b'`: `a\b`, "42": "42"}
	for spelled, want := range cases {
		if got := unquoted(spelled); got != want {
			t.Fatalf("%s: got %q, want %q", spelled, got, want)
		}
	}
}
