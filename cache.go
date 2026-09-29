package tessaridb

// A space used as a cache, a counter and a lock — cache contract 1.0
// (spec/cache-v1.md in the protocol repository).
//
// A Cache uses a connection the caller holds and sends one statement per call,
// with its own USE, so a connection that reconnected underneath it cannot read
// another database. Every key, value, duration and holder is bound.
//
// Two things a cache over this store must know, and that this type makes hard
// to get wrong: a plain Set clears an expiry the key had — pass the ttl again on
// every write that must keep one — and a lock is a lease, not a mutex: past its
// ttl another holder may take it and neither is told. Lease.Release is an
// expiring conditional write, never a delete.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// CacheArgumentError is an argument the cache contract refuses before sending
// (§2): a ttl that is not positive, a listing out of range, an empty holder.
type CacheArgumentError struct{ Reason string }

func (e *CacheArgumentError) Error() string { return "tessaridb: not a cache call: " + e.Reason }

// TTLKind says which of a key's three states TTL found.
type TTLKind int

const (
	// TTLExpires: the key expires after TTL.Left.
	TTLExpires TTLKind = iota
	// TTLNever: the key is there and never expires.
	TTLNever
	// TTLAbsent: there is no such key.
	TTLAbsent
)

// TTL is how long a key has left — the store's two absences kept apart.
type TTL struct {
	Kind TTLKind
	Left time.Duration
}

// Cache is the space Space in Namespace/Database, over a connection the caller
// holds.
type Cache struct {
	conn       *Conn
	statements cacheStatements
}

// NewCache checks the three names, refusing one that is not a name with a
// *BuilderError before anything is sent.
func NewCache(conn *Conn, namespace, database, space string) (*Cache, error) {
	statements, err := newCacheStatements(namespace, database, space)
	if err != nil {
		return nil, err
	}
	return &Cache{conn: conn, statements: statements}, nil
}

// optional turns a ttl of zero into no expiry and refuses a negative one.
func optional(ttl time.Duration) (*Duration, error) {
	if ttl == 0 {
		return nil, nil
	}
	lasting, ok := cacheDuration(ttl)
	if !ok {
		return nil, &CacheArgumentError{Reason: "a ttl must be positive, or zero for none"}
	}
	return &lasting, nil
}

func required(ttl time.Duration) (Duration, error) {
	lasting, ok := cacheDuration(ttl)
	if !ok {
		return Duration{}, &CacheArgumentError{Reason: "a ttl must be positive: a zero or negative one would remove the key"}
	}
	return lasting, nil
}

// Get answers the value under key, and false when there is no such key.
func (c *Cache) Get(key string) (Value, bool, error) {
	found, err := c.value(c.statements.get(key))
	if err != nil {
		return nil, false, err
	}
	if _, absent := found.(None); absent {
		return nil, false, nil
	}
	return found, true, nil
}

// Set stores value, expiring after ttl when ttl is not zero — and clearing any
// expiry the key had when it is.
func (c *Cache) Set(key string, value Value, ttl time.Duration) error {
	lasting, err := optional(ttl)
	if err != nil {
		return err
	}
	_, err = c.value(c.statements.set(key, value, "", nil, lasting))
	return err
}

// SetIfAbsent stores value only if there is no key, and says whether it did.
func (c *Cache) SetIfAbsent(key string, value Value, ttl time.Duration) (bool, error) {
	return c.conditional(key, value, " IF ABSENT", nil, ttl)
}

// SetIfPresent stores value only if there is a key, and says whether it did.
func (c *Cache) SetIfPresent(key string, value Value, ttl time.Duration) (bool, error) {
	return c.conditional(key, value, " IF PRESENT", nil, ttl)
}

// CompareAndSet stores value only if key holds expected, and says whether it did.
func (c *Cache) CompareAndSet(key string, expected, value Value, ttl time.Duration) (bool, error) {
	return c.conditional(key, value, " IF = $e", expected, ttl)
}

func (c *Cache) conditional(key string, value Value, condition string, expected Value, ttl time.Duration) (bool, error) {
	lasting, err := optional(ttl)
	if err != nil {
		return false, err
	}
	return c.flag(c.statements.set(key, value, condition, expected, lasting))
}

// Delete removes key and says whether there was one. A key holding NULL is one.
func (c *Cache) Delete(key string) (bool, error) {
	found, err := c.value(c.statements.delete(key))
	if err != nil {
		return false, err
	}
	_, absent := found.(None)
	return !absent, nil
}

// Incr adds by to the integer under key — a missing key counts from zero — and
// answers the new value. An expiry the key had is kept.
func (c *Cache) Incr(key string, by int64) (int64, error) {
	found, err := c.value(c.statements.incr(key, by))
	if err != nil {
		return 0, err
	}
	held, ok := found.(Integer)
	if !ok {
		return 0, fmt.Errorf("tessaridb: an increment answered %T", found)
	}
	return held.Value, nil
}

// TTL answers how long key has left.
func (c *Cache) TTL(key string) (TTL, error) {
	found, err := c.value(c.statements.ttl(key))
	if err != nil {
		return TTL{}, err
	}
	switch held := found.(type) {
	case None:
		return TTL{Kind: TTLAbsent}, nil
	case Null:
		return TTL{Kind: TTLNever}, nil
	case Duration:
		return TTL{Kind: TTLExpires, Left: time.Duration(held.Seconds)*time.Second + time.Duration(held.Nanos)}, nil
	}
	return TTL{}, fmt.Errorf("tessaridb: a ttl answered %T", found)
}

