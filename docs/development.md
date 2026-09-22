# Development

Run these commands from the repository root:

```sh
make check          # formatting, vet, license headers, upstream checksums,
                    # tests, race detector, safe builds and differential tests
make fuzz           # radix tree, bitmap and database differential fuzzers
make bench-compare  # interleaved comparison against go-memdb, then benchstat
make bench-gate     # check the performance and allocation thresholds
make bench-self BASE=origin/main
make bench-self-gate
```

The [`benchmarks/`](../benchmarks) directory is a separate Go module, so the
original implementations are never a dependency of the main module.
See [benchmarks](benchmarks.md) for the methodology and configuration.

## Build tags

By default, indexed struct fields and radix tree children are accessed through
cached offsets using `unsafe`. Build with `-tags memdb_safe` or `-tags purego`
for a version that uses reflection and ordinary slices instead. Both variants
have the same behaviour and are tested in CI.

```sh
go test -tags memdb_safe ./...
go test -tags purego ./...
```
