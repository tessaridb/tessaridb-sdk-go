# tessaridb-sdk-go

A client for [TessariDB](https://tessaridb.com) in Go, written from the
[protocol specification](https://github.com/tessaridb/tessaridb-protocol) and
nothing else.

> **Status: early.** The wire half is in — the value codec, the connection, change
> subscriptions and the query builder — each proven against the shared conformance
> corpora and exercised against a running node. The HTTP surface is not written
> yet, so objects, files, backup and the operational routes are not available from
> this client. See [What works today](#what-works-today).

```
go get github.com/tessaridb/tessaridb-sdk-go
```

Go 1.24 or newer. Apache-2.0. **No dependencies** — the standard library covers
the protocol, and this package handles credentials, so every dependency would be
supply-chain surface inside it.

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
| query builder                                      | **done**, 30/30 corpus, 21 executed by a node |
| HTTP surface — objects, files, backup, health      | not yet                                       |
| session token — §5.8                               | not yet, and it belongs with the HTTP surface |

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
TESSARIDB_TEST_NODE=127.0.0.1:47915 go test ./...
```

Those tests are opt-in and skip loudly when the variable is unset; a suite that
needs a server cannot be the suite that runs on a clean checkout.

## Licence

Apache-2.0. The engine is licensed separately; the two are distinct decisions.
