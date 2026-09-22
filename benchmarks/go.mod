module github.com/thevilledev/go-maemmidb/benchmarks

go 1.26.0

require github.com/hashicorp/go-memdb v1.3.5

require github.com/thevilledev/go-juuri v0.1.0 // indirect

require (
	github.com/aclements/go-moremath v0.0.0-20210112150236-f10218a38794 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/thevilledev/go-maemmidb v0.0.0
	golang.org/x/perf v0.0.0-20260908200009-22c9c6c9d4da
)

tool golang.org/x/perf/cmd/benchstat

replace github.com/thevilledev/go-maemmidb => ../
