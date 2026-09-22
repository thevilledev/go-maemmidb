// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb_test

import (
	"fmt"

	memdb "github.com/thevilledev/go-maemmidb"
)

// The original go-memdb README example, linked from docs/getting-started.md
// and checked on every test run.
func Example() {
	// Create a sample struct
	type Person struct {
		Email string
		Name  string
		Age   int
	}

	// Create the DB schema
	schema := &memdb.DBSchema{
		Tables: map[string]*memdb.TableSchema{
			"person": {
				Name: "person",
				Indexes: map[string]*memdb.IndexSchema{
					"id": {
						Name:    "id",
						Unique:  true,
						Indexer: &memdb.StringFieldIndex{Field: "Email"},
					},
					"age": {
						Name:    "age",
						Unique:  false,
						Indexer: &memdb.IntFieldIndex{Field: "Age"},
					},
				},
			},
		},
	}

	// Create a new data base
	db, err := memdb.NewMemDB(schema)
	if err != nil {
		panic(err)
	}

	// Create a write transaction
	txn := db.Txn(true)

	// Insert some people
	people := []*Person{
		{"joe@aol.com", "Joe", 30},
		{"lucy@aol.com", "Lucy", 35},
		{"tariq@aol.com", "Tariq", 21},
		{"dorothy@aol.com", "Dorothy", 53},
	}
	for _, p := range people {
		if err := txn.Insert("person", p); err != nil {
			panic(err)
		}
	}

	// Commit the transaction
	txn.Commit()

	// Create read-only transaction
	txn = db.Txn(false)
	defer txn.Abort()

	// Lookup by email
	raw, err := txn.First("person", "id", "joe@aol.com")
	if err != nil {
		panic(err)
	}

	// Say hi!
	fmt.Printf("Hello %s!\n", raw.(*Person).Name)

	// List all the people
	it, err := txn.Get("person", "id")
	if err != nil {
		panic(err)
	}

	fmt.Println("All the people:")
	for obj := it.Next(); obj != nil; obj = it.Next() {
		p := obj.(*Person)
		fmt.Printf("  %s\n", p.Name)
	}

	// Range scan over people with ages between 25 and 35 inclusive
	it, err = txn.LowerBound("person", "age", 25)
	if err != nil {
		panic(err)
	}

	fmt.Println("People aged 25 - 35:")
	for obj := it.Next(); obj != nil; obj = it.Next() {
		p := obj.(*Person)
		if p.Age > 35 {
			break
		}
		fmt.Printf("  %s is aged %d\n", p.Name, p.Age)
	}

	// Output:
	// Hello Joe!
	// All the people:
	//   Dorothy
	//   Joe
	//   Lucy
	//   Tariq
	// People aged 25 - 35:
	//   Joe is aged 30
	//   Lucy is aged 35
}

// A watch fires when a later write transaction changes the result of the
// query it came from.
func ExampleTxn_FirstWatch() {
	type Node struct {
		ID     string
		Status string
	}
	db, err := memdb.NewMemDB(&memdb.DBSchema{Tables: map[string]*memdb.TableSchema{
		"nodes": {Name: "nodes", Indexes: map[string]*memdb.IndexSchema{
			"id": {Name: "id", Unique: true, Indexer: &memdb.StringFieldIndex{Field: "ID"}},
		}},
	}})
	if err != nil {
		panic(err)
	}

	txn := db.Txn(true)
	if err := txn.Insert("nodes", &Node{ID: "n1", Status: "starting"}); err != nil {
		panic(err)
	}
	txn.Commit()

	// Read the node and ask to be told when it changes.
	watch, obj, err := db.Txn(false).FirstWatch("nodes", "id", "n1")
	if err != nil {
		panic(err)
	}
	fmt.Println("status:", obj.(*Node).Status)

	// Objects are never modified in place: write a new version.
	txn = db.Txn(true)
	if err := txn.Insert("nodes", &Node{ID: "n1", Status: "ready"}); err != nil {
		panic(err)
	}
	txn.Commit()

	<-watch // closed by the commit above
	obj, _ = db.Txn(false).First("nodes", "id", "n1")
	fmt.Println("status:", obj.(*Node).Status)

	// Output:
	// status: starting
	// status: ready
}
