# Design

go-maemmidb keeps go-memdb's model -- one immutable radix tree per index, a
single writer, lock-free readers, watch channels on tree nodes -- and replaces
the machinery underneath. This document explains what was changed, why it is
faster, and why it is still correct.

## Where go-memdb spends its time

Measured by profiling and verified in the source of go-memdb `7d3fdd5` and
go-immutable-radix v1.3.1:

| Cost in the original | Replacement |
|---|---|
| Every radix node **and every leaf** gets a `make(chan struct{})` (96 B + an allocation) the moment it is written, whether or not anyone ever watches it. | Watch channels are created lazily, by the watcher. Writers allocate none. |
| To know whether a node may be modified in place, a transaction consults an `interface{}`-keyed LRU (`golang-lru`, 8192 entries) per node per write, and re-copies the node's prefix and its 16-byte edges. Reading your own writes clones the index transaction and throws that cache away. Past 8192 tracked channels, commit falls back to diffing the old and new trees. | Ownership epochs: one integer comparison. No cache, no cliff. |
| Child lookup is `sort.Search` with a closure over 16-byte edges. | A 256-bit bitmap plus a population count. |
| Every index access builds `[]byte(table + "." + index)` (two allocations) and looks it up in a root radix tree; modified indexes live in a map keyed by two strings. | The schema is compiled to integer slots; the database root is a flat slice. |
| Indexers find their struct field with `reflect.Value.FieldByName` on every call -- twice per index on an update -- and build each key through string concatenation and conversion (2-4 allocations per index per row). | Field offsets are resolved once; keys are appended to a reusable buffer. |
| A query allocates ~8 objects before it returns the first row. | One (the iterator); `First`/`Last` allocate nothing. |
| `WatchSet.Watch` starts a goroutine and a context per call, and always runs a 32-way select. | A select of the right size that includes the timeout itself. |

## The storage engine (`internal/radix`)

A persistent, path-compressed radix tree. It must stay a radix tree: go-memdb's
watch semantics are defined by it (a watch on a prefix *is* a watch on the node
whose subtree holds that prefix), and a compressed radix tree's shape is
canonical for a key set, which is what makes watch behaviour reproducible
exactly.

### Nodes

```go
type node struct {         // 96 bytes, then the child array
    prefix string          // compressed path segment, incl. the label byte
    val    interface{}     // value of the key ending here (valid iff leaf != nil)
    bitmap [4]uint64       // one bit per present label
                           // ---- end of the first cache line ----
    leaf   *leaf           // identity of that value, for watchers
    watch  slot            // lazily created watch channel
    epoch  uint64          // ownership stamp
    nkids, ckids uint16    // length and capacity of the child array
}

type leaf struct{ watch slot }
```

- **Rank-indexed children.** Child number `popcount(bitmap below label)` is the
  child for a label -- the rank trick of Roaring bitmaps and HAMTs. Lookup is
  branch-light and needs no search; ordered iteration is a walk over a dense
  array; lower-bound seeks find the next label with bit operations.
- **One allocation per node.** Nodes come in size classes (`node4` ... `node256`)
  that embed the child array right behind the header. Copying a node on write
  is a single allocation of the needed size (8 B per child; go-immutable-radix:
  16 B per edge in a second allocation), and descending a level touches one
  object.
- **No slice for the children.** The array's address is the node's plus a
  constant and its length is a 16-bit field, so the header needs no slice
  header. That is 16 bytes less to allocate and copy for every node on every
  written path, and it is what lets the three things a lookup or an iteration
  step reads -- the segment, the bitmap that locates the next child, and the
  value -- share the header's first cache line (the count is only read where
  the line after it is needed anyway; `Last` and reverse iteration count the
  bitmap instead). Reaching a child slot by address takes `unsafe`, confined to
  five accessors in `node_unsafe.go`; the `memdb_safe` build declares the node
  with an ordinary slice (`node_safe.go`), and every other line of the engine
  is shared. `go test -race` runs the accessors under the runtime's pointer
  checks.
