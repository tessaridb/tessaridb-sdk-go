package tessaridb

// The container calls' statements, as cache contract §2a writes them: a list of
// values is bound one parameter each, a hash field or ranked member as a pair.

import (
	"strconv"
	"strings"
)

func (s cacheStatements) numbered(statement, key, prefix string, values []Value) (string, map[string]Value) {
	given := map[string]Value{"k": Text{Value: key}}
	names := make([]string, 0, len(values))
	for at, value := range values {
		name := prefix + strconv.Itoa(at)
		given[name] = value
		names = append(names, "$"+name)
	}
	return s.tenancy + strings.Replace(statement, "{list}", strings.Join(names, ", "), 1), given
}

func (s cacheStatements) paired(statement, key, left, right string, names, values []Value) (string, map[string]Value) {
	given := map[string]Value{"k": Text{Value: key}}
	written := make([]string, 0, len(names))
	for at := range names {
		index := strconv.Itoa(at)
		given[left+index] = names[at]
		given[right+index] = values[at]
		written = append(written, "$"+left+index+" = $"+right+index)
	}
	return s.tenancy + strings.Replace(statement, "{list}", strings.Join(written, ", "), 1), given
}

func (s cacheStatements) with(statement, key string, extra map[string]Value) (string, map[string]Value) {
	script, given := s.keyed(statement, key)
	for name, value := range extra {
		given[name] = value
	}
	return script, given
}

func texts(items []string) []Value {
	values := make([]Value, 0, len(items))
	for _, item := range items {
		values = append(values, Text{Value: item})
	}
	return values
}

func (s cacheStatements) listPush(key string, end End, values []Value) (string, map[string]Value) {
	return s.numbered("LIST PUSH "+s.space+":$k "+end.word()+" {list};", key, "v", values)
}

func (s cacheStatements) listPop(key string, end End, count *int64) (string, map[string]Value) {
	if count == nil {
		return s.keyed("LIST POP "+s.space+":$k "+end.word()+";", key)
	}
	return s.with("LIST POP "+s.space+":$k "+end.word()+" LIMIT $n;", key, map[string]Value{"n": Integer{Value: *count}})
}

func (s cacheStatements) listRange(key string, start, count int64) (string, map[string]Value) {
	return s.with("RETURN array::slice(GET "+s.space+":$k, $i, $n);", key,
		map[string]Value{"i": Integer{Value: start}, "n": Integer{Value: count}})
}

func (s cacheStatements) listAt(key string, index int64) (string, map[string]Value) {
	return s.with("RETURN array::at(GET "+s.space+":$k, $i);", key, map[string]Value{"i": Integer{Value: index}})
}

func (s cacheStatements) listLen(key string) (string, map[string]Value) {
	return s.keyed("RETURN array::len(GET "+s.space+":$k);", key)
}

func (s cacheStatements) listRemove(key string, value Value, count *int64) (string, map[string]Value) {
	if count == nil {
		return s.with("LIST REMOVE "+s.space+":$k $v;", key, map[string]Value{"v": value})
	}
	return s.with("LIST REMOVE "+s.space+":$k $v LIMIT $n;", key, map[string]Value{"v": value, "n": Integer{Value: *count}})
}

func (s cacheStatements) listTrim(key string, start, stop int64) (string, map[string]Value) {
	return s.with("LIST TRIM "+s.space+":$k FROM $i TO $j;", key,
		map[string]Value{"i": Integer{Value: start}, "j": Integer{Value: stop}})
}

func (s cacheStatements) hashSet(key string, fields []HashField) (string, map[string]Value) {
	names := make([]Value, 0, len(fields))
	values := make([]Value, 0, len(fields))
	for _, field := range fields {
		names = append(names, Text{Value: field.Name})
		values = append(values, field.Value)
	}
	return s.paired("HASH SET "+s.space+":$k {list};", key, "f", "v", names, values)
}

func (s cacheStatements) hashGet(key, field string) (string, map[string]Value) {
	return s.with("RETURN object::get(GET "+s.space+":$k, $f);", key, map[string]Value{"f": Text{Value: field}})
}

func (s cacheStatements) hashRemove(key string, fields []string) (string, map[string]Value) {
	return s.numbered("HASH REMOVE "+s.space+":$k {list};", key, "f", texts(fields))
}

func (s cacheStatements) hashIncr(key, field string, by int64) (string, map[string]Value) {
	return s.with("HASH INCR "+s.space+":$k $f BY $n;", key, map[string]Value{"f": Text{Value: field}, "n": Integer{Value: by}})
}

func (s cacheStatements) hashLen(key string) (string, map[string]Value) {
	return s.keyed("RETURN object::len(GET "+s.space+":$k);", key)
}

func (s cacheStatements) members(verb, key string, members []Value) (string, map[string]Value) {
	return s.numbered("MEMBERS "+verb+" "+s.space+":$k {list};", key, "v", members)
}

func (s cacheStatements) membersHas(key string, member Value) (string, map[string]Value) {
	return s.with("RETURN $v IN GET "+s.space+":$k;", key, map[string]Value{"v": member})
}

func (s cacheStatements) membersLen(key string) (string, map[string]Value) {
	return s.keyed("RETURN set::len(GET "+s.space+":$k);", key)
}

func (s cacheStatements) scoresAdd(key string, scores []Scored) (string, map[string]Value) {
	names := make([]Value, 0, len(scores))
	values := make([]Value, 0, len(scores))
	for _, each := range scores {
		names = append(names, Text{Value: each.Member})
		values = append(values, Float{Value: each.Score})
	}
	return s.paired("SCORES ADD "+s.space+":$k {list};", key, "m", "c", names, values)
}

func (s cacheStatements) scoresIncr(key, member string, by float64) (string, map[string]Value) {
	return s.with("SCORES INCR "+s.space+":$k $m BY $c;", key, map[string]Value{"m": Text{Value: member}, "c": Float{Value: by}})
}

func (s cacheStatements) scoresRemove(key string, members []string) (string, map[string]Value) {
	return s.numbered("SCORES REMOVE "+s.space+":$k {list};", key, "m", texts(members))
}

func (s cacheStatements) scoresOf(function, key, member string) (string, map[string]Value) {
	return s.with("RETURN scores::"+function+"(GET "+s.space+":$k, $m);", key, map[string]Value{"m": Text{Value: member}})
}

func (s cacheStatements) scoresRange(key string, start, stop int64) (string, map[string]Value) {
	return s.with("RETURN scores::range(GET "+s.space+":$k, $i, $j);", key,
		map[string]Value{"i": Integer{Value: start}, "j": Integer{Value: stop}})
}

// scoresBetween writes an absent bound NONE in its place (§2a).
func (s cacheStatements) scoresBetween(key string, low, high *float64) (string, map[string]Value) {
	extra := map[string]Value{}
	lowText, highText := "NONE", "NONE"
	if low != nil {
		lowText = "$lo"
		extra["lo"] = Float{Value: *low}
	}
	if high != nil {
		highText = "$hi"
		extra["hi"] = Float{Value: *high}
	}
	return s.with("RETURN scores::between(GET "+s.space+":$k, "+lowText+", "+highText+");", key, extra)
}
