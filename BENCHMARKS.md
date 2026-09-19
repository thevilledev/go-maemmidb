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
```

Before measuring, make sure nothing else is running -- in particular no
orphaned `benchmarks.test` process from an interrupted earlier run.

## Results

Measured on 2026-09-19 with `make bench-compare ROUNDS=3 COUNT=2 BENCHTIME=0.4s`
(six samples per benchmark and implementation) on a desktop machine carrying
its ordinary background load -- which is why the runs are interleaved, and why
absolute times are on the slow side; the ratios are what to read.

The published files are that run plus targeted re-runs, each interleaved the
same way and spliced in by benchmark name:

- `WatchSet*`: the code changed in response to the gate (see DESIGN.md).
- `Parallel*`, `Snapshot`, `LongestPrefix`: the gate and the summary exposed
  mistakes in the *benchmarks* -- setup on the clock; a 32-byte-allocation
  benchmark that was measuring the two fixtures' heap sizes; key formatting
  inside the timed loop.
- The seven write benchmarks on the 11-index schema at 100,000 rows: a 0.4 s
  window right after building such a database mostly measures the heap warming
  up (intervals of +/-40%); they were re-measured with 2 s windows.

Numbers from a quiet machine, from Linux and from x86-64 are welcome.

### Environment

```
goos: darwin
goarch: arm64
cpu: Apple M1 Max
go:     go version go1.27.1 darwin/arm64
upstream: github.com/hashicorp/go-memdb v1.3.5, github.com/hashicorp/go-immutable-radix v1.3.1
samples:  6-12 per benchmark and implementation
```

### The gate

```
benchgate: 190 benchmarks compared: 181 faster, 9 statistically equal, geomean speed-up 2.65x
benchgate: PASS -- no benchmark is slower, allocates more often, or uses more memory than upstream
```

### Summary by benchmark family

Speed-up is upstream's time divided by go-maemmidb's (median of the samples);
allocs/op and B/op are the change in the family's total.

| Benchmark family | cases | speed-up (geomean) | worst case | best case | allocs/op | B/op |
|---|---:|---:|---:|---:|---:|---:|
| BulkLoad | 4 | **3.42x** | 3.29x | 3.73x | -81% | -71% |
| Changes | 4 | **3.26x** | 2.42x | 4.39x | -83% | -68% |
| CommitNotify | 2 | **3.48x** | 3.20x | 3.78x | -83% | -72% |
| DeleteAbort | 4 | **2.82x** | 2.28x | 3.43x | -88% | -65% |
| DeleteAllAbort | 4 | **3.45x** | 2.45x | 4.64x | -90% | -73% |
| DeletePrefixAbort | 2 | **3.80x** | 3.54x | 4.08x | -93% | -79% |
| FilterIterator | 1 | **1.80x** | 1.80x | 1.80x | -75% | -42% |
| First | 20 | **2.18x** | 1.48x | 4.57x | -71% | -81% |
| FirstIndex | 12 | **2.32x** | 1.45x | 13.49x | -90% | -95% |
| FirstWatch | 2 | **1.83x** | 1.38x | 2.42x | -80% | -89% |
| GetOne | 2 | **1.85x** | 1.82x | 1.88x | -78% | -27% |
| GetWatchCh | 2 | **1.61x** | 1.45x | 1.78x | -73% | -27% |
| Indexer | 23 | **1.89x** | 1.00x | 3.39x | -62% | -52% |
| InsertAbort | 16 | **2.75x** | 1.25x | 4.04x | -79% | -64% |
| InsertDeleteCommit | 12 | **2.80x** | 1.53x | 4.34x | -88% | -68% |
| InsertDeleteCommitManyTables | 1 | **3.24x** | 3.24x | 3.24x | -88% | -63% |
| Iterate | 14 | **2.12x** | 1.14x | 4.60x | -82% | -59% |
| IterateInWriteTxn | 2 | **2.87x** | 2.73x | 3.02x | -82% | -63% |
| Last | 4 | **4.53x** | 1.64x | 23.04x | -94% | -98% |
| LongestPrefix | 2 | **1.22x** | 1.21x | 1.24x | -25% | -38% |
| LowerBound | 5 | **2.09x** | 1.76x | 2.52x | -85% | -65% |
| NewMemDB | 3 | **2.17x** | 1.13x | 3.42x | -83% | -72% |
| ParallelTxnFirst | 2 | **2.10x** | 1.86x | 2.37x | -71% | -68% |
| ParallelTxnFirstWithWriter | 1 | **2.17x** | 2.17x | 2.17x | -71% | -69% |
| ReadYourWrites | 2 | **3.12x** | 2.75x | 3.53x | -82% | -67% |
| Snapshot | 1 | **0.98x** | 0.98x | 0.98x | +0% | +0% |
| SnapshotWrite | 1 | **2.92x** | 2.92x | 2.92x | -85% | -64% |
| Txn | 4 | **1.70x** | 1.56x | 1.91x | -50% | -39% |
| TxnFirst | 2 | **2.19x** | 2.00x | 2.40x | -71% | -68% |
| TxnSnapshot | 1 | **2.36x** | 2.36x | 2.36x | -86% | -64% |
| UpdateCommit | 12 | **2.90x** | 1.67x | 4.64x | -88% | -68% |
| WatchCycle | 2 | **3.17x** | 2.43x | 4.13x | -87% | -70% |
| WatchSetAdd | 1 | **1.10x** | 1.10x | 1.10x | +0% | +0% |
| WatchSetBlockThenFire | 2 | **1.01x** | 1.00x | 1.03x | -4% | -3% |
| WatchSetWatchCtxCancelled | 6 | **13.66x** | 1.00x | 2375.37x | -100% | -100% |
| WatchSetWatchCtxFired | 6 | **1.31x** | 0.99x | 2.14x | -15% | -12% |
| WatchSetWatchExpired | 6 | **26.64x** | 1.79x | 3319.73x | -100% | -100% |
| **all** | 190 | **2.65x** | | | | |

### Memory

From `BenchmarkBulkLoad`: a database loaded in one transaction, then a forced
collection with only that database live.

| schema, 100,000 rows | | upstream | go-maemmidb | |
|---|---|---:|---:|---:|
| 3 indexes | heap bytes per row | 2,443 | 1,135 | **-54%** |
| | heap objects per row | 32.2 | 11.7 | **-64%** |
| | full GC cycle | 198 ms | 154 ms | -22% |
| 11 indexes | heap bytes per row | 6,530 | 2,970 | **-55%** |
| | heap objects per row | 86.5 | 31.5 | **-64%** |
| | full GC cycle | 627 ms | 445 ms | -29% |

### What "statistically equal" means here

Nine of the 190 benchmarks show no significant difference. They are the ones
where both implementations run the same code or are bound by the same runtime
machinery: `MemDB.Snapshot` (one 32-byte allocation in both), the boolean and
conditional indexer methods (upstream's code, unchanged), and `WatchSet` waits
on exactly 32 channels or on more than 128, where the time goes into the Go
runtime's `select`. Nothing is slower.

### Upstream's own benchmarks

The three benchmarks in go-memdb's test files, run in both checkouts:

```
goos: darwin
goarch: arm64
pkg: memdb
cpu: Apple M1 Max
                                 │ results/inpkg-upstream.txt │        results/inpkg-new.txt        │
                                 │           sec/op           │   sec/op     vs base                │
