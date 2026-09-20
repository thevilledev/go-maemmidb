// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb_test

import (
	"fmt"

	memdb "github.com/thevilledev/go-maemmidb"
)

// Alloc is the row type of the examples of go-maemmidb's extensions.
type Alloc struct {
	ID     string
	Node   string
	Status string
	Tags   []string
}

func allocSchema() *memdb.DBSchema {
	return &memdb.DBSchema{Tables: map[string]*memdb.TableSchema{
		"alloc": {Name: "alloc", Indexes: map[string]*memdb.IndexSchema{
			// An indexer built from a function: no reflection, same keys as
			// &memdb.StringFieldIndex{Field: "ID"}.
			"id": {Name: "id", Unique: true, Indexer: &memdb.StringIndex[Alloc]{Get: func(a *Alloc) string { return a.ID }}},
			// Bitmap indexes: value -> set of rows.
			"node":   {Name: "node", Indexer: &memdb.BitmapIndex{Indexer: &memdb.StringFieldIndex{Field: "Node"}}},
			"status": {Name: "status", Indexer: &memdb.BitmapIndex{Indexer: &memdb.StringFieldIndex{Field: "Status"}}},
			"tags": {Name: "tags", AllowMissing: true,
				Indexer: &memdb.BitmapIndex{Indexer: &memdb.StringSliceFieldIndex{Field: "Tags"}}},
		}},
	}}
}

func allocDB() *memdb.MemDB {
	db, err := memdb.NewMemDB(allocSchema())
	if err != nil {
		panic(err)
	}
	txn := db.Txn(true)
	for _, a := range []*Alloc{
		{ID: "a1", Node: "n1", Status: "running", Tags: []string{"batch"}},
		{ID: "a2", Node: "n1", Status: "running"},
		{ID: "a3", Node: "n1", Status: "failed", Tags: []string{"batch", "gpu"}},
		{ID: "a4", Node: "n2", Status: "running", Tags: []string{"gpu"}},
		{ID: "a5", Node: "n2", Status: "pending"},
	} {
		if err := txn.Insert("alloc", a); err != nil {
			panic(err)
		}
	}
	txn.Commit()
	return db
}

// Sets of rows from bitmap indexes are combined with And, Or and AndNot, and
// know their size without visiting a row.
func ExampleTxn_Where() {
	db := allocDB()
	txn := db.Txn(false)

	running, err := txn.Where("alloc", "status", "running")
	if err != nil {
		panic(err)
	}
	onN1, _ := txn.Where("alloc", "node", "n1")
	batch, _ := txn.Where("alloc", "tags", "batch")
	all, _ := txn.AllRows("alloc")

	fmt.Println("allocations:", all.Len())
	fmt.Println("running:", running.Len())
	fmt.Println("running on n1:", running.And(onN1).Len())
	fmt.Println("not running:", all.AndNot(running).Len())

	fmt.Print("running on n1, not batch:")
	for obj := range running.And(onN1).AndNot(batch).All() {
		fmt.Print(" ", obj.(*Alloc).ID)
	}
	fmt.Println()

	// Output:
	// allocations: 5
	// running: 3
	// running on n1: 2
	// not running: 2
	// running on n1, not batch: a2
}

// A watched set fires when a row enters or leaves it -- and not when a row in
// it is merely replaced.
func ExampleTxn_WhereWatch() {
	db := allocDB()
	watch, failed, err := db.Txn(false).WhereWatch("alloc", "status", "failed")
	if err != nil {
		panic(err)
	}
	fired := func() bool {
		select {
		case <-watch:
			return true
		default:
			return false
		}
	}
	fmt.Println("failed:", failed.Len())

	txn := db.Txn(true)
	_ = txn.Insert("alloc", &Alloc{ID: "a3", Node: "n1", Status: "failed", Tags: []string{"retried"}})
	txn.Commit()
	fmt.Println("after replacing a failed allocation:", fired())

	txn = db.Txn(true)
	_ = txn.Insert("alloc", &Alloc{ID: "a5", Node: "n2", Status: "failed"})
	txn.Commit()
	fmt.Println("after another allocation failed:", fired())

	// Output:
	// failed: 1
	// after replacing a failed allocation: false
	// after another allocation failed: true
}

// A typed table returns *Alloc instead of interface{}; a typed key looks a row
// up without boxing the key or resolving names.
func ExampleTable() {
	db := allocDB()
	allocs := memdb.NewTable[Alloc]("alloc")
	byID := allocs.StringKey("id")

	txn := db.Txn(true)
	if err := allocs.Insert(txn, &Alloc{ID: "a6", Node: "n3", Status: "pending"}); err != nil {
		panic(err)
	}
	txn.Commit()

	txn = db.Txn(false)
	a, err := byID.First(txn, "a6")
	if err != nil {
		panic(err)
	}
	fmt.Println(a.ID, a.Node, a.Status)

	missing, _ := byID.First(txn, "nope")
	fmt.Println(missing == nil)

	rows, err := byID.GetPrefix(txn, "a")
	if err != nil {
		panic(err)
	}
	n := 0
	for range rows.All() {
		n++
	}
	fmt.Println(n, "allocations")

	// Output:
	// a6 n3 pending
	// true
	// 6 allocations
}

// Any ResultIterator can be ranged over.
func ExampleAllOf() {
	db := allocDB()
	it, err := db.Txn(false).Get("alloc", "id_prefix", "a")
	if err != nil {
		panic(err)
	}
	for a := range memdb.AllOf[*Alloc](it) {
		if a.Status != "running" {
			fmt.Println(a.ID, a.Status)
		}
	}

	// Output:
	// a3 failed
	// a5 pending
}
