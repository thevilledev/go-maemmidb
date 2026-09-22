# Getting started

[Run this example in the Go Playground](https://go.dev/play/p/YjRP5-ZN22A),
or run it locally with Go 1.23 or later:

```sh
mkdir hello-maemmidb
cd hello-maemmidb
go mod init example.com/hello-maemmidb
go get github.com/thevilledev/go-maemmidb@v0.1.0
```

Save the following as `main.go`, then run `go run .`:

```go
package main

import (
	"fmt"

	memdb "github.com/thevilledev/go-maemmidb"
)

type Person struct {
	Email string
	Name  string
}

func main() {
	db, err := memdb.NewMemDB(&memdb.DBSchema{
		Tables: map[string]*memdb.TableSchema{
			"people": {
				Name: "people",
				Indexes: map[string]*memdb.IndexSchema{
					"id": {
						Name:    "id",
						Unique:  true,
						Indexer: &memdb.StringFieldIndex{Field: "Email"},
					},
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}

	write := db.Txn(true)
	defer write.Abort()
	if err := write.Insert("people", &Person{Email: "ada@example.com", Name: "Ada"}); err != nil {
		panic(err)
	}
	write.Commit()

	read := db.Txn(false)
	defer read.Abort()
	row, err := read.First("people", "id", "ada@example.com")
	if err != nil {
		panic(err)
	}
	fmt.Printf("Hello, %s!\n", row.(*Person).Name)
}
```

Output:

```text
Hello, Ada!
```

Each table needs a unique `id` index. Write transactions commit their changes
atomically; read transactions see a consistent snapshot. Treat inserted
objects as immutable and insert a replacement to update a row.

For range queries and watch channels, see the
[tested examples](../example_test.go). For typed queries and bitmap indexes,
see [extensions](extensions.md).

## Migrating from go-memdb

Install go-maemmidb and replace the import path:

```go
import memdb "github.com/thevilledev/go-maemmidb"
```

The package is still named `memdb`, so existing call sites keep their names.
Run `go mod tidy` and your tests after switching. Review the
[compatibility notes](compatibility.md#deliberate-differences) for deliberate
behaviour differences.
