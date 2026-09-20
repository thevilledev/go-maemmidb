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

Measured on 2026-09-20 on an Apple M1 Max (macOS, Go 1.27.1): three rounds of
two samples per benchmark and implementation, 0.5 s windows, one discarded
warm-up pass per process, a different function layout every round. The machine
was a desktop carrying its ordinary background load -- which is why the runs
are interleaved, and why absolute times are on the slow side; the ratios are
what to read. (The library was built through the script's `BASE` mode, and the
run interleaved a third side, a development version of this package, which is
not part of this report.)

The run is published unedited: no benchmark was re-measured and spliced in.
The first published run of this suite, made before the script discarded a
warm-up pass, had to splice in re-measurements with 2 s windows for the seven
write benchmarks of the 11-index schema, and 51 of its 190 benchmarks had a
`benchstat` interval wider than 15% on one side or the other, against 24 now.
Those numbers were not wrong in direction -- the artefact penalised this
package more than upstream -- but they were noisier than the code deserves.

Numbers from a quiet machine, from Linux and from x86-64 are welcome.

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
benchgate: 190 benchmarks compared: 182 faster, 8 statistically equal, geomean speed-up 2.58x
benchgate: PASS -- no benchmark is slower, allocates more often, or uses more memory than upstream
```

### Summary by benchmark family

Speed-up is upstream's time divided by go-maemmidb's (median of the samples);
allocs/op and B/op are the change in the family's total.

| Benchmark family | cases | speed-up (geomean) | worst case | best case | allocs/op | B/op |
|---|---:|---:|---:|---:|---:|---:|
| BulkLoad | 4 | **3.51x** | 3.32x | 3.91x | -81% | -71% |
| Changes | 4 | **2.94x** | 2.64x | 3.27x | -83% | -68% |
| CommitNotify | 2 | **3.43x** | 3.12x | 3.78x | -83% | -72% |
| DeleteAbort | 4 | **2.89x** | 2.55x | 3.29x | -88% | -65% |
| DeleteAllAbort | 4 | **2.77x** | 1.32x | 4.12x | -90% | -73% |
| DeletePrefixAbort | 2 | **3.76x** | 3.41x | 4.15x | -93% | -79% |
| FilterIterator | 1 | **1.73x** | 1.73x | 1.73x | -75% | -42% |
| First | 20 | **2.19x** | 1.52x | 3.68x | -71% | -81% |
| FirstIndex | 12 | **2.51x** | 1.55x | 12.95x | -90% | -95% |
| FirstWatch | 2 | **1.89x** | 1.49x | 2.40x | -80% | -91% |
| GetOne | 2 | **1.80x** | 1.70x | 1.91x | -78% | -27% |
| GetWatchCh | 2 | **1.78x** | 1.77x | 1.80x | -73% | -27% |
| Indexer | 23 | **1.90x** | 1.00x | 3.44x | -62% | -52% |
| InsertAbort | 16 | **2.44x** | 1.05x | 4.05x | -79% | -64% |
| InsertDeleteCommit | 12 | **2.42x** | 1.37x | 4.31x | -88% | -68% |
| InsertDeleteCommitManyTables | 1 | **3.20x** | 3.20x | 3.20x | -88% | -63% |
| Iterate | 14 | **1.99x** | 1.19x | 3.74x | -82% | -59% |
| IterateInWriteTxn | 2 | **2.69x** | 2.44x | 2.98x | -82% | -63% |
| Last | 4 | **5.18x** | 1.59x | 24.09x | -94% | -98% |
| LongestPrefix | 2 | **1.24x** | 1.22x | 1.26x | -25% | -38% |
| LowerBound | 5 | **1.75x** | 1.11x | 2.43x | -85% | -65% |
| NewMemDB | 3 | **2.13x** | 1.11x | 3.39x | -83% | -72% |
| ParallelTxnFirst | 2 | **2.15x** | 1.98x | 2.33x | -71% | -68% |
| ParallelTxnFirstWithWriter | 1 | **1.99x** | 1.99x | 1.99x | -71% | -68% |
| ReadYourWrites | 2 | **3.31x** | 3.16x | 3.47x | -82% | -67% |
| Snapshot | 1 | **1.00x** | 1.00x | 1.00x | +0% | +0% |
| SnapshotWrite | 1 | **2.69x** | 2.69x | 2.69x | -85% | -64% |
| Txn | 4 | **1.82x** | 1.55x | 2.28x | -50% | -39% |
| TxnFirst | 2 | **2.19x** | 2.08x | 2.29x | -71% | -68% |
| TxnSnapshot | 1 | **2.72x** | 2.72x | 2.72x | -86% | -64% |
| UpdateCommit | 12 | **2.67x** | 1.12x | 4.22x | -88% | -67% |
| WatchCycle | 2 | **3.63x** | 3.30x | 3.99x | -87% | -70% |
| WatchSetAdd | 1 | **1.11x** | 1.11x | 1.11x | +0% | +0% |
| WatchSetBlockThenFire | 2 | **1.02x** | 1.00x | 1.03x | -4% | -3% |
| WatchSetWatchCtxCancelled | 6 | **13.56x** | 1.00x | 2354.45x | -100% | -100% |
| WatchSetWatchCtxFired | 6 | **1.31x** | 1.01x | 2.10x | -15% | -12% |
| WatchSetWatchExpired | 6 | **25.78x** | 1.75x | 3231.58x | -100% | -100% |
| **all** | 190 | **2.58x** | | | | |

### Memory

From `BenchmarkBulkLoad`: a database loaded in one transaction, then a forced
collection with only that database live.

| schema, 100,000 rows | | upstream | go-maemmidb | |
|---|---|---:|---:|---:|
| 3 indexes | heap bytes per row | 2,443 | 1,135 | **-54%** |
| | heap objects per row | 32.2 | 11.7 | **-64%** |
| | full GC cycle | 202 ms | 163 ms | -19% |
| 11 indexes | heap bytes per row | 6,529 | 2,970 | **-55%** |
| | heap objects per row | 86.4 | 31.5 | **-64%** |
| | full GC cycle | 636 ms | 434 ms | -32% |

### What "statistically equal" means here

Eight of the 190 benchmarks show no significant difference. They are the ones
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
                                 │ /private/tmp/claude-501/-Users-ville-git-go-maemmidb/0e12e609-c450-4b30-a6bb-d35fefbbb19b/scratchpad/commit/results-main/inpkg-upstream.txt │ /private/tmp/claude-501/-Users-ville-git-go-maemmidb/0e12e609-c450-4b30-a6bb-d35fefbbb19b/scratchpad/commit/results-main/inpkg-new.txt │
                                 │                                                                   sec/op                                                                    │                                                     sec/op                                                      vs base                │
UUIDFieldIndex_parseString-10                                                                                                                                    116.25n ± 20%                                                                                                      79.89n ± 8%  -31.28% (p=0.000 n=10)
CompoundMultiIndex_FromObject-10                                                                                                                                  652.7n ±  1%                                                                                                      236.5n ± 0%  -63.76% (p=0.000 n=10)
Watch-10                                                                                                                                                      106123.00n ±  5%                                                                                                      20.66n ± 2%  -99.98% (p=0.000 n=10)
geomean                                                                                                                                                           2.004µ                                                                                                            73.09n       -96.35%
                                 │ /private/tmp/claude-501/-Users-ville-git-go-maemmidb/0e12e609-c450-4b30-a6bb-d35fefbbb19b/scratchpad/commit/results-main/inpkg-upstream.txt │ /private/tmp/claude-501/-Users-ville-git-go-maemmidb/0e12e609-c450-4b30-a6bb-d35fefbbb19b/scratchpad/commit/results-main/inpkg-new.txt │
                                 │                                                                    B/op                                                                     │                                                   B/op                                                     vs base                     │
UUIDFieldIndex_parseString-10                                                                                                                                       48.00 ± 0%                                                                                                  16.00 ± 0%   -66.67% (p=0.000 n=10)
CompoundMultiIndex_FromObject-10                                                                                                                                    640.0 ± 0%                                                                                                  352.0 ± 0%   -45.00% (p=0.000 n=10)
Watch-10                                                                                                                                                          10.97Ki ± 0%                                                                                                 0.00Ki ± 0%  -100.00% (p=0.000 n=10)
geomean                                                                                                                                                             701.5                                                                                                                   ?                       ¹ ²
¹ summaries must be >0 to compute geomean
² ratios must be >0 to compute geomean
                                 │ /private/tmp/claude-501/-Users-ville-git-go-maemmidb/0e12e609-c450-4b30-a6bb-d35fefbbb19b/scratchpad/commit/results-main/inpkg-upstream.txt │ /private/tmp/claude-501/-Users-ville-git-go-maemmidb/0e12e609-c450-4b30-a6bb-d35fefbbb19b/scratchpad/commit/results-main/inpkg-new.txt │
                                 │                                                                  allocs/op                                                                  │                                                 allocs/op                                                  vs base                     │
UUIDFieldIndex_parseString-10                                                                                                                                       2.000 ± 0%                                                                                                  1.000 ± 0%   -50.00% (p=0.000 n=10)
CompoundMultiIndex_FromObject-10                                                                                                                                   28.000 ± 0%                                                                                                  5.000 ± 0%   -82.14% (p=0.000 n=10)
Watch-10                                                                                                                                                            79.00 ± 0%                                                                                                   0.00 ± 0%  -100.00% (p=0.000 n=10)
geomean                                                                                                                                                             16.42                                                                                                                   ?                       ¹ ²
¹ summaries must be >0 to compute geomean
² ratios must be >0 to compute geomean
```

