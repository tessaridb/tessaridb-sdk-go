package tessaridb

import (
	"strconv"
	"strings"
	"time"
)

// cacheMostKeys is the most keys one listing may ask for (cache contract §2).
const cacheMostKeys = 1000

// cacheStatements renders the statements a cache sends (cache contract §2) in
// one place. Names are checked here, once, before anything is sent.
type cacheStatements struct {
	// Sent with every statement: a connection that reconnected has forgotten
	// any earlier USE (§1).
	tenancy string
	space   string
}

func newCacheStatements(namespace, database, space string) (cacheStatements, error) {
	for _, each := range [][2]string{{"a namespace", namespace}, {"a database", database}, {"a space", space}} {
		if err := checkName(each[0], each[1]); err != nil {
			return cacheStatements{}, err
		}
	}
	return cacheStatements{
		tenancy: "USE NAMESPACE " + namespace + "; USE DATABASE " + database + "; ",
		space:   space,
	}, nil
}

// cacheDuration is ttl as the store's duration, or false when it is not
// positive (§2) — a zero or negative one would remove the key.
func cacheDuration(ttl time.Duration) (Duration, bool) {
	if ttl <= 0 {
		return Duration{}, false
	}
	return Duration{Seconds: int64(ttl / time.Second), Nanos: uint32(ttl % time.Second)}, true
}

func (s cacheStatements) keyed(statement, key string) (string, map[string]Value) {
	return s.tenancy + statement, map[string]Value{"k": Text{Value: key}}
}

func (s cacheStatements) get(key string) (string, map[string]Value) {
	return s.keyed("GET "+s.space+":$k;", key)
}

// set renders SET with an optional condition (" IF ABSENT", " IF PRESENT",
// " IF = $e") and an optional expiry.
func (s cacheStatements) set(key string, value Value, condition string, expected Value, ttl *Duration) (string, map[string]Value) {
	expiry := ""
	if ttl != nil {
		expiry = " EXPIRE $t"
	}
	script, given := s.keyed("SET "+s.space+":$k = $v"+condition+expiry+";", key)
	given["v"] = value
	if expected != nil {
		given["e"] = expected
	}
	if ttl != nil {
		given["t"] = *ttl
	}
	return script, given
}

func (s cacheStatements) delete(key string) (string, map[string]Value) {
	return s.keyed("DELETE "+s.space+":$k RETURN BEFORE;", key)
}

func (s cacheStatements) incr(key string, by int64) (string, map[string]Value) {
	script, given := s.keyed("INCR "+s.space+":$k BY $n;", key)
	given["n"] = Integer{Value: by}
	return script, given
}

func (s cacheStatements) ttl(key string) (string, map[string]Value) {
	return s.keyed("RETURN TTL "+s.space+":$k;", key)
}

func (s cacheStatements) expire(key string, ttl Duration) (string, map[string]Value) {
	script, given := s.keyed("EXPIRE "+s.space+":$k $t;", key)
	given["t"] = ttl
	return script, given
}

func (s cacheStatements) persist(key string) (string, map[string]Value) {
	return s.keyed("PERSIST "+s.space+":$k;", key)
}

// keys renders a listing; limit is checked by the caller to lie in 1–1000, and
// is the one number written into the text.
func (s cacheStatements) keys(prefix string, after *string, limit int) (string, map[string]Value) {
	var script strings.Builder
	script.WriteString(s.tenancy + "KEYS FROM " + s.space)
	given := map[string]Value{}
	if prefix != "" {
		script.WriteString(" PREFIX $p")
		given["p"] = Text{Value: prefix}
	}
	if after != nil {
		script.WriteString(" AFTER $a")
		given["a"] = Text{Value: *after}
	}
	script.WriteString(" LIMIT " + strconv.Itoa(limit) + ";")
	return script.String(), given
}

func (s cacheStatements) lock(key, holder string, ttl Duration) (string, map[string]Value) {
	return s.held("SET "+s.space+":$k = $h IF ABSENT EXPIRE $t;", key, holder, &ttl)
}

func (s cacheStatements) extend(key, holder string, ttl Duration) (string, map[string]Value) {
	return s.held("SET "+s.space+":$k = $h IF = $h EXPIRE $t;", key, holder, &ttl)
}

// release is never a delete and never a write without an expiry (§4).
func (s cacheStatements) release(key, holder string) (string, map[string]Value) {
	return s.held("SET "+s.space+":$k = 'free' IF = $h EXPIRE 1ms;", key, holder, nil)
}

func (s cacheStatements) held(statement, key, holder string, ttl *Duration) (string, map[string]Value) {
	script, given := s.keyed(statement, key)
	given["h"] = Text{Value: holder}
	if ttl != nil {
		given["t"] = *ttl
	}
	return script, given
}

// unquoted turns a key as the wire spells it back into the string this handle
// wrote (§2): a quoted text key loses its quotes and its two escapes; any other
// kind is returned as it came.
func unquoted(spelled string) string {
	if len(spelled) < 2 || spelled[0] != '\'' || spelled[len(spelled)-1] != '\'' {
		return spelled
	}
	var out strings.Builder
	escaped := false
	for _, character := range spelled[1 : len(spelled)-1] {
		switch {
		case escaped:
			out.WriteRune(character)
			escaped = false
		case character == '\\':
			escaped = true
		default:
			out.WriteRune(character)
		}
	}
	return out.String()
}
