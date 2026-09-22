# Extensions

go-maemmidb adds bitmap indexes, a typed API and iterator adapters to
go-memdb's API. These features are optional and work alongside existing code.

## Bitmap indexes and set queries

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
same columns as ordinary indexes ([benchmarks](benchmarks.md#extensions)):

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

## A typed API

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

## Range-over-func

`memdb.All(it)` and `memdb.AllOf[*Person](it)` turn any `ResultIterator` into
an `iter.Seq`.

See the [tested examples](../example_ext_test.go) for complete programs.
