# tessaridb-sdk-go

A client for [TessariDB](https://tessaridb.com) in Go, written from the
[protocol specification](https://github.com/tessaridb/tessaridb-protocol) and
nothing else.

> **Status: early.** Both transports are in: the value codec, the connection,
> change subscriptions and the query builder on the wire, and the object, file,
> backup and operational routes over HTTP. Each is proven against the shared
> conformance corpora and exercised against a running node — see
> [What works today](#what-works-today).

```
go get github.com/tessaridb/tessaridb-sdk-go
```

Go 1.24 or newer. Apache-2.0. **No dependencies** — the standard library covers
the protocol, and this package handles credentials, so every dependency would be
supply-chain surface inside it.

## Versions, and what actually has to match

This client's version is **its own** and never tracks the engine's. A fix here
would otherwise force an invented engine release, and an engine release would
force five invented client releases.

What has to match is the **protocol**. This release speaks **protocol 1.1**, plus the vault frame of **1.2**, which it
sends only to a node that announces minor 2 (node `0.17.0-beta` and later), and
connects to any node of protocol **major 1**, which is checked in the greeting
before anything else is sent — a differing major is refused there rather than
discovered mid-conversation, where it arrives as a decode failure that reads
like corruption. A differing *minor* is not a refusal: the peer's minor is
reported so a caller can decline to send what an older node cannot read.


## There is no registry step

Go has no package registry: a module is fetched from the path that names it. So
`github.com/tessaridb/tessaridb-sdk-go` is owned because the repository is, and
there is nothing to publish and nothing to reserve.

## What works today

|                                                    |                                               |
| -------------------------------------------------- | --------------------------------------------- |
| value codec — all seventeen types, both directions | **done**, 54/54 corpus vectors                |
| wire connection, greeting, statements, answers     | **done**, exercised against a running node    |
| change subscription                                | **done**, exercised against a running node    |
| query builder                                      | **done**, 38/38 corpus, 26 executed by a node |
| HTTP surface — objects, files, backup, health      | **done**, exercised against a running node    |
| JSON values and outcomes — §5.6, §5.7              | **done**, 58/59 values, 20/20 outcomes        |
| session token — §5.8                               | **done**, open once, `Bearer` thereafter      |
| `/watch`, `/metrics`, `POST /password`             | not yet                                       |

```go
bytes, err := tessaridb.Encode(tessaridb.Integer{Value: 42})
back, err := tessaridb.Decode(bytes) // tessaridb.Integer{Value: 42}
```

## Writing a statement

The builder covers `SELECT`, `CREATE`, `UPDATE` and `DELETE` over one collection.
Anything else you write as a script and send as one, which is always available.

```go
conn, err := tessaridb.Dial("127.0.0.1:9080", nil)

query, err := tessaridb.Select("memories").
	Field("body").
	Where(tessaridb.Compare("session", tessaridb.Eq, tessaridb.Text{Value: "abc"})).
	OrderBy("created", tessaridb.Descending).
	Limit(50).
	Render()

// SELECT body FROM memories WHERE session = $p0 ORDER BY created DESC LIMIT 50;
reply, err := conn.Execute(query.Script, query.Parameters)
```

On a cluster, two more clauses say **which node may answer** rather than what the
answer holds:

```go
query, err := tessaridb.Select("orders").
	Staleness("30s").        // no node further behind than this may answer
	AnsweredBy("LEADER").    // and it must be the node that decides writes
	Render()

// SELECT * FROM orders STALENESS 30s ANSWERED BY LEADER;
```

They are separate controls rather than one: a follower at zero lag is *level*,
not authoritative. A bound tighter than the cluster can know about itself is
refused **by the node**, and the refusal names the floor — this client checks the
shape of a span and never its value, because the floor belongs to the cluster.

**A value you pass never reaches the statement text.** Every one becomes a bound
parameter; the text carries the reference and the value travels beside it,
encoded. So a string that spells a statement is stored as a string that spells a
statement.

Names are the other half of that, and they are not values — a table or field name
is grammar, so a parameter cannot supply one and it is written into the text
directly. That is safe only because each is checked first, against a deliberately
narrow production (`[A-Za-z_][A-Za-z0-9_]*`), and a string that is not a name is
**refused rather than quoted into acceptance** — quoting would turn your mistake
into a statement that runs and means something else.

```go
_, err := tessaridb.Select("memories; DROP COLLECTION memories; --").Render()
// *tessaridb.BuilderError{Reason: "not-a-name", What: "a table", ...}
```

A refusal is returned to you rather than sent to the node, because you are here
now and the node is not. There are exactly two reasons — `not-a-name` and
`incomplete` — and the builder never invents a third.

Every refusal is captured where it happens and surfaces at `Render`; the first one
wins, because it is the one you can act on.

## Consuming a topic

A topic's consumer group (`DEFINE GROUP`, engine `0.12.0-beta` or later) hands
each message to one member and forgets it only when it is acknowledged.
`Consumer` reads under a group and calls your function once per message, in
order, until its context is done:

```go
conn, err := tessaridb.Dial("127.0.0.1:9080", nil)
consumer, err := tessaridb.NewConsumer(conn, "app", "main", "jobs", "workers")

ctx, stop := context.WithCancel(context.Background())
defer stop() // calling stop() from anywhere ends the loop

// Automatic: nil acknowledges the message, an error hands it back at once.
err = consumer.RunAuto(ctx, func(ctx context.Context, m tessaridb.Message) error {
	fmt.Println(m.Position, m.Deliveries, m.Value)
	return nil
})

// Manual: return tessaridb.Ack{}, tessaridb.Nack{Delay: 5 * time.Second}, or
// tessaridb.Leave{} for the group's deadline to hand it out again.
err = consumer.RunManual(ctx, func(ctx context.Context, m tessaridb.Message) tessaridb.Settle {
	return tessaridb.Ack{}
})
```

When the context is done the running handler finishes, its acknowledgement is
sent, and the loop returns `nil`. Both modes are **at least once**: make an
effect outside the store idempotent, keyed by the topic, the group and
`m.Position`. The group, not the connection, holds the state, and it is declared
in the store rather than by the consumer. The behaviour is the protocol
repository's `spec/consumer-v1.md`, which every client follows, and the
statements it sends are checked against all 14 cases of
`conformance/consumer-v1.json`.

## A space as a cache, a counter and a lock

A space (`DEFINE SPACE`) keeps one value per key with an optional expiry.
`Cache` makes each use one call over a connection you hold; a ttl of zero means
no expiry:

```go
cache, err := tessaridb.NewCache(conn, "app", "main", "cache")
if err != nil {
	return err
}
if err := cache.Set("session:abc", tessaridb.Text{Value: "ada"}, 30*time.Minute); err != nil {
	return err
}
page, err := cache.GetOrSet("page:/", time.Minute, func() (tessaridb.Value, error) {
	return tessaridb.Text{Value: "<html>…"}, nil
})
hits, err := cache.Incr("hits", 1)

lease, err := cache.Lock("nightly-report", 30*time.Second, "")
if err == nil && lease != nil {
	// … work, calling lease.Extend(0) before 30 s pass
	_, err = lease.Release()
}
```

Two rules the type is built around: **a plain `Set` clears an expiry the key
had** — pass the ttl on every write that must keep one — and **a lock is a
lease, not a mutex**: past its ttl another holder may take it. `Release` is an
expiring conditional write, never a delete, so a lease that lapsed cannot remove
the next holder's lock. `TTL` keeps the store's two absences apart:
`TTLExpires`, `TTLNever`, `TTLAbsent`. The statements are the protocol
repository's `spec/cache-v1.md`, which every client follows.

## A vault, and its passphrase

A vault (`DEFINE VAULT`) keeps `SECRET` fields encrypted in every copy that is not a
running, unsealed node. The passphrase goes in a frame of its own, never in a
statement, and is in no error this package returns:

```go
if _, err := conn.Unseal(storePassphrase); err != nil { // the store's key, for ten minutes
	return err
}
vault, err := tessaridb.NewVault(conn, "app", "main", "team")
if err != nil {
	return err
}
id := tessaridb.Text{Value: "github"}
err = vault.Write(id, map[string]tessaridb.Value{"password": tessaridb.Text{Value: "hunter2"}})
page, err := vault.List(nil, 100)                // ids only, never a value
secret, err := vault.Reveal(id, "password")
```

A vault declared `DEFINE VAULT team PASSPHRASE '…'` opens with its own passphrase
instead, and the store's opens nothing in it: `vault.Status()`, `vault.Unseal(…)`,
`vault.Seal()` and `vault.ChangePassphrase(…)` act on that vault alone, and
`Status().Custody` says which kind a vault is. An unseal lasts the node's period and
then closes by itself; a refusal after a run of wrong passphrases means **wait**, and
is not retried here. The statements and frames are the protocol repository's
`spec/vault-v1.md`.

## Objects, files and health

Everything the wire protocol does not serve is here, and it is a different client
because it is a different surface rather than an alternative to the first one.

```go
node := tessaridb.NewHTTPClient("127.0.0.1:8000", &tessaridb.Credentials{
	User:     "app",
	Password: os.Getenv("TESSARIDB_PASSWORD"),
})

err := node.Put("acme", "app", "uploads", "reports/100% done.pdf", content)
back, err := node.Get("acme", "app", "uploads", "reports/100% done.pdf")
listing, err := node.List("acme", "app", "uploads")
condition, err := node.Health()
```

**The password is spent once.** A node verifies Basic with Argon2id at the OWASP
floor, and HTTP has no connection to hang a session on, so that cost is paid on
_every_ request that carries one. This client opens a session on its first
authenticated call and presents the token after — and when a token stops working,
which it does four different ways that all answer `401`, it signs in again and
retries once, without the caller seeing it.

A client that skipped this would be correct, would pass every test, and would be
slower than the protocol intends by more than an order of magnitude. Measured
here against a release build over loopback, on a statement that does nothing:
**15.8 ms per request with a password against 0.24 ms with a token**, 65×.

**`node.Script()` takes no parameters, and that is deliberate.** A parameter on
this route is a JSON string carrying _TessariQL source_, not a value —
`{"x":"3"}` is the number 3 and `{"x":"hello"}` is a `400`. Passing a caller's
string through would be a type-confusion hazard that no test written against it
would show, so this client does not build the bridge: a statement with a value in
it goes over the wire, where a parameter is an encoded value and none of this
arises.

**A batch of events goes to a series in one transaction** (node `0.14.0-beta`,
§5.9). `Append` takes `Object` values, renders them as TessariQL source — the one
place this client does, because the route reads nothing else — and answers how
many landed:

```go
landed, err := node.Append("acme", "metrics", "readings", []tessaridb.Value{
	tessaridb.Object{Fields: map[string]tessaridb.Value{
		"sensor": tessaridb.Text{Value: "s1"},
		"at":     tessaridb.Datetime{Seconds: 1_790_676_000},
	}},
})
```

The batch lands whole or not at all, and it is sent **once**: it is not idempotent,
so a transport failure after the request left is the caller's to judge. A kind an
event cannot carry — bytes, a range, a non-finite float — returns a
`*NotAnEventError` before anything is sent.

**A `404` is an answer.** A file that is not there reads as a `nil` slice with no
error, and a file that exists and is empty reads as zero bytes — these are
different facts and the server draws the line, so this client does not erase it.
A listing that comes back `nil` means the name is not a bucket; an empty listing
means the bucket is there and holds nothing.

`HEAD` is deliberately not offered rather than pending. The node reads the whole
object and discards the body, so it costs the server exactly what a `GET` costs;
presenting it as a cheap `exists()` would be an invitation to call it in a loop.

## A value read over HTTP needs its kind

JSON has six types and the store has seventeen, so §5.7 is a decision rather than
a translation: for most of the table the type is **not recoverable from the JSON
alone**. `"12.34"` is a decimal or a string, `"1h30m"` is a duration or a string,
and `users:7` is the integer 7 or the text `'7'`.

So the reader is told, and a caller reads the kind from the field's declaration
in the catalog:

```go
results, err := node.Script("USE NAMESPACE acme; USE DATABASE app; RETURN 1;")
outcome, err := tessaridb.ReadOutcome(results[2], tessaridb.Reading{
	Value: tessaridb.IntegerKind{},
})
```

A reader that guessed instead would be right most of the time, which is worse
than being wrong all of it. If you need types without carrying a catalog, use the
wire protocol, where every value carries its tag.

One spelling stays lossy even with the kind supplied, and this client says so
rather than papering over it: a float `-0.0` is written `0`, because the value is
normalised before it is written. No reader can tell it from `+0.0`.

**A record identity is a string here and is not parsed back.** `users:7` is the
integer 7 and the text `'7'` written identically, and the conformance corpus
carries one `keys` outcome holding `"1"` beside `"ada"` — an integer identity and
a text one in a single array, which no declared kind could cover. It is an
identifier to display, log and pass back; a caller that needs its type reads the
identity off the wire, where it carries its tag.

## There is no TLS on either transport

Credentials travel as given, and so does the session token — it is a bearer
credential in the literal sense. Run this on a protected network, or behind
something that terminates TLS. This is a property of the protocol, not an
omission in the client, and it is stated here rather than left to be discovered.

## Values

The store's model has seventeen types, and `Value` is a sealed interface with one
concrete type per kind rather than a struct carrying every field — a struct would
let you read a `Mantissa` off a string and get a zero, and the compiler would
never mention it.

Two distinctions are easy to lose in Go specifically, and neither is spelled
`nil` here. **`None` and `Null` are different types** — the field is not present,
versus the field is present and holds nothing — and a `nil` `Value` would be
indistinguishable from an absent one, with both printing as `<nil>`.

**A `Duration` is not a `time.Duration`.** That type is an `int64` count of
nanoseconds and tops out near 292 years; the protocol carries whole seconds plus
nanoseconds and reaches as far as an `int64` of seconds. Converting at the
boundary would silently truncate exactly the values you could not have produced
by accident.

Coordinates are **longitude first**, as RFC 7946 fixes, and travel as bits rather
than text. The opposite order is the most common bug in geospatial code precisely
because it is silent: a point in Paris becomes a point in the Indian Ocean, which
is a perfectly valid place.

## Conformance

The codec is checked against the corpus in the protocol repository, which is
generated by a **second implementation written from the specification alone**.
That matters more than it sounds: a codec that is wrong in the same way on both
sides round-trips perfectly, so a suite written alongside this codec cannot catch
what the corpus catches. Both directions are run — encode to exactly the stated
bytes, and decode to exactly the stated value.

The comparison used by those tests compares floats **by their bits**, because
`reflect.DeepEqual` says `NaN != NaN` and says `-0 == 0`, and the protocol
disagrees with it on both.

The query corpus is the same idea applied to text: the rendering must be
byte-identical and the parameter numbering must match, so that the same query
built in any client language is the same statement. Cases the contract says a
builder must refuse are asserted as refusals, with the stated reason, and are
never rendered.

```
go test ./...    # expects ../tessaridb-protocol checked out beside this repo,
                 # or TESSARI_PROTOCOL_CONFORMANCE pointing at the corpus
```

A missing corpus fails loudly rather than skipping: a suite that passes having
found nothing reports coverage it does not have.

Both of those establish only that two implementations of a written document
agree. Neither reaches the node's parser — no client may link it — so the suite
additionally **executes every rendered case against a running node**, which is the
only check that does:

```
TESSARIDB_TEST_NODE=127.0.0.1:47915 \
TESSARIDB_TEST_HTTP=127.0.0.1:47916 go test ./...
```

Those tests are opt-in and skip loudly when the variables are unset; a suite that
needs a server cannot be the suite that runs on a clean checkout. The session
tests additionally need a store with a user declared, because that is the only
thing that makes a token exist at all.

The JSON corpus is decode-only, and it says why: a client never encodes a value
on this surface, since a `/script` parameter carries TessariQL source rather than
JSON. So it is weaker than the value corpus by construction — there is no writer
for a wrong reader to agree with — and what it does catch is every place the JSON
is lossy and the declared kind is what restores the value.

## Licence

Apache-2.0. The engine is licensed separately; the two are distinct decisions.
