[Run the example in the Go Playground →](https://go.dev/play/p/YjRP5-ZN22A)

# go-maemmidb

A fast, dependency-free in-memory database for Go. A drop-in replacement for
[hashicorp/go-memdb](https://github.com/hashicorp/go-memdb), with the same
package name and API.

- Atomic transactions and snapshot isolation.
- Secondary indexes, range queries and watch channels.
- Typed queries and bitmap indexes for set operations.

## Install

Requires Go 1.23 or later.

```sh
go get github.com/thevilledev/go-maemmidb
```

```go
import memdb "github.com/thevilledev/go-maemmidb"
```

Coming from go-memdb? Change the import path and review the
[compatibility notes](docs/compatibility.md).

## Documentation

[Getting started](docs/getting-started.md) ·
[API reference](https://pkg.go.dev/github.com/thevilledev/go-maemmidb) ·
[Guides](docs/README.md)

See [benchmarks](docs/benchmarks.md) and [downstream results](docs/downstream.md)
for performance comparisons with go-memdb.

## License

[MPL-2.0](LICENSE). Derived from go-memdb; see [NOTICE](NOTICE) for attribution.
An independent project, unaffiliated with HashiCorp or IBM.

Named after [mämmi](https://en.wikipedia.org/wiki/M%C3%A4mmi), a Finnish delicacy.
