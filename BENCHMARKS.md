# Benchmarks

go-maemmidb claims to be faster than
[hashicorp/go-memdb](https://github.com/hashicorp/go-memdb) on every operation.
This document says how that is measured, how it is enforced, and what the
numbers are.

## Method

**One benchmark source, two builds.** The suite in [`benchmarks/`](benchmarks)
is written once, against type aliases. A build tag selects which package the
aliases point at:

```
benchmarks/shim_upstream.go   //go:build upstream    -> github.com/hashicorp/go-memdb v1.3.5
benchmarks/shim_new.go        //go:build !upstream   -> github.com/thevilledev/go-maemmidb
```

A type alias *is* the aliased type, so methods, variadic calls and composite
literals bind directly to the selected implementation: there is no adapter,
interface or wrapper in any measured path, and both builds produce benchmarks
with identical names in an identical package, which `benchstat` lines up by
itself. (`benchmarks/apilock.go`, compiled in both builds, doubles as the proof
that the two APIs are identical.) `benchmarks/` is a separate Go module, so the
original implementation never becomes a dependency of the main module.

**Steady state.** A benchmark that lets the database grow punishes the faster
implementation: it completes more iterations, so it works on a bigger tree.
Every write benchmark therefore leaves the database the size it found it:
insert-then-abort, insert/commit-then-delete/commit, or updates of existing
rows. Populated databases come in 1,000 and 100,000 rows (1,000,000 with
`BENCH_LARGE=1`).

**One process per benchmark family.** `scripts/bench-compare.sh` builds both
test binaries once and then runs every top-level benchmark in a process of its
own. A Go process's heap has a history -- how big it has been, what the
scavenger has returned to the OS, which pages must be faulted back in -- and
inside one long-lived process that history leaks from one benchmark into the
next, differently for the two implementations because their databases differ in
size. (An early version of this harness ran the whole suite in one process; a
lookup benchmark that takes 167 ns on its own measured 366 ns there, behind a
benchmark that had just released a large database. Those results were thrown
away.) With one process per family, a family shares its heap with the databases
it needs and with nothing else.

**A different function layout every round.** Where the linker happens to put
a hot loop or a runtime function relative to cache-line and page boundaries is
worth tens of percent on small benchmarks, on some CPUs more than on others. One
build of this suite measured 13.3 ns for `IntFieldIndex.FromArgs` on a Zen 5 --
a function whose source had not changed since a build that measured 9.3 ns --
and 164 ns instead of 108 ns for `LongestPrefix`; relinked with four other
layouts, the same code measured 9.0-9.4 ns and 108 ns in all four. With one
binary per side that luck is a bias nobody can see, in either direction. So the
script links a set of binaries per round with `-ldflags=-randlayout=<round>`:
layout becomes part of the spread that the statistics already account for.

**A warm-up pass that is thrown away.** `go test -count N` runs a whole family
N times in sequence, so the first sample of every benchmark is taken while the
process is still building its databases, one before each benchmark: the heap
grows by hundreds of megabytes between two measurements and the collector runs
back to back. That has nothing to do with the code being measured, and it is
not the same for both sides (the databases differ in size). On the 100,000-row
write benchmarks it made the first sample of a process up to four times slower
than every later one -- 11.4 µs, then 2.8 µs, reproducibly -- which the first
published run of this suite shows as confidence intervals of ±60% and as a
handful of 2-4x wins reported as "statistically equal". The script now runs
`COUNT+WARMUP` passes and drops the first `WARMUP` (default 1).

**Deterministic data** from a seeded PCG generator. Three key shapes: random
UUID strings (uniform fan-out), sequential ids (deep shared prefix) and
hierarchical paths (long shared prefixes). Five schemas: one index; three
indexes (the shape of upstream's own test schema); eleven indexes covering
every built-in indexer; three *custom* function indexers, the style large
go-memdb users prefer, so the results are not over-fitted to the reflection
indexers; and fifty tables, to expose per-commit costs that scale with the
schema.

**Interleaved runs.** Within a round the two implementations alternate family
by family, and the order flips every round (A B, then B A), so that thermal or
background drift hits both sides equally. `ROUNDS` rounds of `COUNT` samples
give every benchmark `ROUNDS x COUNT` samples per side; `benchstat` compares.

**The gate.** `make bench-gate` (`benchmarks/cmd/benchgate`) fails unless, for
*every* benchmark, the new implementation

- is not slower: a slowdown counts if it is statistically significant
  (Mann-Whitney U, α = 0.05 -- the test `benchstat` uses) *and* exceeds a 2%
  tolerance. The tolerance exists for code paths that are identical in both
  implementations (`MemDB.Snapshot`, `WatchSet.Add`), where with ~190 benchmarks
  a pure significance test would produce a handful of false alarms; `make
  bench-aa` (upstream against itself) measures that noise floor;
- does not allocate more often (allocs/op, exact);
- does not use more memory (B/op, heap bytes and heap objects per row).

**The same gate against ourselves.** "Faster than upstream" says nothing about
whether last week's change made anything slower. `make bench-self BASE=<rev>`
builds the library of any git revision under *today's* benchmark sources and
runs it interleaved with the working tree; `make bench-self-gate` applies the
gate to the pair, with one concession: allocs/op may differ by 1%, because a
write transaction's pooled scratch state is dropped by every garbage collection
and the average moves by a fraction with the collector's timing. `SIDES=
'upstream base new'` interleaves all three in one run.

**What is covered.** Single-row and batched inserts, insert/delete cycles,
updates with unchanged and with changed index keys, deletes, `DeleteAll`,
`DeletePrefix`, bulk loads with heap footprint and full-GC time, `First`/`Last`
on every kind of index (hit and miss), `Get` plus iteration in both directions,
`LowerBound`/`ReverseLowerBound`, `LongestPrefix`, `FilterIterator`,
transaction and snapshot overhead, reads of uncommitted writes, iterators over
dirty indexes, `Txn.Snapshot`, change tracking with and without duplicates,
watched lookups, the full watch-update-notify cycle, commit with a hundred
watchers, `WatchSet` from 1 to 1024 channels (expired, cancelled, fired,
blocking), every exported indexer method, database construction, and parallel
readers with and without a concurrent writer. The three benchmarks that ship
inside upstream's own test files exercise unexported code, so they are compared
by running them in both checkouts (`make bench-inpkg`).

## Reproducing

```bash
make bench-compare                 # full interleaved A/B run + benchstat (long)
make bench-gate                    # enforce "universally faster" on that run
make bench-inpkg UPSTREAM=../go-memdb
make bench-parallel                # read scalability at 1, 4 and 8 procs
make bench-compare BENCH='First|Iterate' ROUNDS=3   # a subset
make bench-self BASE=origin/main && make bench-self-gate   # against our own history
```

Before measuring, make sure nothing else is running -- in particular no
orphaned `benchmarks.test` process from an interrupted earlier run.

## Results

Measured on 2026-09-20 with `make bench-compare BASE=main SIDES='upstream base
new'` (three rounds of two samples, 0.5 s windows, one discarded warm-up pass
per process, a different function layout per round) on two machines:

- an **Apple M1 Max** (macOS, Go 1.27.1), a desktop carrying its ordinary
  background load -- which is why the runs are interleaved, and why absolute
  times are on the slow side; the ratios are what to read;
- an **AMD Ryzen AI 9 HX PRO 370** (Zen 5, Linux 6.18, otherwise idle). It has
  no Go toolchain: the same sources were cross-compiled to static linux/amd64
  binaries, and each benchmark process was pinned to one of the four full
  Zen 5 cores (the chip also has eight Zen 5c cores, and a run that migrates
  between the two kinds measures the scheduler).

Both runs are published unedited: no benchmark was re-measured and spliced in.
The raw samples of the second machine are in
[`benchmarks/results/linux-amd64-zen5`](benchmarks/results/linux-amd64-zen5).

### Environment

```
goos: darwin
goarch: arm64
cpu: Apple M1 Max
go:     go version go1.27.1 darwin/arm64
upstream: github.com/hashicorp/go-memdb v1.3.5, github.com/hashicorp/go-immutable-radix v1.3.1
samples:  6 per benchmark and implementation
```

### The gate

```
benchgate: 190 benchmarks compared: 186 faster, 4 statistically equal, geomean speed-up 2.74x
benchgate: 32 benchmarks exist only for the new implementation (extensions), not gated
benchgate: PASS -- no benchmark is slower, allocates more often, or uses more memory than upstream
```

On the Zen 5:

```
benchgate: 190 benchmarks compared: 186 faster, 4 statistically equal, geomean speed-up 2.94x
benchgate: PASS -- no benchmark is slower, allocates more often, or uses more memory than upstream
```

### Summary by benchmark family

Speed-up is upstream's time divided by go-maemmidb's (median of the samples);
allocs/op and B/op are the change in the family's total.

| Benchmark family | cases | speed-up (geomean) | worst case | best case | allocs/op | B/op |
|---|---:|---:|---:|---:|---:|---:|
| BulkLoad | 4 | **3.99x** | 3.59x | 4.55x | -82% | -75% |
| Changes | 4 | **3.08x** | 2.83x | 3.35x | -83% | -70% |
| CommitNotify | 2 | **3.65x** | 3.41x | 3.91x | -83% | -74% |
| DeleteAbort | 4 | **2.95x** | 2.51x | 3.48x | -88% | -66% |
| DeleteAllAbort | 4 | **2.91x** | 1.39x | 4.39x | -90% | -74% |
| DeletePrefixAbort | 2 | **3.81x** | 3.52x | 4.12x | -93% | -80% |
| FilterIterator | 1 | **2.12x** | 2.12x | 2.12x | -75% | -55% |
| First | 20 | **2.30x** | 1.57x | 3.84x | -71% | -81% |
| FirstIndex | 12 | **2.72x** | 1.55x | 17.89x | -90% | -95% |
| FirstWatch | 2 | **1.98x** | 1.57x | 2.49x | -80% | -91% |
| GetOne | 2 | **1.92x** | 1.72x | 2.15x | -78% | -45% |
| GetWatchCh | 2 | **2.14x** | 2.12x | 2.16x | -73% | -44% |
| Indexer | 23 | **1.93x** | 1.00x | 3.64x | -62% | -52% |
| InsertAbort | 16 | **2.55x** | 1.18x | 4.24x | -79% | -66% |
| InsertDeleteCommit | 12 | **2.56x** | 1.45x | 4.45x | -88% | -69% |
| InsertDeleteCommitManyTables | 1 | **3.28x** | 3.28x | 3.28x | -88% | -65% |
| Iterate | 14 | **2.22x** | 1.28x | 4.56x | -82% | -69% |
| IterateInWriteTxn | 2 | **2.82x** | 2.56x | 3.10x | -82% | -65% |
| Last | 4 | **5.86x** | 1.78x | 31.71x | -94% | -98% |
| LongestPrefix | 2 | **1.29x** | 1.28x | 1.29x | -25% | -38% |
| LowerBound | 5 | **2.22x** | 1.85x | 2.62x | -85% | -74% |
| NewMemDB | 3 | **2.41x** | 1.19x | 3.85x | -84% | -77% |
| ParallelTxnFirst | 2 | **2.42x** | 2.22x | 2.64x | -71% | -84% |
| ParallelTxnFirstWithWriter | 1 | **2.14x** | 2.14x | 2.14x | -71% | -84% |
| ReadYourWrites | 2 | **3.61x** | 3.56x | 3.66x | -82% | -69% |
| Snapshot | 1 | **1.02x** | 1.02x | 1.02x | +0% | +0% |
| SnapshotWrite | 1 | **3.00x** | 3.00x | 3.00x | -85% | -67% |
| Txn | 4 | **2.13x** | 1.53x | 2.64x | -58% | -49% |
| TxnFirst | 2 | **2.53x** | 2.37x | 2.71x | -71% | -84% |
| TxnSnapshot | 1 | **2.84x** | 2.84x | 2.84x | -86% | -66% |
| UpdateCommit | 12 | **2.76x** | 1.04x | 4.34x | -88% | -69% |
| WatchCycle | 2 | **3.88x** | 3.58x | 4.19x | -87% | -72% |
| WatchSetAdd | 1 | **1.11x** | 1.11x | 1.11x | +0% | +0% |
| WatchSetBlockThenFire | 2 | **1.04x** | 1.04x | 1.04x | -4% | -3% |
| WatchSetWatchCtxCancelled | 6 | **13.62x** | 1.00x | 2363.09x | -100% | -100% |
| WatchSetWatchCtxFired | 6 | **1.33x** | 1.00x | 2.11x | -15% | -11% |
| WatchSetWatchExpired | 6 | **25.96x** | 1.79x | 3243.24x | -100% | -100% |
| **all** | 190 | **2.74x** | | | | |

<details>
<summary>The same table for the Zen 5</summary>

| Benchmark family | cases | speed-up (geomean) | worst case | best case | allocs/op | B/op |
|---|---:|---:|---:|---:|---:|---:|
| BulkLoad | 4 | **4.45x** | 3.23x | 5.03x | -82% | -75% |
| Changes | 4 | **3.66x** | 2.95x | 5.29x | -83% | -70% |
| CommitNotify | 2 | **4.02x** | 3.21x | 5.05x | -83% | -74% |
| DeleteAbort | 4 | **2.90x** | 1.54x | 4.38x | -88% | -66% |
| DeleteAllAbort | 4 | **3.41x** | 1.35x | 5.97x | -90% | -74% |
| DeletePrefixAbort | 2 | **4.93x** | 4.54x | 5.35x | -93% | -80% |
| FilterIterator | 1 | **1.60x** | 1.60x | 1.60x | -75% | -55% |
| First | 20 | **2.89x** | 1.54x | 6.41x | -71% | -81% |
| FirstIndex | 12 | **2.62x** | 1.43x | 25.92x | -90% | -95% |
| FirstWatch | 2 | **2.18x** | 1.47x | 3.24x | -80% | -91% |
| GetOne | 2 | **2.06x** | 1.59x | 2.66x | -78% | -45% |
| GetWatchCh | 2 | **2.21x** | 1.99x | 2.45x | -73% | -44% |
| Indexer | 23 | **2.07x** | 0.97x | 4.00x | -62% | -52% |
| InsertAbort | 16 | **2.74x** | 1.08x | 5.19x | -79% | -66% |
| InsertDeleteCommit | 12 | **2.64x** | 1.47x | 4.89x | -88% | -69% |
| InsertDeleteCommitManyTables | 1 | **3.98x** | 3.98x | 3.98x | -88% | -65% |
| Iterate | 14 | **2.08x** | 1.25x | 4.06x | -82% | -69% |
| IterateInWriteTxn | 2 | **3.25x** | 2.74x | 3.85x | -82% | -65% |
| Last | 4 | **7.36x** | 1.76x | 64.97x | -94% | -98% |
| LongestPrefix | 2 | **1.82x** | 1.61x | 2.07x | -25% | -38% |
| LowerBound | 5 | **2.48x** | 2.10x | 3.09x | -85% | -74% |
| NewMemDB | 3 | **2.45x** | 1.16x | 4.05x | -84% | -77% |
| ParallelTxnFirst | 2 | **2.61x** | 2.00x | 3.41x | -71% | -84% |
| ParallelTxnFirstWithWriter | 1 | **2.01x** | 2.01x | 2.01x | -71% | -84% |
| ReadYourWrites | 2 | **3.80x** | 3.09x | 4.69x | -82% | -69% |
| Snapshot | 1 | **1.08x** | 1.08x | 1.08x | +0% | +0% |
| SnapshotWrite | 1 | **3.14x** | 3.14x | 3.14x | -85% | -67% |
| Txn | 4 | **2.17x** | 1.55x | 2.75x | -58% | -49% |
| TxnFirst | 2 | **2.59x** | 1.98x | 3.38x | -71% | -84% |
| TxnSnapshot | 1 | **2.00x** | 2.00x | 2.00x | -86% | -66% |
| UpdateCommit | 12 | **3.15x** | 1.22x | 5.78x | -88% | -69% |
| WatchCycle | 2 | **3.77x** | 2.79x | 5.11x | -87% | -72% |
| WatchSetAdd | 1 | **1.06x** | 1.06x | 1.06x | +0% | +0% |
| WatchSetBlockThenFire | 2 | **1.03x** | 1.03x | 1.03x | -4% | -4% |
| WatchSetWatchCtxCancelled | 6 | **14.28x** | 1.00x | 2720.28x | -100% | -100% |
| WatchSetWatchCtxFired | 6 | **1.27x** | 0.99x | 2.27x | -15% | -12% |
| WatchSetWatchExpired | 6 | **26.84x** | 1.81x | 3143.05x | -100% | -100% |
| **all** | 190 | **2.94x** | | | | |

</details>

### Memory

From `BenchmarkBulkLoad`: a database loaded in one transaction, then a forced
collection with only that database live. (Bytes and objects are the same on
both machines; the collection times are the M1's.)

| schema, 100,000 rows | | upstream | go-maemmidb | |
|---|---|---:|---:|---:|
| 3 indexes | heap bytes per row | 2,443 | 1,010 | **-59%** |
| | heap objects per row | 32.2 | 11.7 | **-64%** |
| | full GC cycle | 202 ms | 148 ms | -27% |
| 11 indexes | heap bytes per row | 6,529 | 2,698 | **-59%** |
| | heap objects per row | 86.4 | 31.5 | **-64%** |
| | full GC cycle | 636 ms | 409 ms | -36% |

### What "statistically equal" means here

Four of the 190 benchmarks show no significant difference on either machine,
and they are nearly the same four: the boolean and conditional indexer methods
(upstream's code, unchanged, 8 ns), and `WatchSet` waits on exactly 32 channels,
where both implementations run one 33-way `select` and the time is the Go
runtime's. (On the M1 one noisy write benchmark of the 11-index schema joins
them.) Nothing is slower.

### Extensions

go-memdb has no bitmap indexes and no typed API, so these benchmarks run on
go-maemmidb only and compare an extension with the equivalent use of the
common API (`impl=scan`, `classic`, `untyped`, `field`) in the same process:

```
                                                      │ results/tmp-ext.txt │
                                                      │       sec/op        │
ExtCount/size=1000/impl=scan                                   31.17µ ±  4%
ExtCount/size=1000/impl=bitmap                                 466.9n ±  1%
ExtCount/size=100000/impl=scan                                 213.8µ ±  1%
ExtCount/size=100000/impl=bitmap                               17.94µ ± 17%
ExtCountOne/size=1000/impl=scan                                1.298µ ±  3%
ExtCountOne/size=1000/impl=bitmap                              38.27n ±  1%
ExtCountOne/size=100000/impl=scan                              241.6µ ±  3%
ExtCountOne/size=100000/impl=bitmap                            38.84n ±  2%
ExtIterate/size=1000/impl=scan                                 1.332µ ±  3%
ExtIterate/size=1000/impl=bitmap                               1.429µ ±  4%
ExtIterate/size=100000/impl=scan                               656.6µ ±  3%
ExtIterate/size=100000/impl=bitmap                             236.8µ ±  6%
ExtWrite/op=insert-delete/size=1000/impl=classic               8.513µ ±  1%
ExtWrite/op=update-same-keys/size=1000/impl=classic            4.441µ ± 11%
ExtWrite/op=insert-delete/size=1000/impl=bitmap                8.953µ ±  2%
ExtWrite/op=update-same-keys/size=1000/impl=bitmap             1.873µ ±  1%
ExtWrite/op=insert-delete/size=100000/impl=classic             24.61µ ±  3%
ExtWrite/op=update-same-keys/size=100000/impl=classic          11.58µ ± 13%
ExtWrite/op=insert-delete/size=100000/impl=bitmap              21.66µ ± 16%
ExtWrite/op=update-same-keys/size=100000/impl=bitmap           4.493µ ±  5%
ExtFootprint/size=1000/impl=classic                            2.109n ±  2%
ExtFootprint/size=1000/impl=bitmap                             2.129n ±  2%
ExtFootprint/size=100000/impl=classic                          2.117n ±  1%
ExtFootprint/size=100000/impl=bitmap                           2.112n ±  1%
ExtTypedFirst/size=1000/impl=untyped                           92.25n ±  2%
ExtTypedFirst/size=1000/impl=table                             92.97n ±  3%
ExtTypedFirst/size=1000/impl=key                               50.79n ±  1%
ExtTypedFirst/size=100000/impl=untyped                         228.8n ±  7%
ExtTypedFirst/size=100000/impl=table                           233.8n ±  6%
ExtTypedFirst/size=100000/impl=key                             157.4n ±  7%
ExtTypedInsert/impl=field                                      45.27µ ±  5%
ExtTypedInsert/impl=func                                       45.36µ ±  3%
geomean                                                        1.364µ
                                                      │ results/tmp-ext.txt │
                                                      │        B/op         │
ExtCount/size=1000/impl=scan                                   128.0 ± 0%
ExtCount/size=1000/impl=bitmap                                 656.0 ± 0%
ExtCount/size=100000/impl=scan                                 128.0 ± 0%
ExtCount/size=100000/impl=bitmap                             12.78Ki ± 0%
ExtCountOne/size=1000/impl=scan                                128.0 ± 0%
ExtCountOne/size=1000/impl=bitmap                              0.000 ± 0%
ExtCountOne/size=100000/impl=scan                              128.0 ± 0%
ExtCountOne/size=100000/impl=bitmap                            0.000 ± 0%
ExtIterate/size=1000/impl=scan                                 128.0 ± 0%
ExtIterate/size=1000/impl=bitmap                               544.0 ± 0%
ExtIterate/size=100000/impl=scan                               128.0 ± 0%
ExtIterate/size=100000/impl=bitmap                           40.42Ki ± 0%
ExtWrite/op=insert-delete/size=1000/impl=classic             9.762Ki ± 0%
ExtWrite/op=update-same-keys/size=1000/impl=classic          5.137Ki ± 0%
ExtWrite/op=insert-delete/size=1000/impl=bitmap              11.41Ki ± 0%
ExtWrite/op=update-same-keys/size=1000/impl=bitmap           2.029Ki ± 0%
ExtWrite/op=insert-delete/size=100000/impl=classic           14.15Ki ± 0%
ExtWrite/op=update-same-keys/size=100000/impl=classic        7.216Ki ± 0%
ExtWrite/op=insert-delete/size=100000/impl=bitmap            15.78Ki ± 0%
ExtWrite/op=update-same-keys/size=100000/impl=bitmap         2.723Ki ± 0%
ExtTypedFirst/size=1000/impl=untyped                           16.00 ± 0%
ExtTypedFirst/size=1000/impl=table                             16.00 ± 0%
ExtTypedFirst/size=1000/impl=key                               0.000 ± 0%
ExtTypedFirst/size=100000/impl=untyped                         16.00 ± 0%
ExtTypedFirst/size=100000/impl=table                           16.00 ± 0%
ExtTypedFirst/size=100000/impl=key                             0.000 ± 0%
ExtTypedInsert/impl=field                                    60.67Ki ± 0%
ExtTypedInsert/impl=func                                     60.67Ki ± 0%
geomean                                                                   ¹
¹ summaries must be >0 to compute geomean
                                                      │ results/tmp-ext.txt │
                                                      │      allocs/op      │
ExtCount/size=1000/impl=scan                                   1.000 ± 0%
ExtCount/size=1000/impl=bitmap                                 8.000 ± 0%
ExtCount/size=100000/impl=scan                                 1.000 ± 0%
ExtCount/size=100000/impl=bitmap                               152.0 ± 0%
ExtCountOne/size=1000/impl=scan                                1.000 ± 0%
ExtCountOne/size=1000/impl=bitmap                              0.000 ± 0%
ExtCountOne/size=100000/impl=scan                              1.000 ± 0%
ExtCountOne/size=100000/impl=bitmap                            0.000 ± 0%
ExtIterate/size=1000/impl=scan                                 1.000 ± 0%
ExtIterate/size=1000/impl=bitmap                               7.000 ± 0%
ExtIterate/size=100000/impl=scan                               1.000 ± 0%
ExtIterate/size=100000/impl=bitmap                             433.0 ± 0%
ExtWrite/op=insert-delete/size=1000/impl=classic               64.00 ± 0%
ExtWrite/op=update-same-keys/size=1000/impl=classic            36.00 ± 0%
ExtWrite/op=insert-delete/size=1000/impl=bitmap                109.0 ± 0%
ExtWrite/op=update-same-keys/size=1000/impl=bitmap             14.00 ± 0%
ExtWrite/op=insert-delete/size=100000/impl=classic             84.00 ± 0%
ExtWrite/op=update-same-keys/size=100000/impl=classic          46.00 ± 0%
ExtWrite/op=insert-delete/size=100000/impl=bitmap              145.0 ± 0%
ExtWrite/op=update-same-keys/size=100000/impl=bitmap           16.00 ± 0%
ExtTypedFirst/size=1000/impl=untyped                           1.000 ± 0%
ExtTypedFirst/size=1000/impl=table                             1.000 ± 0%
ExtTypedFirst/size=1000/impl=key                               0.000 ± 0%
ExtTypedFirst/size=100000/impl=untyped                         1.000 ± 0%
ExtTypedFirst/size=100000/impl=table                           1.000 ± 0%
ExtTypedFirst/size=100000/impl=key                             0.000 ± 0%
ExtTypedInsert/impl=field                                      699.0 ± 0%
ExtTypedInsert/impl=func                                       699.0 ± 0%
geomean                                                                   ¹
¹ summaries must be >0 to compute geomean
                                      │ results/tmp-ext.txt │
                                      │      heapB/row      │
ExtFootprint/size=1000/impl=classic             1.215k ± 0%
ExtFootprint/size=1000/impl=bitmap               470.6 ± 0%
ExtFootprint/size=100000/impl=classic           1.213k ± 0%
ExtFootprint/size=100000/impl=bitmap             453.0 ± 0%
geomean                                          748.7
                                      │ results/tmp-ext.txt │
                                      │    heapobjs/row     │
ExtFootprint/size=1000/impl=classic              13.99 ± 0%
ExtFootprint/size=1000/impl=bitmap               5.484 ± 0%
ExtFootprint/size=100000/impl=classic            14.03 ± 0%
ExtFootprint/size=100000/impl=bitmap             5.307 ± 0%
geomean                                          8.694
```

### Against the previous revision of this package

The same run interleaved a third side: the library as of `main (a13ae5a)`,
under the same benchmark sources (`make bench-self`). Its samples are in
[`benchmarks/results/previous.txt`](benchmarks/results/previous.txt) and
[`benchmarks/results/linux-amd64-zen5/previous.txt`](benchmarks/results/linux-amd64-zen5/previous.txt).

```
benchgate: 190 benchmarks compared: 101 faster, 89 statistically equal, geomean speed-up 1.06x
benchgate: 32 benchmarks exist only for the new implementation (extensions), not gated
benchgate: PASS -- no benchmark is slower, allocates more often, or uses more memory than the previous revision
```

| Benchmark family | cases | speed-up (geomean) | worst case | best case | allocs/op | B/op |
|---|---:|---:|---:|---:|---:|---:|
| BulkLoad | 4 | **1.14x** | 1.06x | 1.37x | -5% | -16% |
| Changes | 4 | **1.05x** | 1.02x | 1.08x | -0% | -6% |
| CommitNotify | 2 | **1.06x** | 1.04x | 1.09x | +0% | -7% |
| DeleteAbort | 4 | **1.02x** | 0.98x | 1.07x | +0% | -4% |
| DeleteAllAbort | 4 | **1.05x** | 1.02x | 1.10x | +0% | -5% |
| DeletePrefixAbort | 2 | **1.01x** | 0.99x | 1.03x | +0% | -5% |
| FilterIterator | 1 | **1.23x** | 1.23x | 1.23x | +0% | -23% |
| First | 20 | **1.05x** | 1.00x | 1.20x | +0% | +0% |
| FirstIndex | 12 | **1.09x** | 0.96x | 1.38x | +0% | +0% |
| FirstWatch | 2 | **1.05x** | 1.03x | 1.06x | +0% | +0% |
| GetOne | 2 | **1.07x** | 1.01x | 1.13x | +0% | -25% |
| GetWatchCh | 2 | **1.20x** | 1.18x | 1.22x | +0% | -23% |
| Indexer | 23 | **1.01x** | 0.99x | 1.12x | +0% | +0% |
| InsertAbort | 16 | **1.05x** | 0.98x | 1.13x | -0% | -7% |
| InsertDeleteCommit | 12 | **1.06x** | 0.96x | 1.56x | +0% | -5% |
| InsertDeleteCommitManyTables | 1 | **1.02x** | 1.02x | 1.02x | +0% | -5% |
| Iterate | 14 | **1.11x** | 1.01x | 1.29x | +0% | -25% |
| IterateInWriteTxn | 2 | **1.05x** | 1.04x | 1.05x | +0% | -7% |
| Last | 4 | **1.13x** | 1.05x | 1.32x | +0% | +0% |
| LongestPrefix | 2 | **1.04x** | 1.03x | 1.05x | +0% | +0% |
| LowerBound | 5 | **1.26x** | 1.07x | 1.66x | +0% | -26% |
| NewMemDB | 3 | **1.13x** | 1.06x | 1.19x | -6% | -18% |
| ParallelTxnFirst | 2 | **1.13x** | 1.12x | 1.13x | +0% | -50% |
| ParallelTxnFirstWithWriter | 1 | **1.08x** | 1.08x | 1.08x | +0% | -50% |
| ReadYourWrites | 2 | **1.09x** | 1.06x | 1.12x | -0% | -8% |
| Snapshot | 1 | **1.01x** | 1.01x | 1.01x | +0% | +0% |
| SnapshotWrite | 1 | **1.12x** | 1.12x | 1.12x | +0% | -7% |
| Txn | 4 | **1.17x** | 0.99x | 1.55x | -17% | -16% |
| TxnFirst | 2 | **1.16x** | 1.14x | 1.18x | +0% | -50% |
| TxnSnapshot | 1 | **1.05x** | 1.05x | 1.05x | +0% | -4% |
| UpdateCommit | 12 | **1.03x** | 0.92x | 1.12x | +0% | -5% |
| WatchCycle | 2 | **1.07x** | 1.05x | 1.09x | +0% | -7% |
| WatchSetAdd | 1 | **1.00x** | 1.00x | 1.00x | +0% | +0% |
| WatchSetBlockThenFire | 2 | **1.02x** | 1.01x | 1.04x | +0% | -0% |
| WatchSetWatchCtxCancelled | 6 | **1.00x** | 1.00x | 1.01x | 0 → 0 | 0 → 0 |
| WatchSetWatchCtxFired | 6 | **1.02x** | 1.00x | 1.04x | +0% | +0% |
| WatchSetWatchExpired | 6 | **1.01x** | 1.00x | 1.03x | 0 → 0 | 0 → 0 |
| **all** | 190 | **1.06x** | | | | |

The Zen 5 agrees on the whole -- 94 faster, 90 equal, 1.05x overall -- and, with
its much tighter measurements (+/-0.5%), flags six benchmarks between +2.2% and
+6.6%. Two of them repeat from run to run: `TxnSnapshot` and
`DeletePrefixAbort` on the 100,000-row tables, both of which spend most of
their time in the garbage collector marking the fixture (a quarter of the
profile is `runtime` span bookkeeping) and both of which measure equal or
faster when run alone in a process. A smaller database shifts the collector's
pacing; the four others are 17 ns to 10 µs benchmarks that differ between runs.
They are listed here because a gate that is only reported when it passes is
not a gate.

### Upstream's own benchmarks

The three benchmarks in go-memdb's test files, run in both checkouts:

```
goos: darwin
goarch: arm64
pkg: memdb
cpu: Apple M1 Max
                                 │ results/inpkg-upstream.txt │        results/inpkg-new.txt        │
                                 │           sec/op           │   sec/op     vs base                │
UUIDFieldIndex_parseString-10                   113.75n ± 18%   82.25n ± 3%  -27.69% (p=0.000 n=10)
CompoundMultiIndex_FromObject-10                 601.9n ±  1%   236.0n ± 0%  -60.80% (p=0.000 n=10)
Watch-10                                      94190.50n ± 10%   20.72n ± 0%  -99.98% (p=0.000 n=10)
geomean                                          1.861µ         73.81n       -96.03%
                                 │ results/inpkg-upstream.txt │          results/inpkg-new.txt           │
                                 │            B/op            │    B/op      vs base                     │
UUIDFieldIndex_parseString-10                      48.00 ± 0%    16.00 ± 0%   -66.67% (p=0.000 n=10)
CompoundMultiIndex_FromObject-10                   640.0 ± 0%    352.0 ± 0%   -45.00% (p=0.000 n=10)
Watch-10                                         10.95Ki ± 0%   0.00Ki ± 0%  -100.00% (p=0.000 n=10)
geomean                                            701.0                     ?                       ¹ ²
¹ summaries must be >0 to compute geomean
² ratios must be >0 to compute geomean
                                 │ results/inpkg-upstream.txt │          results/inpkg-new.txt          │
                                 │         allocs/op          │ allocs/op   vs base                     │
UUIDFieldIndex_parseString-10                      2.000 ± 0%   1.000 ± 0%   -50.00% (p=0.000 n=10)
CompoundMultiIndex_FromObject-10                  28.000 ± 0%   5.000 ± 0%   -82.14% (p=0.000 n=10)
Watch-10                                           79.00 ± 0%    0.00 ± 0%  -100.00% (p=0.000 n=10)
geomean                                            16.42                    ?                       ¹ ²
¹ summaries must be >0 to compute geomean
² ratios must be >0 to compute geomean
```

### Parallel readers

```
                               │            sec/op             │    sec/op     vs base               │
ParallelTxnFirst/size=1000                         296.5n ± 3%   111.3n ±  2%  -62.48% (p=0.002 n=6)
ParallelTxnFirst/size=1000-4                       89.77n ± 3%   30.98n ±  8%  -65.48% (p=0.002 n=6)
ParallelTxnFirst/size=1000-8                       84.61n ± 5%   22.64n ± 14%  -73.24% (p=0.002 n=6)
ParallelTxnFirst/size=100000                       917.1n ± 6%   419.1n ±  2%  -54.30% (p=0.002 n=6)
ParallelTxnFirst/size=100000-4                     280.5n ± 3%   139.8n ±  4%  -50.16% (p=0.002 n=6)
ParallelTxnFirst/size=100000-8                    154.25n ± 5%   67.38n ±  2%  -56.32% (p=0.002 n=6)
ParallelTxnFirstWithWriter                         929.5n ± 3%   424.2n ±  8%  -54.37% (p=0.002 n=6)
ParallelTxnFirstWithWriter-4                       286.9n ± 2%   138.2n ±  2%  -51.85% (p=0.002 n=6)
ParallelTxnFirstWithWriter-8                      153.65n ± 4%   68.19n ±  4%  -55.62% (p=0.002 n=6)
geomean                                            248.9n        102.3n        -58.88%
                               │ results/parallel-upstream.txt │     results/parallel-new.txt      │
```

### Full results

The raw samples are in [`benchmarks/results/upstream.txt`](benchmarks/results/upstream.txt) and
[`benchmarks/results/new.txt`](benchmarks/results/new.txt); the complete `benchstat` comparison
(time, bytes, allocations and the heap metrics of every benchmark) is in
[`benchmarks/results/benchstat.txt`](benchmarks/results/benchstat.txt).
