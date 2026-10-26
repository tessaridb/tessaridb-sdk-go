package tessaridb

// The container calls' statements, byte for byte, as cache contract §2a writes
// them, and the refusals a client makes before sending.

import (
	"errors"
	"math"
	"reflect"
	"sort"
	"testing"
)

func TestEveryContainerStatementIsTheOneTheContractWrites(t *testing.T) {
	s, err := newCacheStatements("app", "main", "cache")
	if err != nil {
		t.Fatal(err)
	}
	two := []Value{Text{Value: "a"}, Text{Value: "b"}}
	count := int64(2)
	low, high := 1.5, 9.0
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
		{r(s.listPush("k", Back, two)), "LIST PUSH cache:$k BACK $v0, $v1;", []string{"k", "v0", "v1"}},
		{r(s.listPush("k", Front, two[:1])), "LIST PUSH cache:$k FRONT $v0;", []string{"k", "v0"}},
		{r(s.listPop("k", Front, nil)), "LIST POP cache:$k FRONT;", []string{"k"}},
		{r(s.listPop("k", Back, &count)), "LIST POP cache:$k BACK LIMIT $n;", []string{"k", "n"}},
		{r(s.listRange("k", 0, 10)), "RETURN array::slice(GET cache:$k, $i, $n);", []string{"i", "k", "n"}},
		{r(s.listAt("k", -1)), "RETURN array::at(GET cache:$k, $i);", []string{"i", "k"}},
		{r(s.listLen("k")), "RETURN array::len(GET cache:$k);", []string{"k"}},
		{r(s.listRemove("k", Text{Value: "a"}, nil)), "LIST REMOVE cache:$k $v;", []string{"k", "v"}},
		{r(s.listRemove("k", Text{Value: "a"}, &count)), "LIST REMOVE cache:$k $v LIMIT $n;", []string{"k", "n", "v"}},
		{r(s.listTrim("k", 0, -1)), "LIST TRIM cache:$k FROM $i TO $j;", []string{"i", "j", "k"}},
		{r(s.hashSet("k", []HashField{{"a", Integer{Value: 1}}, {"b", Null{}}})),
			"HASH SET cache:$k $f0 = $v0, $f1 = $v1;", []string{"f0", "f1", "k", "v0", "v1"}},
		{r(s.hashGet("k", "a")), "RETURN object::get(GET cache:$k, $f);", []string{"f", "k"}},
		{r(s.get("k")), "GET cache:$k;", []string{"k"}},
		{r(s.hashRemove("k", []string{"a", "b"})), "HASH REMOVE cache:$k $f0, $f1;", []string{"f0", "f1", "k"}},
		{r(s.hashIncr("k", "a", 1)), "HASH INCR cache:$k $f BY $n;", []string{"f", "k", "n"}},
		{r(s.hashLen("k")), "RETURN object::len(GET cache:$k);", []string{"k"}},
		{r(s.members("ADD", "k", two)), "MEMBERS ADD cache:$k $v0, $v1;", []string{"k", "v0", "v1"}},
		{r(s.members("REMOVE", "k", two)), "MEMBERS REMOVE cache:$k $v0, $v1;", []string{"k", "v0", "v1"}},
		{r(s.membersHas("k", Text{Value: "a"})), "RETURN $v IN GET cache:$k;", []string{"k", "v"}},
		{r(s.membersLen("k")), "RETURN set::len(GET cache:$k);", []string{"k"}},
		{r(s.scoresAdd("k", []Scored{{"a", 1}, {"b", 2.5}})),
			"SCORES ADD cache:$k $m0 = $c0, $m1 = $c1;", []string{"c0", "c1", "k", "m0", "m1"}},
		{r(s.scoresIncr("k", "a", 0.5)), "SCORES INCR cache:$k $m BY $c;", []string{"c", "k", "m"}},
		{r(s.scoresRemove("k", []string{"a", "b"})), "SCORES REMOVE cache:$k $m0, $m1;", []string{"k", "m0", "m1"}},
		{r(s.scoresOf("score", "k", "a")), "RETURN scores::score(GET cache:$k, $m);", []string{"k", "m"}},
		{r(s.scoresOf("rank", "k", "a")), "RETURN scores::rank(GET cache:$k, $m);", []string{"k", "m"}},
		{r(s.scoresRange("k", 0, -1)), "RETURN scores::range(GET cache:$k, $i, $j);", []string{"i", "j", "k"}},
		{r(s.scoresBetween("k", &low, &high)), "RETURN scores::between(GET cache:$k, $lo, $hi);", []string{"hi", "k", "lo"}},
		{r(s.scoresBetween("k", nil, &high)), "RETURN scores::between(GET cache:$k, NONE, $hi);", []string{"hi", "k"}},
		{r(s.scoresBetween("k", &low, nil)), "RETURN scores::between(GET cache:$k, $lo, NONE);", []string{"k", "lo"}},
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

func TestAContainerCallTheContractForbidsIsRefusedBeforeSending(t *testing.T) {
	// No connection: a refusal must come before anything is sent.
	c, err := NewCache(nil, "app", "main", "cache")
	if err != nil {
		t.Fatal(err)
	}
	negative := int64(-1)
	cases := []struct {
		name string
		call func() error
	}{
		{"an empty push", func() error { _, err := c.ListPush("k", Back); return err }},
		{"a negative pop count", func() error { _, err := c.ListPopMany("k", Front, -1); return err }},
		{"a negative range start", func() error { _, err := c.ListRange("k", -1, 1); return err }},
		{"a negative range count", func() error { _, err := c.ListRange("k", 0, -1); return err }},
		{"a negative remove count", func() error { _, err := c.ListRemove("k", Null{}, &negative); return err }},
		{"an empty hash write", func() error { _, err := c.HashSet("k"); return err }},
		{"an empty hash removal", func() error { _, err := c.HashRemove("k"); return err }},
		{"an empty set write", func() error { _, err := c.MembersAdd("k"); return err }},
		{"an empty set removal", func() error { _, err := c.MembersRemove("k"); return err }},
		{"an empty ranking write", func() error { _, err := c.ScoresAdd("k"); return err }},
		{"an empty ranking removal", func() error { _, err := c.ScoresRemove("k"); return err }},
		{"a NaN score", func() error { _, err := c.ScoresAdd("k", Scored{"a", math.NaN()}); return err }},
		{"an infinite increment", func() error { _, err := c.ScoresIncr("k", "a", math.Inf(1)); return err }},
	}
	for _, each := range cases {
		t.Run(each.name, func(t *testing.T) {
			var refused *CacheArgumentError
			if err := each.call(); !errors.As(err, &refused) {
				t.Fatalf("got %v, want a *CacheArgumentError", err)
			}
		})
	}
}
