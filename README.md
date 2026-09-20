# go-maemmidb

A drop-in replacement for [hashicorp/go-memdb](https://github.com/hashicorp/go-memdb)
that is faster on every operation we could think of measuring -- and a benchmark
suite, compiled against both implementations from one set of sources, that
fails the build if that ever stops being true.

```go
import memdb "github.com/thevilledev/go-maemmidb"
```

Same package name, same API, same behaviour: an in-memory database with
[MVCC](https://en.wikipedia.org/wiki/Multiversion_concurrency_control),
atomic transactions across tables, rich indexing, and watch channels, built on
immutable radix trees. No dependencies.

> go-maemmidb is an independent project. It is not affiliated with, sponsored
> by, or endorsed by HashiCorp or IBM. It is derived from go-memdb under the
> terms of the Mozilla Public License 2.0; see [Credits and license](#credits-and-license).

## Performance

Measured against go-memdb v1.3.5 with 190 benchmarks from one shared source, on
two machines. On an Apple M1 Max (Go 1.27.1): **186 are faster, 4 are
statistically equal, none is slower, none allocates more or uses more memory**
-- geometric mean **2.74x**. On an AMD Ryzen AI 9 HX PRO 370 (Zen 5, Linux):
186 faster, 4 equal, none slower -- geometric mean **2.94x**.

| 100,000-row table, M1 Max | go-memdb | go-maemmidb | |
|---|---:|---:|---:|
| Insert + commit, then delete + commit (3 indexes) | 45.2 µs | 13.1 µs | **3.4x** |
| The same with 11 indexes | 152 µs | 64 µs | **2.4x** |
| Update + commit, keys unchanged (3 indexes) | 29.1 µs | 8.3 µs | **3.5x** |
| Bulk load, 3 indexes | 0.79 s | 0.22 s | **3.6x** |
| `First` by id: hit / miss | 683 / 398 ns | 398 / 127 ns | **1.7x / 3.1x** |
| Open a read transaction and `First` | 939 ns | 396 ns | **2.4x** |
| Open and close a read transaction | 64.6 ns | 24.4 ns | **2.6x** |
| `Last` on a non-unique index | 624 ns | 197 ns | **3.2x** |
| Iterate a 100-row group: forward / reverse | 5.1 / 9.6 µs | 2.2 / 2.1 µs | **2.3x / 4.5x** |
| Scan all 100,000 rows | 4.7 ms | 2.5 ms | **1.9x** |
| Read your own writes (10 inserts + lookups in one txn) | 156 µs | 44 µs | **3.6x** |
| `DeletePrefix` of 100 rows | 544 µs | 155 µs | **3.5x** |
| Track 1,000 changes and call `Changes()` | 11.7 ms | 4.1 ms | **2.9x** |
| Watch a row, update it, observe the notification | 31.2 µs | 8.7 µs | **3.6x** |
| Parallel readers on 8 procs, with a writer | 154 ns | 68 ns | **2.3x** |
| Upstream's own `BenchmarkWatch` (1024 channels, expired timeout) | 94 µs | 21 ns | |
| Heap bytes per row (3 / 11 indexes) | 2.4 / 6.5 kB | 1.0 / 2.7 kB | **-59%** |
| Heap objects per row (3 / 11 indexes) | 32 / 86 | 12 / 32 | **-64%** |

The M1 was carrying ordinary desktop load, so read the ratios rather than the
absolute times.

Every number comes from the *same benchmark source file* built twice: once
against `github.com/hashicorp/go-memdb` (`-tags upstream`) and once against this
package, via a file of type aliases, so there is no adapter in the measured
path. Runs are interleaved (A/B/A/B), every round with differently laid out
binaries, and compared with `benchstat`.
`make bench-gate` then enforces the claim: it fails if **any** benchmark is
slower (statistically significant and beyond a 2% noise tolerance), allocates
more often, or uses more memory than upstream. Method, environment and the full
tables are in [BENCHMARKS.md](BENCHMARKS.md).

Where the speed comes from, in one paragraph: go-immutable-radix allocates a
channel for every node and leaf it writes, tracks writable nodes in an LRU, and
finds children by binary search with a closure; go-memdb on top of it resolves
struct fields by name through reflection on every call and builds every index
key through string concatenation. Here, watch channels are created lazily by
whoever watches and sealed when their node is replaced; node ownership is an
integer comparison; children are found with a bitmap and a population count
(the rank trick of Roaring bitmaps) in nodes that carry their child array
inline; field offsets are resolved once; keys are appended to a reused buffer.
[DESIGN.md](DESIGN.md) has the details, including why Roaring bitmaps proper do
*not* belong on the ordered hot path and where they will go instead.

## Compatibility

- The complete upstream test suite runs here **unmodified**: the eight
  `*_test.go` files are byte-identical copies, checked against their upstream
  SHA-256 by `make verify-upstream-tests`.
- An API lock compiled against both packages pins every exported signature and
  struct shape.
- Differential fuzzers drive both implementations (and both radix trees) with
  the same random transactions and demand identical results, ordering, error
  messages, `Changes()`, snapshot isolation and **fired watch channels**.

The handful of deliberate differences -- two upstream defects that are fixed
rather than reproduced, and a few places where upstream's behaviour is random --
are listed in [COMPATIBILITY.md](COMPATIBILITY.md).

## Migrating from go-memdb

The migration is the import path, and nothing else. The package is still named
`memdb`, so every call site -- `memdb.NewMemDB`, `memdb.StringFieldIndex`,
`*memdb.Txn` -- keeps its spelling, and even an unaliased import still binds to
the name `memdb`:

```bash
go get github.com/thevilledev/go-maemmidb@v0.1.0
grep -rl 'github.com/hashicorp/go-memdb' --include='*.go' . \
  | xargs sed -i '' 's|"github.com/hashicorp/go-memdb"|"github.com/thevilledev/go-maemmidb"|g'
go mod tidy && go build ./... && go test ./...
```

## Example

Unchanged from go-memdb:

```go
// Create a sample struct
type Person struct {
	Email string
	Name  string
	Age   int
}

// Create the DB schema
schema := &memdb.DBSchema{
	Tables: map[string]*memdb.TableSchema{
		"person": {
			Name: "person",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "Email"},
				},
				"age": {
					Name:    "age",
					Unique:  false,
					Indexer: &memdb.IntFieldIndex{Field: "Age"},
				},
			},
		},
	},
}

// Create a new data base
db, err := memdb.NewMemDB(schema)
if err != nil {
	panic(err)
}

// Create a write transaction
txn := db.Txn(true)

// Insert some people
people := []*Person{
	{"joe@aol.com", "Joe", 30},
	{"lucy@aol.com", "Lucy", 35},
	{"tariq@aol.com", "Tariq", 21},
	{"dorothy@aol.com", "Dorothy", 53},
}
for _, p := range people {
	if err := txn.Insert("person", p); err != nil {
		panic(err)
	}
}

// Commit the transaction
txn.Commit()

// Create read-only transaction
txn = db.Txn(false)
defer txn.Abort()

// Lookup by email
raw, err := txn.First("person", "id", "joe@aol.com")
if err != nil {
	panic(err)
}
fmt.Printf("Hello %s!\n", raw.(*Person).Name)

// Range scan over people with ages between 25 and 35 inclusive
it, err := txn.LowerBound("person", "age", 25)
if err != nil {
	panic(err)
}
for obj := it.Next(); obj != nil; obj = it.Next() {
	p := obj.(*Person)
	if p.Age > 35 {
		break
	}
	fmt.Printf("  %s is aged %d\n", p.Name, p.Age)
}
```

## Beyond go-memdb

Everything above is go-memdb's API. Three additions go further; none of them
changes what the rest does, and a database that does not use them pays nothing
for them. The benchmark gate holds them to that.

### Bitmap indexes and set queries

Wrap an indexer in `BitmapIndex` and the index maps each value to the *set* of
rows that have it -- a persistent, Roaring-style compressed bitmap over row
ids -- instead of to an ordered list of rows:

```go
"status": {Name: "status", Indexer: &memdb.BitmapIndex{Indexer: &memdb.StringFieldIndex{Field: "Status"}}},
"node":   {Name: "node",   Indexer: &memdb.BitmapIndex{Indexer: &memdb.StringFieldIndex{Field: "NodeID"}}},
"tags":   {Name: "tags",   Indexer: &memdb.BitmapIndex{Indexer: &memdb.StringSliceFieldIndex{Field: "Tags"}}},
```

```go
running, err := txn.Where("alloc", "status", "running")
onNode, _ := txn.Where("alloc", "node", nodeID)
batch, _ := txn.Where("alloc", "tags", "batch")

n := running.And(onNode).Len()                 // a count, without visiting a row
for obj := range running.And(onNode).AndNot(batch).All() {
	...
}

all, _ := txn.AllRows("alloc")
notRunning := all.AndNot(running)              // a complement; all.Len() is the table size
```

Sets are immutable values as transactional as everything else (snapshots,
aborts, read-your-writes), and `WhereWatch` fires when a row enters or leaves a
set. It suits columns with few distinct values relative to the number of rows:
states, types, flags, owners, tags. Measured on a 100,000-row table against the
same columns as ordinary indexes ([BENCHMARKS.md](BENCHMARKS.md#extensions)):

| | ordinary indexes | bitmap indexes |
|---|---:|---:|
| Count rows matching three columns | 214 µs (index walk + filter) | **18 µs** |
| Count rows with one value | 242 µs | **39 ns** |
| Visit the 10,000 rows matching two columns | 657 µs | **237 µs** |
| Replace a row, indexed values unchanged | 11.6 µs | **4.5 µs** |
| Insert a row, then delete it | 24.6 µs | 21.7 µs |
| Heap per row (id + three such indexes) | 1213 B | **453 B** |

What it gives up: results come in row id order (roughly insertion order), not
index order; there are no range scans, and `First`/`Get` refuse such an index.

### A typed API

```go
var people = memdb.NewTable[Person]("person")
var personByEmail = people.StringKey("id")

err := people.Insert(txn, &Person{...})
p, err := personByEmail.First(txn, "joe@aol.com") // p is a *Person
rows, err := people.Get(txn, "age", 30)
for p := range rows.All() { ... }
```

- `StringKey`, `IntKey` and `UintKey` take the key as a Go value. The untyped
  API has to box its arguments into interfaces -- an allocation per query for
  anything but small integers -- and resolve two names; a typed key does
  neither: a point lookup on a small table takes 51 ns instead of 92 ns (157
  instead of 229 ns on 100,000 rows) and does not allocate.
- `StringIndex[T]`, `IntIndex[T, N]`, `UintIndex[T, N]`, `BoolIndex[T]` and
  `StringSliceIndex[T]` are indexers built from accessor functions
  (`Get: func(p *Person) string { return p.Email }`). They go where the
  `*FieldIndex` types go, produce exactly the same keys, and use no reflection
  anywhere.
- `Table[T]` inserts `*T` and returns `*T`. Typed and untyped calls mix freely.

### Range-over-func

`memdb.All(it)` and `memdb.AllOf[*Person](it)` turn any `ResultIterator` into
an `iter.Seq`.

## Build tags

By default, struct fields of indexed objects are read at a cached offset, and
a tree node finds its children at a fixed offset behind its header, both
through `package unsafe`. Build with `-tags memdb_safe` (or `purego`) for a
pure-safe variant that uses reflection with a cached field index and an
ordinary slice instead: same behaviour, still far faster than upstream, tested
in CI in both modes.

## Development

```bash
make check          # lint, headers, upstream-suite checksums, tests, -race, safe build, differential tests
make fuzz           # tree fuzzer + differential fuzzer against upstream
make bench-compare  # interleaved A/B run against upstream, then benchstat
make bench-gate     # fail unless universally faster
make bench-self BASE=origin/main && make bench-self-gate
                    # the same, against an older revision of this package
```

The `benchmarks/` directory is a separate Go module, so that the original
implementations are never a dependency of the main module.

## Credits and license

go-maemmidb is a derivative work of
[go-memdb](https://github.com/hashicorp/go-memdb), Copyright (c) 2015 HashiCorp,
Inc. / Copyright IBM Corp. 2015, 2026. Its public API, documentation, semantics
and test suite are go-memdb's; the design of its storage engine follows the
semantics of [go-immutable-radix](https://github.com/hashicorp/go-immutable-radix).
All credit for the design of the database -- the table/index model, the
indexers, the watch mechanism -- belongs to the go-memdb authors. See
[NOTICE](NOTICE) for which files are verbatim, which are modified, and which are
new.

This Source Code Form is subject to the terms of the Mozilla Public License,
v. 2.0. If a copy of the MPL was not distributed with this file, You can obtain
one at <http://mozilla.org/MPL/2.0/>. The full text is in [LICENSE](LICENSE).
