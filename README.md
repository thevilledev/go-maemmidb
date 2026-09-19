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

Measured against go-memdb v1.3.5 on an Apple M1 Max (Go 1.27.1), 190 benchmarks
from one shared source: **181 are faster, 9 are statistically equal, none is
slower, none allocates more or uses more memory** -- geometric mean **2.65x**.

| 100,000-row table | go-memdb | go-maemmidb | |
|---|---:|---:|---:|
| Insert + commit, then delete + commit (3 indexes) | 58.3 µs | 13.4 µs | **4.3x** |
| The same with 11 indexes | 185 µs | 73 µs | **2.5x** |
| Update + commit, keys unchanged (3 / 11 indexes) | 25.8 / 118 µs | 9.9 / 44.9 µs | **2.6x** |
| Bulk load, 3 indexes | 0.80 s | 0.24 s | **3.3x** |
| `First` by id: hit / miss | 757 / 350 ns | 512 / 131 ns | **1.5x / 2.7x** |
| Open a read transaction and `First` | 1018 ns | 510 ns | **2.0x** |
| `Last` on a non-unique index | 632 ns | 240 ns | **2.6x** |
| Iterate a 100-row group: forward / reverse | 5.2 / 10.0 µs | 2.7 / 2.6 µs | **1.9x / 3.8x** |
| Scan all 100,000 rows | 5.4 ms | 2.7 ms | **2.0x** |
| Read your own writes (10 inserts + lookups in one txn) | 138 µs | 50 µs | **2.8x** |
| `DeletePrefix` of 100 rows | 554 µs | 157 µs | **3.5x** |
| Track 1,000 changes and call `Changes()` | 12.5 ms | 3.5 ms | **3.6x** |
| Watch a row, update it, observe the notification | 27.7 µs | 11.4 µs | **2.4x** |
| Parallel readers on 8 procs, with a writer | 149 ns | 78 ns | **1.9x** |
| Upstream's own `BenchmarkWatch` (1024 channels, expired timeout) | 106 µs | 21 ns | |
| Heap bytes per row (3 / 11 indexes) | 2.4 / 6.5 kB | 1.1 / 3.0 kB | **-54%** |
| Heap objects per row (3 / 11 indexes) | 32 / 86 | 12 / 32 | **-64%** |

The machine was carrying ordinary desktop load, so read the ratios rather than
the absolute times.

Every number comes from the *same benchmark source file* built twice: once
against `github.com/hashicorp/go-memdb` (`-tags upstream`) and once against this
package, via a file of type aliases, so there is no adapter in the measured
path. Runs are interleaved (A/B/A/B) and compared with `benchstat`.
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

## Roadmap

The first goal was a faithful, faster go-memdb, and nothing else. Planned on top
of it, as opt-in additions that never touch the default paths (and are held to
the same benchmark gate):

- **Bitmap indexes and set-algebra queries.** Roaring-style compressed bitmaps
  over dense per-table row ids, with `And` / `Or` / `Not` / `Count` across
  indexes -- the queries that need a filtering scan today.
- **A typed, generic facade** (`Table[T]`, indexers built from accessor
  functions): no reflection and no interface boxing at all.
- **`iter.Seq` iterators** for range-over-func.

## Build tags

By default, struct fields of indexed objects are read at a cached offset
through `package unsafe`. Build with `-tags memdb_safe` (or `purego`) for a
pure-safe variant that uses reflection with a cached field index instead: same
behaviour, still far faster than upstream, tested in CI in both modes.

## Development

```bash
make check          # lint, headers, upstream-suite checksums, tests, -race, safe build, differential tests
make fuzz           # tree fuzzer + differential fuzzer against upstream
make bench-compare  # interleaved A/B run against upstream, then benchstat
make bench-gate     # fail unless universally faster
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