- **Small nodes are 128 bytes, not 112.** A 96-byte header would put the most
  common nodes -- two children, or a short inline segment -- into the 112-byte
  size class, whose objects do not start on cache line boundaries: the header's
  first line would straddle two, and point lookups on small tables measured 3-6%
  slower for it. So there is no class below 128 bytes, which is what those nodes
  cost before the slice header was removed, with room for four children instead
  of two.
- **Immutable strings for path segments.** Substrings on split cost nothing,
  one-byte segments come from a static table, and a one-byte segment is never
  even read during lookup (the bitmap already matched its only byte).
- **Childless nodes hold their segment inline.** One node per stored key has no
  children, and its segment -- the tail of the key -- is the last thing a
  lookup compares. Those nodes are allocated in classes with a trailing byte
  array (32/48/64 bytes, each landing exactly on an allocator size class)
  and their `prefix` string points into it. That removes an allocation per
  inserted key and a cache miss per point lookup: measured in isolation, 10-13%
  off `First` hits and 30% fewer heap objects, at no cost in heap bytes. Building
  that string is the engine's other use of `unsafe` (the `memdb_safe` build uses
  ordinary strings). The rule that keeps it from pinning dead nodes: an inline
  segment is never shared with another node -- copying a childless node, or
  giving part of its segment to a different node, copies the bytes.
- **No keys in leaves, no size counter.** go-memdb consumes neither.
- **The value lives in the node; its identity lives in a leaf.** A `leaf` is
  eight bytes: the watch slot of one version of one key's value. It is a
  separate object shared by every copy of the node that holds the key, so the
  slot survives copying, splitting and merging -- watchers of an unchanged key
  must not fire, and a reader that arrives late through an *older* copy of the
  node must still be notified when the key finally changes (which is why the
  leaf cannot be created lazily). The value itself sits in the node, next to
  everything else a lookup or an iteration step touches, so reads never
  dereference the leaf unless they ask for a watch. go-immutable-radix pays
  that extra cache miss on every `Get` and every iterator step.

### Ownership epochs instead of a modification cache

Every node carries the epoch of the transaction that created it; epochs come
from one process-wide atomic counter (trees of a database and of its snapshots
share nodes and have independent writers, so an epoch must never be issued
twice). A write transaction may mutate a node in place iff
`node.epoch == txn.epoch`; any other node is copied first.

`Freeze()` makes everything written so far immutable in O(1): the transaction
forgets its epoch and draws a new one on its next write. go-memdb needs this
whenever uncommitted state escapes: an iterator created inside a write
transaction, `Txn.Snapshot()`, a watch channel. Plain reads (`First`, `Last`,
`LongestPrefix`) escape nothing and freeze nothing -- so the very common
"read, then write, in one transaction" loop never re-copies its path, whereas
upstream clones on every such read.

### Lazy watch channels, sealed on replacement

A watch slot is an atomic pointer that only ever moves
`nil -> channel -> sealed`.

