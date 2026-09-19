// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// These tests pin behaviour that upstream documents but does not test, and
// that this implementation has to work for: write transactions mutate tree
// nodes in place, so anything that escapes a write transaction must be frozen
// first. (Found by mutation testing: removing the freeze broke no other test
// in this package.)

func ids(it ResultIterator) []string {
	var out []string
	for obj := it.Next(); obj != nil; obj = it.Next() {
		out = append(out, obj.(*versioned).ID)
	}
	return out
}

// TestIteratorInWriteTxnIsASnapshot: "When a ResultIterator is created from a
// write transaction, the results from Next will reflect a snapshot of the
// table at the time the ResultIterator is created."
func TestIteratorInWriteTxnIsASnapshot(t *testing.T) {
	db, err := NewMemDB(versionedSchema())
	if err != nil {
		t.Fatal(err)
	}
	row := func(i int) *versioned { return &versioned{ID: fmt.Sprintf("row-%03d", i), Group: "g"} }

	txn := db.Txn(true)
	for i := 0; i < 40; i += 2 {
		if err := txn.Insert("rows", row(i)); err != nil {
			t.Fatal(err)
		}
	}
	txn.Commit()

	type open func(txn *Txn) (ResultIterator, error)
	cases := map[string]open{
		"Get":               func(txn *Txn) (ResultIterator, error) { return txn.Get("rows", "id") },
		"Get group":         func(txn *Txn) (ResultIterator, error) { return txn.Get("rows", "group", "g") },
		"GetReverse":        func(txn *Txn) (ResultIterator, error) { return txn.GetReverse("rows", "id") },
		"LowerBound":        func(txn *Txn) (ResultIterator, error) { return txn.LowerBound("rows", "id", "row-010") },
		"ReverseLowerBound": func(txn *Txn) (ResultIterator, error) { return txn.ReverseLowerBound("rows", "id", "row-030") },
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			txn := db.Txn(true)
			defer txn.Abort()

			// Make the indexes dirty first: the interesting case is an
			// iterator over nodes this transaction owns.
			for i := 1; i < 40; i += 4 {
				if err := txn.Insert("rows", row(i)); err != nil {
					t.Fatal(err)
				}
			}
			ref, err := open(txn)
			if err != nil {
				t.Fatal(err)
			}
			want := ids(ref)

			it, err := open(txn)
			if err != nil {
				t.Fatal(err)
			}
			// Read a little, then rewrite the table underneath the iterator:
			// delete what it has yet to return, insert in between, update.
			got := []string{it.Next().(*versioned).ID, it.Next().(*versioned).ID}
			for i := 0; i < 40; i++ {
				switch i % 3 {
				case 0:
					if err := txn.Delete("rows", row(i)); err != nil && err != ErrNotFound {
						t.Fatal(err)
					}
				case 1:
					if err := txn.Insert("rows", &versioned{ID: fmt.Sprintf("row-%03d", i), Group: "g", Version: 9}); err != nil {
						t.Fatal(err)
					}
				case 2:
					if err := txn.Insert("rows", &versioned{ID: fmt.Sprintf("row-%03d-new", i), Group: "g"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			got = append(got, ids(it)...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("iterator observed later writes of its own transaction:\n got  %v\n want %v", got, want)
			}

			// An iterator opened now does see them.
			fresh, err := open(txn)
			if err != nil {
				t.Fatal(err)
			}
			if now := ids(fresh); reflect.DeepEqual(now, want) {
				t.Fatal("a new iterator does not reflect the transaction's writes")
			}
		})
	}
}

// TestWatchInWriteTxn: a watch channel taken inside a write transaction, over
// data that transaction has already written, fires when the transaction writes
// there again and commits.
func TestWatchInWriteTxn(t *testing.T) {
	db, err := NewMemDB(versionedSchema())
	if err != nil {
		t.Fatal(err)
	}
	txn := db.Txn(true)
	if err := txn.Insert("rows", &versioned{ID: "a", Group: "g", Version: 1}); err != nil {
		t.Fatal(err)
	}
	ch, obj, err := txn.FirstWatch("rows", "id", "a")
	if err != nil || obj.(*versioned).Version != 1 {
		t.Fatalf("FirstWatch = %v, %v", obj, err)
	}
	it, err := txn.Get("rows", "group", "g")
	if err != nil {
		t.Fatal(err)
	}
	groupCh := it.WatchCh()

	if err := txn.Insert("rows", &versioned{ID: "a", Group: "g", Version: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
		t.Fatal("watch fired before commit")
	default:
	}
	if obj, _ := txn.First("rows", "id", "a"); obj.(*versioned).Version != 2 {
		t.Fatal("second write not visible")
	}
	txn.Commit()
	for name, c := range map[string]<-chan struct{}{"key": ch, "group": groupCh} {
		select {
		case <-c:
		default:
			t.Fatalf("%s watch taken inside the write transaction did not fire at commit", name)
		}
	}
}

// TestFailedInsertLeavesTxnUnchanged: an Insert that fails on a secondary
// index must not leave the row half-written. (Upstream leaves whatever indexes
// it had already processed modified; see COMPATIBILITY.md.)
func TestFailedInsertLeavesTxnUnchanged(t *testing.T) {
	db, err := NewMemDB(versionedSchema())
	if err != nil {
		t.Fatal(err)
	}
	txn := db.Txn(true)
	original := &versioned{ID: "a", Group: "g", Version: 1}
	if err := txn.Insert("rows", original); err != nil {
		t.Fatal(err)
	}
	txn.Commit()

	txn = db.Txn(true)
	txn.TrackChanges()

	// The "group" index does not allow missing values.
	err = txn.Insert("rows", &versioned{ID: "b", Version: 7})
	if err == nil || !strings.Contains(err.Error(), "missing value for index 'group'") {
		t.Fatalf("new row: err = %v", err)
	}
	err = txn.Insert("rows", &versioned{ID: "a", Version: 7})
	if err == nil || !strings.Contains(err.Error(), "missing value for index 'group'") {
		t.Fatalf("update: err = %v", err)
	}

	check := func(txn *Txn, when string) {
		if obj, _ := txn.First("rows", "id", "b"); obj != nil {
			t.Fatalf("%s: the failed insert is visible by id: %+v", when, obj)
		}
		if obj, _ := txn.First("rows", "id", "a"); obj != original {
			t.Fatalf("%s: the failed update replaced the row: %+v", when, obj)
		}
		it, err := txn.Get("rows", "group", "g")
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(it); !reflect.DeepEqual(got, []string{"a"}) {
			t.Fatalf("%s: group index holds %v", when, got)
		}
	}
	check(txn, "in the transaction")
	if changes := txn.Changes(); len(changes) != 0 {
		t.Fatalf("failed inserts were recorded as changes: %+v", changes)
	}
	txn.Commit()
	check(db.Txn(false), "after commit")
}
