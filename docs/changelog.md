# Changelog

## Unreleased

### Changed

- The storage engine is now the separate module
  [go-juuri](https://github.com/thevilledev/go-juuri) v0.1.0, the same tree
  that was `internal/radix`, with its own tests, fuzzers and differential test
  against go-immutable-radix. The library's behaviour is unchanged; it now has
  one dependency, which has none of its own. The `memdb_safe` and `purego`
  build tags still select the engine variant that uses no package `unsafe`.

## v0.1.0 - 2026-09-20

First version: a drop-in reimplementation of
[hashicorp/go-memdb](https://github.com/hashicorp/go-memdb) at commit `7d3fdd5`
(v1.3.5 plus header-only commits), with the same API and behaviour.

### Added

- `internal/radix`: a new persistent radix tree (ownership epochs, lazily
  created and sealed watch channels, rank-indexed size-classed nodes) replacing
  the dependency on `hashicorp/go-immutable-radix` and `hashicorp/golang-lru`.
  The main module has no dependencies.
- Compiled schema, flat database root, pooled write-transaction state.
- Allocation-free extractors for all built-in indexers, with the original
  reflective implementations kept as the fallback for every unusual input.
- Tiered `WatchSet` selects that wait on the timeout/context directly.
- `memdb_safe` / `purego` build tags for a build without `package unsafe`.
- `benchmarks/`: one benchmark suite compiled against either implementation,
  differential fuzzers against go-memdb and go-immutable-radix, an API lock,
  and `benchgate`, which enforces that no benchmark is slower than upstream.

### Added beyond go-memdb's API

- Bitmap indexes: `BitmapIndex` turns an index into a map from value to a
  persistent, Roaring-style compressed set of row ids (`internal/bitmap`,
  `internal/pvec`), queried with `Txn.Where`, `Txn.WhereWatch` and
  `Txn.AllRows` and combined with `RowSet.And`, `Or`, `AndNot` and `Len`.
- A typed API: `Table[T]`; `StringKey`, `IntKey` and `UintKey`, whose lookups
  neither box their argument nor resolve names; and the accessor-based indexers
  `StringIndex`, `StringSliceIndex`, `IntIndex`, `UintIndex` and `BoolIndex`,
  which produce the keys of their `*FieldIndex` twins without reflection.
- `All` and `AllOf`: `iter.Seq` adapters for any `ResultIterator`.

### Faster since the first cut

- Table and index names are resolved through a length-bucketed table instead of
  two map lookups.
- A read transaction is a 32-byte object (was 80); a write transaction is
  unchanged.
- Tree nodes find their children at a fixed offset instead of through a slice:
  16 bytes less per node, and the segment, the label bitmap and the value share
  the first cache line. The iterator object behind `Get` shrank from 176 to 128
  bytes.
- `WatchSet`: the helper goroutines of a large watch set call their 32-way
  select directly. On x86-64 the extra frame made a 1024-channel watch 6%
  slower than upstream's (found on a Zen 5; an M1 does not show it).
- The exported string indexer methods build their key in place instead of in a
  scratch buffer, and the extractor they share with transactions is half the
  size.
- `NewMemDB` allocates less than before (a 50-table schema: 808 allocations,
  was 860).
- The benchmark harness discards a warm-up pass per process, and
  `make bench-self` / `bench-self-gate` hold a change to "never slower than an
  older revision of this package". `BenchmarkTxn` runs against the small
  database, like `BenchmarkSnapshot`: with 100,000 rows next to it, three
  quarters of an empty transaction's time was the collector marking them.

### Fixed (relative to upstream)

See the [compatibility notes](compatibility.md) for details.

- `CompoundMultiIndex` with `AllowMissing` and three or more sub-indexers
  produced corrupted, duplicated keys through slice aliasing.
- `DeletePrefix` could fail to notify watchers inside the removed subtree.
- A failing `Insert`/`Delete` left the transaction partially applied.
- `go generate` stripped the license header from `watch_few.go`.
