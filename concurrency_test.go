// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type versioned struct {
	ID      string
	Group   string
	Version int
}

func versionedSchema() *DBSchema {
	return &DBSchema{Tables: map[string]*TableSchema{
		"rows": {Name: "rows", Indexes: map[string]*IndexSchema{
			"id":    {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
			"group": {Name: "group", Indexer: &StringFieldIndex{Field: "Group"}},
		}},
	}}
}

// TestWatchersSeeNewState is the end-to-end form of the lazy watch protocol:
// readers take watches on rows and on a whole group while a writer keeps
// rewriting them. Every watch must fire (no lost wakeup), and a reader woken
// by a watch must find newer data in a fresh transaction, because commit
// publishes the new root before it notifies.
func TestWatchersSeeNewState(t *testing.T) {
	const rows = 32
	db, err := NewMemDB(versionedSchema())
	if err != nil {
		t.Fatal(err)
	}
	txn := db.Txn(true)
	for i := 0; i < rows; i++ {
		if err := txn.Insert("rows", &versioned{ID: fmt.Sprintf("row-%02d", i), Group: "g"}); err != nil {
			t.Fatal(err)
		}
	}
	txn.Commit()

	stop := make(chan struct{})
	var readers, writer sync.WaitGroup

	writer.Add(1)
	go func() {
		defer writer.Done()
		for v := 1; ; v++ {
			select {
			case <-stop:
				return
			default:
			}
			txn := db.Txn(true)
			for i := 0; i < rows; i++ {
				if err := txn.Insert("rows", &versioned{ID: fmt.Sprintf("row-%02d", i), Group: "g", Version: v}); err != nil {
					panic(err)
				}
			}
			if v%5 == 0 {
				// Reads and iterators inside the write transaction.
				it, err := txn.Get("rows", "group", "g")
				if err != nil {
					panic(err)
				}
				n := 0
				for obj := it.Next(); obj != nil; obj = it.Next() {
					n++
				}
				if n != rows {
					panic(fmt.Sprintf("writer saw %d rows", n))
				}
				if err := txn.Insert("rows", &versioned{ID: "row-00", Group: "g", Version: v}); err != nil {
					panic(err)
				}
			}
			txn.Commit()
		}
	}()

	deadline := time.Now().Add(1500 * time.Millisecond)
	if testing.Short() {
		deadline = time.Now().Add(200 * time.Millisecond)
	}
	errs := make(chan error, 8)
	fail := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}
	for r := 0; r < 8; r++ {
		readers.Add(1)
		go func(r int) {
			defer readers.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				id := fmt.Sprintf("row-%02d", (i+r)%rows)
				txn := db.Txn(false)
				ws := NewWatchSet()
				var seen int
				if i%2 == 0 {
					ch, obj, err := txn.FirstWatch("rows", "id", id)
					if err != nil || obj == nil {
						fail(fmt.Errorf("FirstWatch(%s): %v %v", id, obj, err))
						return
					}
					seen = obj.(*versioned).Version
					ws.Add(ch)
				} else {
					it, err := txn.Get("rows", "group", "g")
					if err != nil {
						fail(err)
						return
					}
					ws.Add(it.WatchCh())
					for obj := it.Next(); obj != nil; obj = it.Next() {
						if v := obj.(*versioned).Version; v > seen {
							seen = v
						}
					}
				}

				timeout := time.After(10 * time.Second)
				if timedOut := ws.Watch(timeout); timedOut {
					fail(fmt.Errorf("reader %d: watch on %s never fired (lost wakeup)", r, id))
					return
				}

				// Woken: a fresh transaction must show what woke us.
				fresh := db.Txn(false)
				newest := 0
				if i%2 == 0 {
					obj, err := fresh.First("rows", "id", id)
					if err != nil || obj == nil {
						fail(fmt.Errorf("First(%s): %v %v", id, obj, err))
						return
					}
					newest = obj.(*versioned).Version
				} else {
					it, err := fresh.Get("rows", "group", "g")
					if err != nil {
						fail(err)
						return
					}
					for obj := it.Next(); obj != nil; obj = it.Next() {
						if v := obj.(*versioned).Version; v > newest {
							newest = v
						}
					}
				}
				if newest <= seen {
					fail(fmt.Errorf("reader %d: woken, but version %d is not newer than the %d it had seen", r, newest, seen))
					return
				}
			}
		}(r)
	}

	readers.Wait()
	close(stop)
	writer.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

