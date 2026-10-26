package tessaridb

// A key holding a list, a hash, a set of members or a ranking — cache contract
// 1.1, §2a. Writes are the node's container statements, atomic per key; reads
// are functions over the key's value, so every call is still one statement. A
// read of a key that is not there answers the empty form — an empty slice or
// map, 0, false — and an element, field, score or rank that is not there
// answers found = false.

import (
	"fmt"
	"math"
)

// End is which end of a list.
type End int

const (
	// Front is the first element.
	Front End = iota
	// Back is the last element.
	Back
)

func (e End) word() string {
	if e == Front {
		return "FRONT"
	}
	return "BACK"
}

// HashField is one field of a hash, in the order a write sends it.
type HashField struct {
	Name  string
	Value Value
}

// Scored is a ranked member and its score.
type Scored struct {
	Member string
	Score  float64
}

func refuse(reason string) error { return &CacheArgumentError{Reason: reason} }

// ListPush pushes values at end; the new length.
func (c *Cache) ListPush(key string, end End, values ...Value) (int64, error) {
	if len(values) == 0 {
		return 0, refuse("a push needs a value")
	}
	return c.integer(c.statements.listPush(key, end, values))
}

// ListPop takes one element from end; found is false when the list is not there.
func (c *Cache) ListPop(key string, end End) (value Value, found bool, err error) {
	return c.present(c.statements.listPop(key, end, nil))
}

// ListPopMany takes up to count elements from end, in the order taken.
func (c *Cache) ListPopMany(key string, end End, count int64) ([]Value, error) {
	if count < 0 {
		return nil, refuse("a count is not negative")
	}
	return c.elements(c.statements.listPop(key, end, &count))
}

// ListRange answers count elements from start.
func (c *Cache) ListRange(key string, start, count int64) ([]Value, error) {
	if start < 0 {
		return nil, refuse("a range starts at or after the first element")
	}
	if count < 0 {
		return nil, refuse("a count is not negative")
	}
	return c.elements(c.statements.listRange(key, start, count))
}

// ListAt answers the element at index, a negative one counted from the back.
func (c *Cache) ListAt(key string, index int64) (value Value, found bool, err error) {
	return c.present(c.statements.listAt(key, index))
}

// ListLen answers how many elements the list holds.
func (c *Cache) ListLen(key string) (int64, error) { return c.length(c.statements.listLen(key)) }

// ListRemove removes value — every occurrence, or at most *count; how many went.
func (c *Cache) ListRemove(key string, value Value, count *int64) (int64, error) {
	if count != nil && *count < 0 {
		return 0, refuse("a count is not negative")
	}
	return c.integer(c.statements.listRemove(key, value, count))
}

// ListTrim keeps the elements from start to stop, both included, either
// counted from the back when negative.
func (c *Cache) ListTrim(key string, start, stop int64) error {
	_, err := c.value(c.statements.listTrim(key, start, stop))
	return err
}

// HashSet sets fields, in the order given; how many were new.
func (c *Cache) HashSet(key string, fields ...HashField) (int64, error) {
	if len(fields) == 0 {
		return 0, refuse("a hash write needs a field")
	}
	return c.integer(c.statements.hashSet(key, fields))
}

// HashGet answers one field; found is false when it is not there.
func (c *Cache) HashGet(key, field string) (value Value, found bool, err error) {
	return c.present(c.statements.hashGet(key, field))
}

// HashGetAll answers every field.
func (c *Cache) HashGetAll(key string) (map[string]Value, error) {
	found, err := c.value(c.statements.get(key))
	if err != nil {
		return nil, err
	}
	switch held := found.(type) {
	case None:
		return map[string]Value{}, nil
	case Object:
		return held.Fields, nil
	}
	return nil, fmt.Errorf("tessaridb: a hash read answered %T", found)
}

// HashRemove removes fields; how many went.
func (c *Cache) HashRemove(key string, fields ...string) (int64, error) {
	if len(fields) == 0 {
		return 0, refuse("a hash removal needs a field")
	}
	return c.integer(c.statements.hashRemove(key, fields))
}

// HashIncr adds by to a field; its new value.
func (c *Cache) HashIncr(key, field string, by int64) (int64, error) {
	return c.integer(c.statements.hashIncr(key, field, by))
}

// HashLen answers how many fields the hash holds.
func (c *Cache) HashLen(key string) (int64, error) { return c.length(c.statements.hashLen(key)) }

// MembersAdd adds members; how many were new.
func (c *Cache) MembersAdd(key string, members ...Value) (int64, error) {
	if len(members) == 0 {
		return 0, refuse("a set write needs a member")
	}
	return c.integer(c.statements.members("ADD", key, members))
}

// MembersRemove removes members; how many went.
func (c *Cache) MembersRemove(key string, members ...Value) (int64, error) {
	if len(members) == 0 {
		return 0, refuse("a set removal needs a member")
	}
	return c.integer(c.statements.members("REMOVE", key, members))
}

// MembersHas says whether member is in the set.
func (c *Cache) MembersHas(key string, member Value) (bool, error) {
	return c.flag(c.statements.membersHas(key, member))
}

// MembersAll answers every member.
func (c *Cache) MembersAll(key string) ([]Value, error) {
	found, err := c.value(c.statements.get(key))
	if err != nil {
		return nil, err
	}
	switch held := found.(type) {
	case None:
		return []Value{}, nil
	case Set:
		return held.Items, nil
	}
	return nil, fmt.Errorf("tessaridb: a set read answered %T", found)
}