- A **reader** materialises a channel with `CompareAndSwap(nil, ch)`.
- The **writer** records every object it replaces (only on the primary
  database, mirroring upstream's `TrackMutate(db.primary)`). After the new
  database root is published it **seals** each one: `old := slot.Swap(sealed)`
  and closes `old` if it was a real channel. `sealed` holds a permanently
  closed channel.

Why nothing is lost: for a replaced object, either the reader's CAS precedes
the writer's Swap, and the writer closes that very channel; or the Swap comes
first, the CAS fails, and the reader receives the closed channel -- correct,
because the object it looked at is stale. Because a transaction freezes before
handing out any channel, *a materialised slot implies an immutable node*: the
write path never reads or resets a slot, and ABA cannot occur.

The remaining rules make behaviour identical to go-immutable-radix rather than
merely safe: a miss returns the slot of the deepest node reached, *including a
child whose prefix diverges from the key* (that is the node an insert of the
key would replace); a failed delete copies and notifies nothing; a split seals
the trimmed child, a merge seals the absorbed child, neither touches leaves;
the root is never merged; `DeletePrefix` records only the subtree's root and
walks it at commit (an abort never pays for it); every index starts with its
own root object. A bit stolen from `epoch` marks a leaf as created in the
node's own epoch, so rewriting one key a million times in one transaction
records one object, not a million.

Tested invariant: after a commit on the primary database, *exactly* the
objects reachable from the old root and not from the new one are sealed.

## The database layer

- **Compiled schema.** `NewMemDB` resolves every index to a slot, asserts
  indexers to their interfaces once, fixes a deterministic index order (`id`
  first) and precomputes name resolution for both `name` and `name_prefix`.
  The database root is an immutable `[]radix.Tree` behind an atomic pointer.
- **Names are resolved by length.** Every query starts by resolving a table
  name and an index name, and on a small table two map lookups cost as much as
  the tree descent. A schema has few names and they rarely share a length, so
  they are kept in one array sorted by length and a lookup compares its
  argument with the few names that are exactly as long -- and callers nearly
  always pass the very constant the schema was built from, which compares equal
  by address. A length that many names share (fifty tables called `table-NN`)
  is bisected. The array is filled by a counting sort, two allocations and no
  comparisons, which keeps `NewMemDB` cheaper than building the maps was; the
  benchmark gate caught the first version, which was not.
- **Read transactions** are a pointer to a root: no tree transaction, no
  scratch state, safe to share between goroutines like upstream's. Queries
  build their key in a stack buffer. A read `Txn` is 32 bytes; what only a
  write transaction needs (its state, its deferred functions, its change list)
  is a second struct allocated in the same 80-byte object as the write `Txn`.
- **Write transactions** draw their bookkeeping (tree transactions, key
  buffers, the list of replaced objects) from a pool; `Txn` objects themselves
  are never pooled, since `Changes()` and repeated `Commit`/`Abort` must keep
  working afterwards.
- **Insert** writes the primary index first: one descent both stores the row
  and returns the previous version. It then builds the keys of all secondary
  indexes before touching any, so an indexer error leaves the transaction
  logically unchanged, and finally applies upstream's positional old/new key
  comparison.
- **Extractors** (`extract.go`) are an allocation-free twin of the built-in
  indexers: per index, a cached `(object type, field) -> offset` resolution and
  an encoder that appends to a buffer. They only take the well-defined path --
  a non-nil pointer to a struct, a field of the expected kind, well-formed
  arguments. Anything else is declined and handled by the original exported
  method, which therefore still owns every error message, quirk and panic.
  The exported methods themselves use the same machinery through a
  process-wide field cache and return a single exact allocation. They run a
  throw-away extractor on their stack, so the extractor is kept small: it
  reaches the user's indexer through one interface field and a type assertion
  per use, not through a typed pointer per kind of indexer.
- **`Changes()`** looks for repeated objects pairwise (few changes) or with a
  set of 64-bit hashes (many) before falling back to upstream's string-keyed
  map.
- **`WatchSet`** keeps its type (`map[<-chan struct{}]struct{}`). Up to 128
  channels wait in one generated select of the smallest sufficient size (1, 2,
  4 ... 128) that also waits on the timeout or context directly. Larger sets
  return at once if the timeout or context is already done, and are otherwise
  spread over goroutines in chunks of 32, each running a generated select
  without the timeout arm. (Chunks of 64 and 128, one slice for the whole set,
  and polling every channel up front were all measured and lost: select set-up
  is n log n, a pointer slice over 512 bytes pays for a malloc header and
  size-class slack, and the poll taxes the common blocking case.) A helper
  goroutine calls its 32-way select *directly*, not through the function that
  picks a select by size: on a new goroutine's small stack that one extra frame
  moved the first stack growth to the entry of the big select function, where
  the runtime walks the function's long stack table to size the new stack --
  12 µs per 1024-channel watch on a Zen 5, invisible on an M1, found by
  profiling the one benchmark family that was slower than upstream there.

## Where Roaring bitmaps fit, and where they do not

The obvious idea -- replace the entries of a non-unique index by
`value -> compressed bitmap of row ids` -- does not survive contact with
go-memdb's contract. Rows under one index value must come out ordered by
primary key (upstream's tests assert it), which a bitmap of insertion-ordered
ids cannot provide without sorting; `First()` on a low-cardinality index would
turn from O(depth) into O(matches); and copy-on-write of an 8 KB container per
single-row transaction copies more than a radix path does.

What the ordered, versioned hot path does take from Roaring is its core trick:
a bitmap plus a population count as the index into a dense array, used here
for every node's children. Roaring proper is an opt-in extension, below.

## Bitmap indexes (extension)

An index declared as `&BitmapIndex{Indexer: ...}` maps an index value to the
*set* of rows that have it, for the two questions an ordered index answers
badly: counting and combining. `Txn.Where` returns a `RowSet`; sets combine
with `And`, `Or` and `AndNot` and know their `Len`. A table without such an
index runs none of this: the write path tests one integer per row write, and
the database root carries one nil pointer.

- **Row ids.** A table with a bitmap index numbers its rows with small dense
  integers. A row keeps its id across updates; the id of a deleted row is
  reused, lowest first, so the id space stays as dense as the table. Three
  persistent structures do the bookkeeping, versioned with the index trees and
  published by the same atomic root swap: a radix tree from primary key to id,
  a vector from id to row object (`internal/pvec`, a 32-ary trie with 16-value
  leaves), and the sets of used and free ids.
- **The sets** (`internal/bitmap`) follow Roaring's layout -- a sparse
  directory of chunks addressed by the high bits of the id, children located by
  population count -- with different proportions, because the cost model is
  different. A Roaring container covers 2^16 ids and may be 8 KB, which is
  right for a mutable bitmap and wrong for a persistent one, where every
  single-row transaction would copy it. Here a chunk is 256 ids and the
  directory a 64-ary trie over the chunks, only as tall as the largest id
  needs: a write copies an 80-byte chunk and two or three small nodes. A set is
  a one-pointer value stored directly as the value of the index's radix tree,
  keyed by the index value alone. Writers own nodes by epoch, exactly like the
  radix tree, so a bulk load mutates in place.
- **Set algebra shares structure.** A result subtree that comes out equal to an
  operand's *is* that operand's; only chunks where the operands really differ
  are allocated. Intersecting three sets over 100,000 rows takes microseconds.
- **Updates that move nothing cost nothing.** An ordinary index stores the row
  object, so replacing a row rewrites its entry in every index. A bitmap index
  stores the row's id: if the row keeps its value, the index is not touched,
  and its watchers are not woken.
- **Watches** come from the radix tree the sets live in: `WhereWatch` fires when
  a row enters or leaves the set of that value.
- **What it gives up.** Results come in row id order, not index order; there are
  no range scans; `First`, `Get` and the other ordered queries refuse a bitmap
  index. A sparse set costs a chunk per 256-id range it touches, so a column
  with nearly as many values as rows belongs in an ordinary index.

## The typed API (extension)

- **Indexers from accessor functions** (`StringIndex[T]`, `IntIndex[T, N]`, ...)
  produce the keys of their `*FieldIndex` twins byte for byte, so an index can
  be switched from one to the other without anyone noticing. Transactions do
  not call their exported methods: the extractor asks the indexer for the
  *value* (one interface call, one call of the user's function) and encodes it
  into its own buffer. The value, not a destination buffer, crosses the
  interface on purpose -- a buffer passed to an interface method escapes, and
  the stack buffer every query builds its key in would move to the heap.
- **Typed keys** (`StringKey[T]`, `IntKey[T, N]`, `UintKey[T, N]`) take the key
  as a Go value. The untyped API cannot avoid boxing its arguments: the slow
  path hands them to the user's `FromArgs`, so they escape, and a string costs
  an allocation per query. A typed key also resolves its table and index once
  per schema (one atomic load and a pointer comparison afterwards). Whatever it
  cannot encode itself goes through the untyped path, errors included.

## What is deliberately not done

- **Slab/arena allocation of nodes.** One live node would pin its whole slab;
  with copy-on-write churn that is a several-fold memory blow-up.
- **Allocating a node together with its leaf.** A copied node would keep its
  predecessor alive through the shared leaf.
- **Assembly or SIMD.** Go cannot inline assembly, and at these sizes the call
  costs more than it saves.