// Expire lets key expire after ttl, and says whether there was a key.
func (c *Cache) Expire(key string, ttl time.Duration) (bool, error) {
	lasting, err := required(ttl)
	if err != nil {
		return false, err
	}
	return c.flag(c.statements.expire(key, lasting))
}

// Persist makes key never expire, and says whether there was a key.
func (c *Cache) Persist(key string) (bool, error) {
	return c.flag(c.statements.persist(key))
}

// Keys answers up to limit keys (1–1000) in key order, starting with prefix
// (empty: every key) and after *after when after is not nil.
func (c *Cache) Keys(prefix string, after *string, limit int) ([]string, error) {
	if limit < 1 || limit > cacheMostKeys {
		return nil, &CacheArgumentError{Reason: "a key listing asks for 1 to 1000 keys"}
	}
	reply, err := c.execute(c.statements.keys(prefix, after, limit))
	if err != nil {
		return nil, err
	}
	answered, ok := last(reply).(Keys)
	if !ok {
		return nil, fmt.Errorf("tessaridb: a key listing answered %T", last(reply))
	}
	keys := make([]string, 0, len(answered.Keys))
	for _, key := range answered.Keys {
		keys = append(keys, unquoted(key))
	}
	return keys, nil
}

// GetOrSet answers the value under key, or — when there is none — what loader
// makes, stored for ttl if nobody stored first (§3). Racing callers are not
// coordinated: each that misses runs its loader, the first to store wins, and
// the others answer the winner's value.
func (c *Cache) GetOrSet(key string, ttl time.Duration, loader func() (Value, error)) (Value, error) {
	if _, err := required(ttl); err != nil {
		return nil, err
	}
	found, ok, err := c.Get(key)
	if err != nil || ok {
		return found, err
	}
	made, err := loader()
	if err != nil {
		return nil, err
	}
	stored, err := c.SetIfAbsent(key, made, ttl)
	if err != nil || stored {
		return made, err
	}
	// Somebody stored first — or stored and it has already expired.
	again, ok, err := c.Get(key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return made, nil
	}
	return again, nil
}

// Lease is a lock held by this caller until its ttl passes (§4).
type Lease struct {
	Key    string
	Holder string
	TTL    time.Duration
	cache  *Cache
}

// Lock takes the lock key for ttl as holder (a fresh unique one when empty), and
// answers the lease, or nil when somebody else holds it.
func (c *Cache) Lock(key string, ttl time.Duration, holder string) (*Lease, error) {
	lasting, err := required(ttl)
	if err != nil {
		return nil, err
	}
	if holder == "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		holder = hex.EncodeToString(raw)
	}
	taken, err := c.flag(c.statements.lock(key, holder, lasting))
	if err != nil || !taken {
		return nil, err
	}
	return &Lease{Key: key, Holder: holder, TTL: ttl, cache: c}, nil
}

// LeaseOf is a lease for key held as holder, for a caller that stored the holder
// and must extend or release after a restart.
func (c *Cache) LeaseOf(key, holder string, ttl time.Duration) *Lease {
	return &Lease{Key: key, Holder: holder, TTL: ttl, cache: c}
}

// Extend holds the lease for another ttl (its own when ttl is zero); false means
// it was already lost and the work it guarded must stop.
func (l *Lease) Extend(ttl time.Duration) (bool, error) {
	if ttl == 0 {
		ttl = l.TTL
	}
	lasting, err := required(ttl)
	if err != nil {
		return false, err
	}
	return l.cache.flag(l.cache.statements.extend(l.Key, l.Holder, lasting))
}

// Release gives the lease back, and says whether it was still held.
func (l *Lease) Release() (bool, error) {
	return l.cache.flag(l.cache.statements.release(l.Key, l.Holder))
}

func (c *Cache) execute(script string, parameters map[string]Value) (*Reply, error) {
	reply, err := c.conn.Execute(script, parameters)
	if err != nil {
		return nil, err
	}
	if reply.Redirect != nil {
		return nil, fmt.Errorf("tessaridb: a cache statement was answered by a redirect to %s", reply.Redirect.Endpoint)
	}
	return reply, nil
}

// value answers the last outcome's value; a statement that answers none (a
// plain SET) reads as None.
func (c *Cache) value(script string, parameters map[string]Value) (Value, error) {
	reply, err := c.execute(script, parameters)
	if err != nil {
		return nil, err
	}
	switch answered := last(reply).(type) {
	case ValueOutcome:
		return answered.Value, nil
	case Done:
		return None{}, nil
	}
	return nil, fmt.Errorf("tessaridb: a cache statement answered %T", last(reply))
}

func (c *Cache) flag(script string, parameters map[string]Value) (bool, error) {
	found, err := c.value(script, parameters)
	if err != nil {
		return false, err
	}
	held, ok := found.(Bool)
	if !ok {
		return false, fmt.Errorf("tessaridb: a conditional write answered %T", found)
	}
	return held.Value, nil
}