// MembersLen answers how many members the set holds.
func (c *Cache) MembersLen(key string) (int64, error) { return c.length(c.statements.membersLen(key)) }

// ScoresAdd ranks scores, in the order given; how many members were new.
func (c *Cache) ScoresAdd(key string, scores ...Scored) (int64, error) {
	if len(scores) == 0 {
		return 0, refuse("a ranking write needs a member")
	}
	for _, each := range scores {
		if math.IsNaN(each.Score) || math.IsInf(each.Score, 0) {
			return 0, refuse("a score is a finite number")
		}
	}
	return c.integer(c.statements.scoresAdd(key, scores))
}

// ScoresIncr adds by to a member's score; its new score.
func (c *Cache) ScoresIncr(key, member string, by float64) (float64, error) {
	if math.IsNaN(by) || math.IsInf(by, 0) {
		return 0, refuse("a score is a finite number")
	}
	found, err := c.value(c.statements.scoresIncr(key, member, by))
	if err != nil {
		return 0, err
	}
	score, ok := found.(Float)
	if !ok {
		return 0, fmt.Errorf("tessaridb: a score answered %T", found)
	}
	return score.Value, nil
}

// ScoresRemove removes members; how many went.
func (c *Cache) ScoresRemove(key string, members ...string) (int64, error) {
	if len(members) == 0 {
		return 0, refuse("a ranking removal needs a member")
	}
	return c.integer(c.statements.scoresRemove(key, members))
}

// ScoresScore answers a member's score; found is false when it is not ranked.
func (c *Cache) ScoresScore(key, member string) (score float64, found bool, err error) {
	held, err := c.value(c.statements.scoresOf("score", key, member))
	if err != nil {
		return 0, false, err
	}
	switch answered := held.(type) {
	case None:
		return 0, false, nil
	case Float:
		return answered.Value, true, nil
	}
	return 0, false, fmt.Errorf("tessaridb: a score answered %T", held)
}

// ScoresRank answers a member's place from the lowest score, from 0; found is
// false when it is not ranked.
func (c *Cache) ScoresRank(key, member string) (place int64, found bool, err error) {
	held, err := c.value(c.statements.scoresOf("rank", key, member))
	if err != nil {
		return 0, false, err
	}
	switch answered := held.(type) {
	case None:
		return 0, false, nil
	case Integer:
		return answered.Value, true, nil
	}
	return 0, false, fmt.Errorf("tessaridb: a rank answered %T", held)
}

// ScoresRange answers the members at places start to stop, both included,
// either counted from the highest when negative — lowest first.
func (c *Cache) ScoresRange(key string, start, stop int64) ([]Scored, error) {
	return c.ranked(c.statements.scoresRange(key, start, stop))
}

// ScoresBetween answers the members whose score is from *low to *high, both
// included, a nil bound leaving that side open — lowest first.
func (c *Cache) ScoresBetween(key string, low, high *float64) ([]Scored, error) {
	return c.ranked(c.statements.scoresBetween(key, low, high))
}

func (c *Cache) integer(script string, parameters map[string]Value) (int64, error) {
	found, err := c.value(script, parameters)
	if err != nil {
		return 0, err
	}
	held, ok := found.(Integer)
	if !ok {
		return 0, fmt.Errorf("tessaridb: a container call answered %T", found)
	}
	return held.Value, nil
}

// length is 0 for a key that is not there.
func (c *Cache) length(script string, parameters map[string]Value) (int64, error) {
	found, err := c.value(script, parameters)
	if err != nil {
		return 0, err
	}
	switch held := found.(type) {
	case None:
		return 0, nil
	case Integer:
		return held.Value, nil
	}
	return 0, fmt.Errorf("tessaridb: a length answered %T", found)
}

func (c *Cache) present(script string, parameters map[string]Value) (Value, bool, error) {
	found, err := c.value(script, parameters)
	if err != nil {
		return nil, false, err
	}
	if _, absent := found.(None); absent {
		return nil, false, nil
	}
	return found, true, nil
}

// elements answers none for a key that is not there.
func (c *Cache) elements(script string, parameters map[string]Value) ([]Value, error) {
	found, err := c.value(script, parameters)
	if err != nil {
		return nil, err
	}
	switch held := found.(type) {
	case None:
		return []Value{}, nil
	case Array:
		return held.Items, nil
	}
	return nil, fmt.Errorf("tessaridb: a container read answered %T", found)
}

func (c *Cache) ranked(script string, parameters map[string]Value) ([]Scored, error) {
	pairs, err := c.elements(script, parameters)
	if err != nil {
		return nil, err
	}
	out := make([]Scored, 0, len(pairs))
	for _, pair := range pairs {
		held, ok := pair.(Array)
		if !ok || len(held.Items) != 2 {
			return nil, fmt.Errorf("tessaridb: a ranking read answered %T", pair)
		}
		member, isText := held.Items[0].(Text)
		score, isFloat := held.Items[1].(Float)
		if !isText || !isFloat {
			return nil, fmt.Errorf("tessaridb: a ranking read answered %v", held.Items)
		}
		out = append(out, Scored{Member: member.Value, Score: score.Value})
	}
	return out, nil
}
