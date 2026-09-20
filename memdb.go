// Copyright IBM Corp. 2015, 2026
// SPDX-License-Identifier: MPL-2.0
//
// Modifications Copyright (c) 2026 Ville Vesilehto
// Derived from github.com/hashicorp/go-memdb memdb.go @ 7d3fdd5: the exported
// API and its documentation are upstream's; the storage layout (compiled
// schema, flat root of index trees) is new.

// Package memdb provides an in-memory database that supports transactions
// and MVCC.
package memdb

import (
	"sync"

	"github.com/thevilledev/go-maemmidb/internal/radix"
)

// MemDB is an in-memory database providing Atomicity, Consistency, and
// Isolation from ACID. MemDB doesn't provide Durability since it is an
// in-memory database.
//
// MemDB provides a table abstraction to store objects (rows) with multiple
// indexes based on inserted values. The database makes use of immutable radix
// trees to provide transactions and MVCC.
//
// Objects inserted into MemDB are not copied. It is **extremely important**
// that objects are not modified in-place after they are inserted since they
// are stored directly in MemDB. It remains unsafe to modify inserted objects
// even after they've been deleted from MemDB since there may still be older
// snapshots of the DB being read from other goroutines.
type MemDB struct {
	// compiled is the schema, as given and as compiled. It is immutable and
	// shared with every snapshot of the database; one pointer keeps MemDB --
	// which Snapshot allocates -- as small as upstream's.
	*compiled
	root    rootPtr
	primary bool

	// There can only be a single writer at once
	writer sync.Mutex
}

// compiled is a schema together with its compiled form.
type compiled struct {
	schema *DBSchema
	tables nameIndex[*compiledTable]
}

// inlineTrees is the number of index trees a dbRoot holds without a second
// allocation.
const inlineTrees = 12

// dbRoot is one immutable version of the whole database: the tree of every
// index, addressed by compiledIndex.slot. A commit publishes a new dbRoot with
// a single atomic pointer store.
type dbRoot struct {
	trees  []radix.Tree
	inline [inlineTrees]radix.Tree
}

func newDBRoot(n int) *dbRoot {
	r := &dbRoot{}
	if n <= inlineTrees {
		r.trees = r.inline[:n:n]
	} else {
		r.trees = make([]radix.Tree, n)
	}
	return r
}

// NewMemDB creates a new MemDB with the given schema.
func NewMemDB(schema *DBSchema) (*MemDB, error) {
	// Validate the schema
	if err := schema.Validate(); err != nil {
		return nil, err
	}

	// Create the MemDB
	tables, slots := compileSchema(schema)

	// Every index starts as its own empty tree. The roots must be distinct
	// objects: watching "the whole index" watches its root node.
	root := newDBRoot(slots)
	for i := range root.trees {
		root.trees[i] = radix.New()
	}
	return newMemDB(&compiled{schema: schema, tables: tables}, root, true), nil
}

// DBSchema returns schema in use for introspection.
//
// The method is intended for *read-only* debugging use cases,
// returned schema should *never be modified in-place*.
func (db *MemDB) DBSchema() *DBSchema {
	return db.schema
}

// Txn is used to start a new transaction in either read or write mode.
// There can only be a single concurrent writer, but any number of readers.
func (db *MemDB) Txn(write bool) *Txn {
	if write {
		db.writer.Lock()
	}
	txn := &Txn{
		db:    db,
		write: write,
		root:  db.root.load(),
	}
	return txn
}

// Snapshot is used to capture a point-in-time snapshot  of the database that
// will not be affected by any write operations to the existing DB.
//
// If MemDB is storing reference-based values (pointers, maps, slices, etc.),
// the Snapshot will not deep copy those values. Therefore, it is still unsafe
// to modify any inserted values in either DB.
func (db *MemDB) Snapshot() *MemDB {
	return newMemDB(db.compiled, db.root.load(), false)
}
