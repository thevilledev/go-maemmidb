# Cross-project benchmarks

These benchmarks compare go-maemmidb with
[hashicorp/go-memdb](https://github.com/hashicorp/go-memdb) in six projects that
already use go-memdb. They measure each project's database operations and
complement the [library benchmarks](benchmarks.md).

The comparisons use unchanged application source and existing tests and
benchmarks. Dependency configuration selects the database implementation;
additional benchmark files cover operations without existing benchmarks.

## What was measured

| Project | Version | go-memdb pinned | Package under test |
|---|---|---|---|
| [moby/moby](https://github.com/moby/moby) (Docker Engine) | `5c47efc` | v1.3.5 | `daemon/container` (the container ViewDB) |
| [moby/swarmkit](https://github.com/moby/swarmkit) | `1fd637b` | **v1.3.2** | `manager/state/store` |
| [hashicorp/consul](https://github.com/hashicorp/consul) | v1.21.5 | v1.3.4 | `agent/consul/state` |
| [hashicorp/nomad](https://github.com/hashicorp/nomad) | v1.10.5 | v1.3.5 | `nomad/state` |
| [hashicorp/vault](https://github.com/hashicorp/vault) | v1.20.4 | v1.3.4 | `vault` (identity store) |
| [authzed/spicedb](https://github.com/authzed/spicedb) | `f7620a5` | v1.3.5 | `internal/datastore/memdb` |

The projects pin go-memdb v1.3.2, v1.3.4 or v1.3.5. Each was checked with
go-maemmidb as a replacement, with the test limitations described below.

## Method

Each project is built twice: once with its pinned go-memdb version and once
with go-maemmidb. A local copy of go-maemmidb uses the go-memdb module path so
the project's imports, including indirect dependencies, resolve to the
replacement. `go list -deps` confirms the implementation used in each build.
The replacement also passes its own test suite under the aliased module path.
See [Reproducing](#reproducing) for setup.

The runs follow the method in
[`scripts/bench-compare.sh`](../scripts/bench-compare.sh):

1. Build a pair of test binaries per round with `-ldflags=-randlayout=N`,
   changing `N` each round to vary function layout.
2. Run each benchmark family (a top-level `Benchmark` function) in a separate
   process and discard its first pass as a warm-up.
3. Alternate implementations and reverse their order each round.
4. Compare the collected samples with `benchstat`.

Varying function layout matters: using a single binary per implementation
changes several results by a few percent.

Docker's four memdb benchmarks and SwarmKit's eight benchmarks are from those
projects. Consul also provides `BenchmarkGetNodes` and
`BenchmarkCheckServiceNodes`. All are run unmodified. The remaining benchmarks
were added here; their source is in
[`benchmarks/downstream/`](../benchmarks/downstream). Fixture sizes are listed
with each project's results.

## Compatibility

All six projects build with go-maemmidb, with Docker's Linux requirement noted
below. Tests cover the following packages and test groups:

| Package | Tests | Result |
|---|---:|---|
| consul `agent/consul/state` | 298 | pass |
| consul `agent/{blockingquery,configentry}`, `internal/storage/inmem`, `agent/consul/{watch,autopilotevents,servercert,auth,fsm}`, `agent/proxycfg-glue`, `agent/rpc/peering`, `agent/grpc-external/services/...` | — | pass |
| nomad `nomad/state` (+ `indexer`, `paginator`) | 288 | pass |
| nomad `scheduler` | 374 | pass |
| nomad `nomad/{deploymentwatcher,volumewatcher}`, `lib/auth`, `helper/raftutil` | — | pass |
| vault `vault` — `TestIdentityStore*` | 70 | pass |
| vault `vault/quotas`, `command/agentproxyshared/cache/cachememdb` | 19 | pass |
| swarmkit `manager/state/store` | — | pass |
| spicedb `internal/datastore/memdb` | — | pass |
| consul `agent/consul` (server, RPC, raft) | 576 | flaky — see below |

Consul's `agent/consul` tests had intermittent failures with both
implementations. After a failure with go-maemmidb, three control runs with
go-memdb failed twice. Failures involved raft leadership, WAN gossip and expiry
timing in `TestAutopilot_MinQuorum`, `TestServer_JoinWAN_viaMeshGateway`,
`TestACLEndpoint_TokenList`, `TestCatalog_ListServices_Stale`,
`TestServer_TLSForceOutgoingToNoTLS` and `TestRPC_RPCMaxConnsPerClient`.
The two tests that failed with go-maemmidb each passed all three isolated runs
with both implementations. These results do not establish a clean pass for
the full package.

Docker's `daemon/container` depends on Linux-only packages and cannot be built
or tested on macOS. `GOOS=linux GOARCH=amd64 go build ./daemon/...` succeeds
with go-maemmidb. Its benchmarks were run on Linux.

## Results

Measurements use an Apple M1 Max and Go 1.27.1 unless noted. `n` is the number
of samples per benchmark and implementation. Times are per operation;
percentage changes are relative to go-memdb, so negative values mean less
time or fewer allocations. `allocs` reports the change in allocations per
operation, and `geomean` is the geometric mean across the listed benchmarks.

All memdb benchmarks shown take less time with go-maemmidb (`benchstat`
reports `p=0.000`), and none allocates more. Docker's benchmarks that do not
use memdb show no statistically significant change.

### moby / Docker Engine — `daemon/container`

AMD Ryzen AI 9 HX PRO 370 (Zen 5), Linux, `linux/amd64`, n=12. These are
Docker's existing benchmarks. `DBGetByPrefix` measures container ID prefix
lookups used by the CLI.

| Benchmark | go-memdb | go-maemmidb | |
|---|---:|---:|---:|
| `DBAdd100` | 243.3 µs | 66.3 µs | **-72.8%** |
| `DBGetByPrefix500` | 134.6 µs | 80.8 µs | -40.0% |
| `DBGetByPrefix250` | 65.4 µs | 40.2 µs | -38.5% |
| `DBGetByPrefix100` | 25.2 µs | 15.5 µs | -38.3% |
| geomean of the four | | | **-50%** |

The same test binary includes `BenchmarkReplaceOrAppendEnvValues`, which does
not use memdb and serves as a control. `~` indicates no statistically
significant change:

| | go-memdb | go-maemmidb | |
|---|---:|---:|---|
| `ReplaceOrAppendEnvValues/0` | 59.55 ns | 59.69 ns | ~ (p=0.443) |
| `ReplaceOrAppendEnvValues/100` | 1.082 µs | 1.077 µs | ~ (p=0.681) |
| `ReplaceOrAppendEnvValues/1000` | 10.31 µs | 10.26 µs | ~ (p=0.932) |
| `ReplaceOrAppendEnvValues/10000` | 102.5 µs | 102.2 µs | ~ (p=0.713) |

### moby/swarmkit — `manager/state/store`

n=20. SwarmKit's eight existing benchmarks, using its pinned go-memdb v1.3.2
as the baseline.

| Benchmark | go-memdb v1.3.2 | go-maemmidb | |
|---|---:|---:|---:|
| `UpdateNode` | 5.307 µs | 1.375 µs | **-74.1%** |
| `CreateNode` | 5.782 µs | 1.772 µs | **-69.4%** |
| `DeleteNodeTransaction` | 5.522 µs | 2.379 µs | -56.9% |
| `UpdateNodeTransaction` | 12.620 µs | 5.595 µs | -55.7% |
| `NodeConcurrency` (5 readers, 5 writers) | 65.13 µs | 29.61 µs | -54.5% |
| `GetNode` | 526.9 ns | 354.6 ns | -32.7% |
| `FindNodeByName` | 264.8 ns | 212.0 ns | -19.9% |
| `FindAllNodes` | 3.428 ms | 2.814 ms | -17.9% |
| **geomean** | 9.505 µs | 4.607 µs | **-51.5%** |

### HashiCorp Vault — `vault` identity store

n=12. The fixture contains 5,000 entities, each with an alias, spread across
four auth mounts, plus 200 groups of 50 members. `AliasByFactors` looks up an
alias during login; `GroupsByMemberEntityID` finds group memberships for
policy resolution.

| Benchmark | go-memdb | go-maemmidb | | allocs |
|---|---:|---:|---:|---:|
| `UpsertEntity` | 13.26 µs | 3.58 µs | **-73.0%** | -85.3% |
| `UpsertGroup` (50-entry multi-value index) | 83.28 µs | 25.01 µs | **-70.0%** | -75.0% |
| `EntityByName` | 455.9 ns | 221.0 ns | -51.5% | -73.3% |
| `LoginLookup` (alias -> entity -> groups) | 1248 ns | 608.8 ns | -51.2% | -68.6% |
| `EntityByAliasID` | 534.0 ns | 264.8 ns | -50.4% | -69.2% |
| `EntityByID` | 307.7 ns | 165.3 ns | -46.3% | -62.5% |
| `AliasByFactors` | 534.1 ns | 299.4 ns | -44.0% | -68.8% |
| `GroupsByMemberEntityID` | 387.5 ns | 223.8 ns | -42.3% | -61.5% |
| **geomean** | 1.466 µs | 658.4 ns | **-55.1%** | **-71.6%** |

### HashiCorp Nomad — `nomad/state`

n=12. The fixture contains 500 client nodes, 100 jobs and 5,000 allocations.
`AllocsByNode` looks up a client's allocations; `NodeHeartbeat` updates a
node's state.

| Benchmark | go-memdb | go-maemmidb | | allocs |
|---|---:|---:|---:|---:|
| `JobByID` | 320.9 ns | 105.9 ns | **-67.0%** | -78.6% |
| `UpsertAllocUpdate` | 32.52 µs | 12.49 µs | **-61.6%** | -72.3% |
| `WatchFire` (long-poll plus the write that wakes it) | 33.30 µs | 12.96 µs | -61.1% | -71.4% |
| `NodeHeartbeat` | 15.38 µs | 6.28 µs | -59.1% | -76.0% |
| `AllocsByJob` | 850.6 ns | 368.6 ns | -56.7% | -75.0% |
| `AllocsByNode` | 498.4 ns | 226.4 ns | -54.6% | -60.0% |
| `AllocsByNodeBlocking` | 515.5 ns | 238.6 ns | -53.7% | -60.0% |
| `NodeByID` | 311.3 ns | 204.2 ns | -34.4% | -71.4% |
| `ListNodes` | 3.822 µs | 3.440 µs | -10.0% | -77.8% |
| **geomean** | 2.235 µs | 1.047 µs | **-53.2%** | **-72.1%** |

### authzed/spicedb — `internal/datastore/memdb`

n=20. The fixture contains 2,000 documents with three viewer relationships
each, drawn from 50 users. SpiceDB's memdb datastore is intended for
development, so these results apply to that use.

| Benchmark | go-memdb | go-maemmidb | | allocs |
|---|---:|---:|---:|---:|
| `WriteRelationships` (batch of 10) | 119.94 µs | 59.43 µs | **-50.5%** | -80.8% |
| `TouchExisting` | 10.112 µs | 6.320 µs | -37.5% | -65.4% |
| `QueryByResourceAndRelation` | 123.28 µs | 99.18 µs | -19.5% | -58.6% |
| `QueryNamespaceScan` (all 6,000) | 245.3 µs | 197.4 µs | -19.5% | -52.6% |
| `QueryByResource` (permission check) | 120.11 µs | 99.63 µs | -17.1% | -47.6% |
| `ReverseQueryBySubject` | 148.0 µs | 125.0 µs | -15.5% | -45.5% |
| **geomean** | 93.11 µs | 67.14 µs | **-27.9%** | **-60.6%** |

### HashiCorp Consul — `agent/consul/state`

n=12. The added catalog benchmarks use 1,000 nodes, 50 services, three service
instances per node, and one Serf health check per node plus one per instance.
`CatalogCheckServiceNodes` measures the state-store query used for service
discovery. `GetNodes` and `CheckServiceNodes` are Consul's existing benchmarks
and use their own fixtures.

| Benchmark | go-memdb | go-maemmidb | | allocs |
|---|---:|---:|---:|---:|
| `CatalogNodeChurn` (register then deregister a node) | 349.1 µs | 144.7 µs | **-58.6%** | -63.6% |
| `CatalogWatchFire` (blocking query plus the write that wakes it) | 137.5 µs | 92.1 µs | -33.0% | -33.1% |
| `GetNodes` (Consul's own) | 485.8 ns | 374.9 ns | -22.8% | — |
| `CatalogCheckServiceNodesBlocking` | 93.79 µs | 72.86 µs | -22.3% | -20.7% |
| `CatalogCheckServiceNodes` | 93.76 µs | 73.14 µs | -22.0% | -20.7% |
| `CatalogListNodes` | 11.73 µs | 9.16 µs | -21.9% | -26.9% |
| `CatalogServiceNodes` | 36.52 µs | 30.26 µs | -17.1% | -1.2% |
| `CheckServiceNodes` (Consul's own) | 2.046 µs | 1.701 µs | -16.9% | — |
| `CatalogNodeServices` | 1.961 µs | 1.643 µs | -16.2% | -17.6% |
| `CatalogCheckUpdate` (health check flap) | 1.930 µs | 1.665 µs | -13.7% | -10.0% |
| `CatalogNodesByMeta` | 1.290 µs | 1.123 µs | -13.0% | -26.1% |
| `CatalogServiceList` | 119.1 µs | 104.9 µs | -11.9% | -25.9% |
| **geomean** | 14.73 µs | 11.24 µs | **-23.7%** | **-26.7%** |

### Summary

| Project | geomean sec/op | Benchmarks |
|---|---:|---|
| hashicorp/vault | **-55.1%** | written here |
| hashicorp/nomad | **-53.2%** | written here |
| moby/swarmkit | **-51.5%** | the project's own |
| moby/moby (memdb subset) | **~-50%** | the project's own |
| authzed/spicedb | **-27.9%** | written here |
| hashicorp/consul | **-23.7%** | 2 of the project's own, 10 here |

## Interpreting the results

The [library benchmarks](benchmarks.md) report a 2.74x
geometric mean speedup on the M1 Max. The per-project averages here are
smaller because each operation also includes work in the calling project.
For example, `CatalogCheckServiceNodes` assembles service and health-check
results as well as querying memdb. Its total time falls from 93.76 µs to
73.14 µs, a 22% reduction.

Writes show the largest reductions in these results. Changes to watch-channel
allocation and writable-node tracking help explain the write improvements;
the [design notes](design.md) describe those changes. These are measurements of
the listed database operations, not of whole applications or requests.

## Without `unsafe`

`-tags memdb_safe` builds go-maemmidb without `package unsafe`; see
[compatibility notes](compatibility.md#build-tags). The following runs alternate
between go-memdb, the default go-maemmidb build and the safe build, using the
method above. The first column of results compares the safe build with
go-memdb; the second shows its added time relative to the default build.

| | geomean vs go-memdb | cost of dropping `unsafe` |
|---|---:|---:|
| nomad `nomad/state` (n=20) | -52.8% | +0.92% |
| consul `agent/consul/state` (n=20) | -20.9% | +2.33% |
| vault `vault` identity (n=12) | -53.6% | +4.10% |

The safe build retains most of the measured improvement. Its largest
individual slowdowns relative to the default build are:

- Vault: `VaultUpsertGroup` takes 24.3% more time and `VaultUpsertEntity` takes
  10.7% more. The safe build uses reflection for field access and adds an
  allocation for each inserted key. The group update writes a 50-entry index
  and allocates 69% more objects.
- Consul: `CatalogListNodes` takes 10.0% more time and `CatalogServiceList`
  takes 5.4% more. These scans use the safe build's slice-based tree nodes,
  with no change in allocations.

In these runs, extra allocations occur only when inserting new keys. Nomad's
writes update existing keys and show no allocation change, as do all read
benchmarks in the three projects. A few safe-build benchmarks take less time,
including `CatalogNodesByMeta` at -3.4%.

## Reproducing

1. Check out the project at the revision listed above. Keep separate copies
   for the go-memdb baseline and the go-maemmidb comparison.
2. For Consul, Nomad, Vault and SpiceDB, copy the additional benchmark file
   into both checkouts. The destination packages are listed in
   the [downstream benchmark guide](downstream-benchmarks.md).
3. Make a separate copy of go-maemmidb. In that copy, set the `go.mod` module
   path to `github.com/hashicorp/go-memdb` and change its own `internal/...`
   imports to use that prefix. Run its tests under the new module path.
4. In the comparison checkout, point go-memdb at that local copy. Replace
   `/absolute/path/to/aliased-go-maemmidb` in the command below with its path.
   Docker and SwarmKit also require `go mod vendor` to update their vendored
   source.

   ```sh
   go mod edit -replace github.com/hashicorp/go-memdb=/absolute/path/to/aliased-go-maemmidb
   go mod vendor # Docker and SwarmKit only
   ```

5. Confirm dependency resolution with `go list -deps`, then build and run the
   project's tests and benchmarks using the [method above](#method).

[`scripts/bench-compare.sh`](../scripts/bench-compare.sh) implements this method
for this repository's library benchmarks. Use its build and sampling procedure
with the downstream package paths to reproduce these comparisons.
