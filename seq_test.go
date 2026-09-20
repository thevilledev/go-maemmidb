// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"fmt"
	"reflect"
	"testing"
)

type seqRow struct {
	ID   string
	Even bool
}

func TestSeq(t *testing.T) {
	db, err := NewMemDB(&DBSchema{Tables: map[string]*TableSchema{"rows": {Name: "rows", Indexes: map[string]*IndexSchema{
		"id": {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	txn := db.Txn(true)
	for i := 0; i < 10; i++ {
		if err := txn.Insert("rows", &seqRow{ID: fmt.Sprintf("row-%03d", i), Even: i%2 == 0}); err != nil {
			t.Fatal(err)
		}
	}
	txn.Commit()
	txn = db.Txn(false)

	it, err := txn.Get("rows", "id")
	if err != nil {
		t.Fatal(err)
	}
	var first []string
	for r := range AllOf[*seqRow](it) {
		first = append(first, r.ID)
		if len(first) == 3 {
			break
		}
	}
	// Breaking out leaves the rest in the iterator.
	var rest []string
	for obj := range All(it) {
		rest = append(rest, obj.(*seqRow).ID)
	}
	if want := []string{"row-000", "row-001", "row-002"}; !reflect.DeepEqual(first, want) {
		t.Errorf("first three: %v", first)
	}
	if len(rest) != 7 || rest[0] != "row-003" || rest[6] != "row-009" {
		t.Errorf("rest: %v", rest)
	}
	for range All(it) {
		t.Error("an exhausted iterator yielded a row")
	}

	// A FilterIterator is a ResultIterator too.
	it, _ = txn.Get("rows", "id")
	odd := NewFilterIterator(it, func(obj interface{}) bool { return obj.(*seqRow).Even })
	n := 0
	for r := range AllOf[*seqRow](odd) {
		if r.Even {
			t.Errorf("filter let %s through", r.ID)
		}
		n++
	}
	if n != 5 {
		t.Errorf("%d odd rows, want 5", n)
	}
}