UUIDFieldIndex_parseString-10                   116.25n ± 20%   79.89n ± 8%  -31.28% (p=0.000 n=10)
CompoundMultiIndex_FromObject-10                 652.7n ±  1%   236.5n ± 0%  -63.76% (p=0.000 n=10)
Watch-10                                     106123.00n ±  5%   20.66n ± 2%  -99.98% (p=0.000 n=10)
geomean                                          2.004µ         73.09n       -96.35%
                                 │ results/inpkg-upstream.txt │          results/inpkg-new.txt           │
                                 │            B/op            │    B/op      vs base                     │
UUIDFieldIndex_parseString-10                      48.00 ± 0%    16.00 ± 0%   -66.67% (p=0.000 n=10)
CompoundMultiIndex_FromObject-10                   640.0 ± 0%    352.0 ± 0%   -45.00% (p=0.000 n=10)
Watch-10                                         10.97Ki ± 0%   0.00Ki ± 0%  -100.00% (p=0.000 n=10)
geomean                                            701.5                     ?                       ¹ ²
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
                               │            sec/op             │   sec/op     vs base               │
ParallelTxnFirst/size=1000                        294.3n ±  2%   127.2n ± 1%  -56.78% (p=0.002 n=6)
ParallelTxnFirst/size=1000-4                      93.22n ±  1%   39.54n ± 2%  -57.58% (p=0.002 n=6)
ParallelTxnFirst/size=1000-8                      82.52n ±  4%   32.29n ± 8%  -60.86% (p=0.002 n=6)
ParallelTxnFirst/size=100000                      879.2n ±  5%   443.2n ± 4%  -49.58% (p=0.002 n=6)
ParallelTxnFirst/size=100000-4                    276.9n ± 23%   142.4n ± 4%  -48.58% (p=0.002 n=6)
ParallelTxnFirst/size=100000-8                   149.65n ±  5%   76.98n ± 1%  -48.56% (p=0.002 n=6)
ParallelTxnFirstWithWriter                        900.1n ±  4%   453.2n ± 5%  -49.65% (p=0.002 n=6)
ParallelTxnFirstWithWriter-4                      274.8n ±  2%   140.8n ± 3%  -48.74% (p=0.002 n=6)
ParallelTxnFirstWithWriter-8                     148.70n ±  3%   77.70n ± 1%  -47.75% (p=0.002 n=6)
geomean                                           243.7n         116.4n       -52.25%
                               │ results/parallel-upstream.txt │     results/parallel-new.txt      │
```

### Full results

The raw samples are in [`benchmarks/results/upstream.txt`](benchmarks/results/upstream.txt) and
[`benchmarks/results/new.txt`](benchmarks/results/new.txt); the complete `benchstat` comparison
(time, bytes, allocations and the heap metrics of every benchmark) is in
[`benchmarks/results/benchstat.txt`](benchmarks/results/benchstat.txt).
