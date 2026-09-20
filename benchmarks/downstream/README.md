# Downstream benchmarks

Additional benchmarks for Consul, Nomad, Vault and SpiceDB, used in
[DOWNSTREAM.md](../../DOWNSTREAM.md). These cover operations beyond the
existing project benchmarks included in the comparison.

Each file uses its target project's package and types. The files are stored
under `testdata/` so Go excludes them from builds and tests in this module.

| File | Drop into | Fixture |
|---|---|---|
| `consul_catalog_bench_test.go` | consul `agent/consul/state/` | 1,000 nodes, 50 services, 3 instances per node |
| `nomad_state_bench_test.go` | nomad `nomad/state/` | 500 nodes, 100 jobs, 5,000 allocations |
| `vault_identity_bench_test.go` | vault `vault/` | 5,000 entities with aliases, 200 groups of 50 |
| `spicedb_memdb_bench_test.go` | spicedb `internal/datastore/memdb/` | 2,000 documents, 6,000 relationships |

To run a comparison, check out the project at the revision listed in
[DOWNSTREAM.md](../../DOWNSTREAM.md) and copy its benchmark file into the
directory above. Use the same benchmark file for both database implementations.
Follow the [reproduction steps](../../DOWNSTREAM.md#reproducing) to set up the
module-path alias, select the implementation and collect samples.

Docker's and SwarmKit's existing benchmarks are in
`daemon/container/view_test.go` and `manager/state/store/memory_test.go` in
their respective repositories. Consul's existing `BenchmarkGetNodes` and
`BenchmarkCheckServiceNodes` are also included in the comparison. These
benchmarks are run unmodified.
