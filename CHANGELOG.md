# Changelog

## Unreleased

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

### Fixed (relative to upstream; see COMPATIBILITY.md)

- `CompoundMultiIndex` with `AllowMissing` and three or more sub-indexers
  produced corrupted, duplicated keys through slice aliasing.
- `DeletePrefix` could fail to notify watchers inside the removed subtree.
- A failing `Insert`/`Delete` left the transaction partially applied.
- `go generate` stripped the license header from `watch_few.go`.
