# Compatibility with hashicorp/go-memdb

go-maemmidb is a drop-in replacement for
[`github.com/hashicorp/go-memdb`](https://github.com/hashicorp/go-memdb)
(baseline: commit `7d3fdd5`, i.e. v1.3.5 plus header-only commits). Switching is
an import-path change:

```go
import memdb "github.com/thevilledev/go-maemmidb"
```

The package name, every exported identifier, every signature, the field order of
every exported struct (upstream's tests and much user code use unkeyed
literals), the index key encodings, result ordering, error messages, isolation
semantics and watch semantics are upstream's.

## How compatibility is established

| Evidence | What it proves |
|---|---|
| **Upstream's complete test suite, byte for byte.** The eight `*_test.go` files (5,248 lines, 71 tests) plus `schema.go`, `filter.go` and `changes.go` are verbatim copies; `make verify-upstream-tests` checks their SHA-256 against upstream. | Everything upstream asserts about itself holds here. |
| **API lock.** `benchmarks/apilock.go` pins the full signature of every exported method and the shape of every exported struct, and is compiled against both implementations. | The two APIs are identical, mechanically. |
| **Differential fuzzing of the database** (`benchmarks/differential`). Random transactions -- inserts, updates, deletes, `DeleteAll`, `DeletePrefix`, every query kind with valid and invalid arguments, iterators held across writes, `Txn.Snapshot`, writable `MemDB.Snapshot`s, aborts, `TrackChanges` -- run against both implementations with the same object pointers. | Identical results and result order, identical error strings, identical `Changes()`, identical snapshot isolation, and identical **sets of fired watch channels** after every commit. |
| **Differential fuzzing of the storage engine** (`benchmarks/radixdiff`) against `hashicorp/go-immutable-radix` v1.3.1, including transactions large enough to push upstream past its 8192-entry tracking cap into its slow-notify path. | `internal/radix` reproduces iteration order, prefix/lower-bound/reverse seeks, longest-prefix matching and watch granularity exactly. |
| **Extractor equivalence tests** (`extract_test.go`). | The allocation-free fast paths agree with the exported indexer methods on every input they accept, and decline everything else. |

## Upstream behaviour that is kept on purpose

These look like bugs, but programs may depend on them, so they are reproduced
and covered by the differential tests:

- A trailing `_prefix` on an index name is always stripped first and turns the
  query into a prefix scan. An index literally named `x_prefix` is therefore
  only reachable as `x_prefix_prefix`. The no-argument fast path comes before
  the prefix-support check, so `Get(table, "age_prefix")` works even though
  `IntFieldIndex` has no prefix support.
- `Get` always performs a prefix seek, even for an exact key on a unique index.
  Only `First`/`Last` use an exact lookup, and only when the index is unique,
  arguments were given and the name carries no `_prefix`.
- `LowerBound` / `ReverseLowerBound` return a nil watch channel.
- `DeletePrefix` cuts the index subtree at the **raw** prefix string rather
  than at the key the indexer derives from it, checks its arguments in
  upstream's order, and panics with upstream's message if the cut removed
  nothing.
- `DeleteAll` returns `(n, ErrNotFound)` when an iterator yields the same
  object twice (a `_prefix` scan over a multi-value index).
- Integer index keys are as wide as the Go type of the *argument*, so querying
  an `int` field with an `int32` argument finds nothing.
- `StringMapFieldIndex` accepts one argument but never matches with it.
- A unique *secondary* index does not reject duplicates: last write wins.
- `StringFieldIndex` on a non-string field indexes reflect's `"<int Value>"`;
  `UUIDFieldIndex.FromObject` returns `ok == true` together with its error.
- On update, old and new index keys are compared position by position (not as
  sets), which decides which keys are deleted and re-inserted and therefore
  which watchers fire.
- `Changes()` returns the transaction's internal slice when there is nothing to
  collapse, caches the collapsed list, reports an insert-then-delete as an
  empty non-nil list, and returns nil after `Abort`.
- `Watch` returns `true` for a *timeout*; `FilterFunc` returning `true` *drops*
  the row.
- Using a write transaction after `Commit` or `Abort` panics.

## Deliberate differences

1. **`CompoundMultiIndex` no longer corrupts keys.** With `AllowMissing` and
   three or more sub-indexers, upstream builds the intermediate "prefix" keys
   with `append` on a slice that siblings share, so a later sibling overwrites
   a prefix that was already emitted (`index.go`, `walkVals`). With fields
   `A="a"`, `B=["b1","b2","b3"]`, `C=["c1","c2"]` upstream returns the key
   `a\0b3\0` three times and never `a\0b1\0` or `a\0b2\0`. Which bytes get
   clobbered depends on allocator size classes, so the corruption cannot even
   be reproduced faithfully. Here every key is intact, in upstream's
   depth-first order (`TestCompoundMultiIndexPrefixesAreIntact`).

2. **`DeletePrefix` notifies every watcher in the removed subtree.** In
   go-immutable-radix, when the subtree's root node was already written earlier
   in the same transaction, `deletePrefix` empties that node *before* walking
   it to collect the channels to close, so watchers of keys inside the removed
   subtree are never woken: a lost notification. go-maemmidb wakes them. It
   never notifies less than upstream; the differential tests allow exactly this
   one superset and nothing else.

3. **A failing `Insert` or `Delete` leaves the transaction logically
   unchanged.** Upstream walks the indexes in Go map order and returns at the
   first failing indexer, leaving some indexes updated and others not. Here all
   index keys are computed before any secondary index is touched, and the
   primary-index write is undone on error, so every query answers as it did
   before the call (`TestFailedInsertLeavesTxnUnchanged`). Aborting after an
   error, which is what callers of upstream must do, remains correct.

4. **Which error is reported when an object violates several indexes at once.**
   Upstream reports whichever index Go's randomised map iteration reaches
   first. Here indexes are processed in a fixed order (`id`, then by name), so
   the reported error is deterministic. The message format is unchanged.

5. **The schema is compiled at `NewMemDB`.** Replacing indexers or flags inside
   a `DBSchema` after the database was created was never supported upstream
   (adding a table makes it panic); here such changes are simply not seen. The
   configuration *fields* of the built-in indexers (`Field`, `Lowercase`,
   `AllowMissing`, `Indexes`) are still read live.

6. **Both a fired watch and an expired timeout.** When a watch channel has
   already fired *and* the timeout has expired (or the context is done) at the
   time of the call, upstream's `select` picks one at random. For watch sets
   larger than 128 channels this implementation checks the timeout and the
   context first, so they win deterministically -- the usual Go convention for
   a context that is already done.

## Build tags

`-tags memdb_safe` (or `purego`) builds without `package unsafe`: struct fields
are then read through reflection with a cached field index instead of a cached
offset. Behaviour is identical; CI runs the whole suite in both modes.