// TestLongKeys: keys longer than the on-stack query scratch (and than any
// inline buffer) must work everywhere a key is built.
func TestLongKeys(t *testing.T) {
	db, err := NewMemDB(versionedSchema())
	if err != nil {
		t.Fatal(err)
	}
	long := func(i int) string { return strings.Repeat("x", 4*keyScratch) + fmt.Sprintf("-%03d", i) }
	txn := db.Txn(true)
	for i := 0; i < 50; i++ {
		if err := txn.Insert("rows", &versioned{ID: long(i), Group: long(i % 3), Version: i}); err != nil {
			t.Fatal(err)
		}
	}
	txn.Commit()

	read := db.Txn(false)
	for i := 0; i < 50; i++ {
		obj, err := read.First("rows", "id", long(i))
		if err != nil || obj == nil || obj.(*versioned).Version != i {
			t.Fatalf("First(long %d) = %v, %v", i, obj, err)
		}
	}
	it, err := read.Get("rows", "group", long(1))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for obj := it.Next(); obj != nil; obj = it.Next() {
		n++
	}
	if n != 17 {
		t.Fatalf("group scan returned %d rows, want 17", n)
	}
	it, err = read.Get("rows", "id_prefix", strings.Repeat("x", 4*keyScratch)+"-00")
	if err != nil {
		t.Fatal(err)
	}
	n = 0
	for obj := it.Next(); obj != nil; obj = it.Next() {
		n++
	}
	if n != 10 {
		t.Fatalf("prefix scan returned %d rows, want 10", n)
	}

	txn = db.Txn(true)
	if deleted, err := txn.DeleteAll("rows", "group", long(2)); err != nil || deleted != 16 {
		t.Fatalf("DeleteAll = %d, %v", deleted, err)
	}
	txn.Commit()
}

// TestWriteStateReuse: write-transaction bookkeeping is pooled; a transaction
// must never see what a previous one (on another table, another database, or
// aborted half-way) left behind.
func TestWriteStateReuse(t *testing.T) {
	wide := &DBSchema{Tables: map[string]*TableSchema{
		"a": {Name: "a", Indexes: map[string]*IndexSchema{
			"id": {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
			"g1": {Name: "g1", Indexer: &StringFieldIndex{Field: "Group"}},
			"g2": {Name: "g2", Indexer: &StringFieldIndex{Field: "Group", Lowercase: true}},
			"v":  {Name: "v", Indexer: &IntFieldIndex{Field: "Version"}},
		}},
		"b": {Name: "b", Indexes: map[string]*IndexSchema{
			"id": {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
		}},
	}}
	db1, err := NewMemDB(wide)
	if err != nil {
		t.Fatal(err)
	}
	db2, err := NewMemDB(versionedSchema())
	if err != nil {
		t.Fatal(err)
	}

	for round := 0; round < 50; round++ {
		txn := db1.Txn(true)
		for i := 0; i < 5; i++ {
			obj := &versioned{ID: fmt.Sprintf("r%d-%d", round, i), Group: "G", Version: round}
			if err := txn.Insert("a", obj); err != nil {
				t.Fatal(err)
			}
			if err := txn.Insert("b", obj); err != nil {
				t.Fatal(err)
			}
		}
		if round%3 == 0 {
			txn.Abort()
		} else {
			txn.Commit()
		}

		txn = db2.Txn(true)
		if err := txn.Insert("rows", &versioned{ID: fmt.Sprintf("x%d", round), Group: "g"}); err != nil {
			t.Fatal(err)
		}
		txn.Commit()
	}

	count := func(db *MemDB, table, index string, args ...interface{}) int {
		it, err := db.Txn(false).Get(table, index, args...)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for obj := it.Next(); obj != nil; obj = it.Next() {
			n++
		}
		return n
	}
	committed := 0
	for round := 0; round < 50; round++ {
		if round%3 != 0 {
			committed++
		}
	}
	for _, c := range []struct {
		table, index string
		args         []interface{}
		want         int
	}{
		{"a", "id", nil, 5 * committed}, {"a", "g1", []interface{}{"G"}, 5 * committed},
		{"a", "g2", []interface{}{"g"}, 5 * committed}, {"a", "v", []interface{}{4}, 5},
		{"a", "v", []interface{}{3}, 0}, {"b", "id", nil, 5 * committed},
	} {
		if got := count(db1, c.table, c.index, c.args...); got != c.want {
			t.Errorf("db1 %s/%s%v: %d rows, want %d", c.table, c.index, c.args, got, c.want)
		}
	}
	if got := count(db2, "rows", "id"); got != 50 {
		t.Errorf("db2: %d rows, want 50", got)
	}
}