### Parallel readers

```
                               │                                                                     sec/op                                                                     │                                                       sec/op                                                        vs base               │
ParallelTxnFirst/size=1000                                                                                                                                          295.8n ± 6%                                                                                                         125.9n ±  3%  -57.45% (p=0.002 n=6)
ParallelTxnFirst/size=1000-4                                                                                                                                        90.38n ± 3%                                                                                                         39.59n ±  5%  -56.19% (p=0.002 n=6)
ParallelTxnFirst/size=1000-8                                                                                                                                        81.78n ± 2%                                                                                                         32.39n ±  8%  -60.39% (p=0.002 n=6)
ParallelTxnFirst/size=100000                                                                                                                                        940.2n ± 5%                                                                                                         452.8n ± 11%  -51.85% (p=0.002 n=6)
ParallelTxnFirst/size=100000-4                                                                                                                                      282.1n ± 3%                                                                                                         146.5n ±  3%  -48.06% (p=0.002 n=6)
ParallelTxnFirst/size=100000-8                                                                                                                                     153.90n ± 3%                                                                                                         79.25n ±  3%  -48.50% (p=0.002 n=6)
ParallelTxnFirstWithWriter                                                                                                                                          910.2n ± 3%                                                                                                         450.2n ± 14%  -50.55% (p=0.002 n=6)
ParallelTxnFirstWithWriter-4                                                                                                                                        290.0n ± 5%                                                                                                         148.2n ±  7%  -48.91% (p=0.002 n=6)
ParallelTxnFirstWithWriter-8                                                                                                                                       156.55n ± 3%                                                                                                         79.00n ±  2%  -49.54% (p=0.002 n=6)
geomean                                                                                                                                                             249.1n                                                                                                              118.1n        -52.58%
                               │ /private/tmp/claude-501/-Users-ville-git-go-maemmidb/0e12e609-c450-4b30-a6bb-d35fefbbb19b/scratchpad/commit/results-main/parallel-upstream.txt │ /private/tmp/claude-501/-Users-ville-git-go-maemmidb/0e12e609-c450-4b30-a6bb-d35fefbbb19b/scratchpad/commit/results-main/parallel-new.txt │
```

### Full results

The raw samples are in [`benchmarks/results/upstream.txt`](benchmarks/results/upstream.txt) and
[`benchmarks/results/new.txt`](benchmarks/results/new.txt); the complete `benchstat` comparison
(time, bytes, allocations and the heap metrics of every benchmark) is in
[`benchmarks/results/benchstat.txt`](benchmarks/results/benchstat.txt).
