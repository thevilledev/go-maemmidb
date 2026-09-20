// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// bmRow is the row type of the bitmap index tests.
type bmRow struct {
	ID     string
	Status string
	Owner  string
	Tags   []string
	Active bool
	Group  string
	Rev    int
}

func bmSchema() *DBSchema {
	return &DBSchema{Tables: map[string]*TableSchema{
		"rows": {Name: "rows", Indexes: map[string]*IndexSchema{
			"id":     {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
			"group":  {Name: "group", AllowMissing: true, Indexer: &StringFieldIndex{Field: "Group"}},
			"status": {Name: "status", Indexer: &BitmapIndex{Indexer: &StringFieldIndex{Field: "Status"}}},
			"owner": {Name: "owner", AllowMissing: true,
				Indexer: &BitmapIndex{Indexer: &StringIndex[bmRow]{Get: func(r *bmRow) string { return r.Owner }, Lowercase: true}}},
			"tags":   {Name: "tags", AllowMissing: true, Indexer: &BitmapIndex{Indexer: &StringSliceFieldIndex{Field: "Tags"}}},
			"active": {Name: "active", Indexer: &BitmapIndex{Indexer: &BoolFieldIndex{Field: "Active"}}},
		}},
		// A second table without bitmap indexes, and a third with one, so that
		// the per-table bookkeeping is exercised.
		"plain": {Name: "plain", Indexes: map[string]*IndexSchema{
			"id": {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
		}},
		"other": {Name: "other", Indexes: map[string]*IndexSchema{
			"id":     {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
			"status": {Name: "status", Indexer: &BitmapIndex{Indexer: &StringFieldIndex{Field: "Status"}}},
		}},
	}}
}

func randomBmRow(rng *rand.Rand, id int) *bmRow {
	r := &bmRow{
		ID:     fmt.Sprintf("row-%04d", id),
		Status: []string{"pending", "running", "complete", "failed", "lost"}[rng.Intn(5)],
		Active: rng.Intn(3) == 0,
		Group:  fmt.Sprintf("g%d", rng.Intn(4)),
		Rev:    rng.Int(),
	}
	if rng.Intn(4) != 0 {
		r.Owner = []string{"Alice", "bob", "CAROL"}[rng.Intn(3)]
	}
	for i := rng.Intn(4); i > 0; i-- {
		r.Tags = append(r.Tags, []string{"red", "green", "blue", "redder", "x"}[rng.Intn(5)])
	}
	return r
}

// scan is the oracle: the rows of the table that satisfy keep, by id.
func scan(t *testing.T, txn *Txn, table string, keep func(*bmRow) bool) []string {
	t.Helper()
	it, err := txn.Get(table, "id")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for r := range AllOf[*bmRow](it) {
		if keep(r) {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

func setIDs(t *testing.T, s RowSet) []string {
	t.Helper()
	ids := []string{}
	for obj := range s.All() {
		ids = append(ids, obj.(*bmRow).ID)
	}
	if len(ids) != s.Len() {
		t.Fatalf("set of Len %d yields %d rows", s.Len(), len(ids))
	}
	viaIterator := 0
	for it := s.Iterator(); it.Next() != nil; {
		viaIterator++
	}
	if viaIterator != len(ids) {
		t.Fatalf("iterator yields %d rows, All %d", viaIterator, len(ids))
	}
	sort.Strings(ids)
	return ids
}

func hasTag(r *bmRow, tag string) bool {
	for _, t := range r.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// checkQueries compares a spread of bitmap queries with the oracle.
func checkQueries(t *testing.T, txn *Txn, when string) {
	t.Helper()
	where := func(index string, args ...interface{}) RowSet {
		t.Helper()
		s, err := txn.Where("rows", index, args...)
		if err != nil {
			t.Fatalf("%s: Where(%s, %v): %v", when, index, args, err)
		}
		return s
	}
	expect := func(what string, s RowSet, keep func(*bmRow) bool) {
		t.Helper()
		if got, want := setIDs(t, s), scan(t, txn, "rows", keep); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %s:\n got  %v\n want %v", when, what, got, want)
		}
	}

	all, err := txn.AllRows("rows")
	if err != nil {
		t.Fatal(err)
	}
	expect("all rows", all, func(*bmRow) bool { return true })

	running, failed := where("status", "running"), where("status", "failed")
	active, alice := where("active", true), where("owner", "ALICE")
	red, blue := where("tags", "red"), where("tags", "blue")

	expect("status=running", running, func(r *bmRow) bool { return r.Status == "running" })
	expect("status=nope", where("status", "nope"), func(r *bmRow) bool { return false })
	expect("active", active, func(r *bmRow) bool { return r.Active })
	expect("inactive", where("active", false), func(r *bmRow) bool { return !r.Active })
	expect("owner=alice", alice, func(r *bmRow) bool { return strings.EqualFold(r.Owner, "alice") })
	expect("tag=red", red, func(r *bmRow) bool { return hasTag(r, "red") })
	expect("tag prefix red", where("tags_prefix", "red"), func(r *bmRow) bool { return hasTag(r, "red") || hasTag(r, "redder") })
	expect("status prefix", where("status_prefix", "p"), func(r *bmRow) bool { return r.Status == "pending" })
	expect("owner prefix", where("owner_prefix", "C"), func(r *bmRow) bool { return strings.EqualFold(r.Owner, "carol") })

	expect("running AND active", running.And(active), func(r *bmRow) bool { return r.Status == "running" && r.Active })
	expect("running AND active AND red", running.And(active, red), func(r *bmRow) bool {
		return r.Status == "running" && r.Active && hasTag(r, "red")
	})
	expect("running OR failed", running.Or(failed), func(r *bmRow) bool { return r.Status == "running" || r.Status == "failed" })
	expect("red OR blue, not alice's", red.Or(blue).AndNot(alice), func(r *bmRow) bool {
		return (hasTag(r, "red") || hasTag(r, "blue")) && !strings.EqualFold(r.Owner, "alice")
	})
	expect("NOT running", all.AndNot(running), func(r *bmRow) bool { return r.Status != "running" })
	expect("no owner", all.AndNot(where("owner_prefix", "")), func(r *bmRow) bool { return r.Owner == "" })
	expect("(running AND NOT active) OR (failed AND red)", running.AndNot(active).Or(failed.And(red)), func(r *bmRow) bool {
		return (r.Status == "running" && !r.Active) || (r.Status == "failed" && hasTag(r, "red"))
	})
}

// TestBitmapIndexAgainstScan drives a database with random transactions and
// compares the bitmap queries with a filtering scan of the primary index --
// inside the write transactions, after commits, after aborts, and in old read
// transactions that must not have moved.
func TestBitmapIndexAgainstScan(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		rng := rand.New(rand.NewSource(seed))
		db, err := NewMemDB(bmSchema())
		if err != nil {
			t.Fatal(err)
		}
		type frozen struct {
			txn  *Txn
			sets map[string][]string
		}
		var old []frozen
		nextID := 0
		for round := 0; round < 60; round++ {
			txn := db.Txn(true)
			for op := rng.Intn(12); op >= 0; op-- {
				switch rng.Intn(10) {
				case 0, 1, 2, 3: // insert
					nextID++
					if err := txn.Insert("rows", randomBmRow(rng, nextID)); err != nil {
						t.Fatal(err)
					}
				case 4, 5: // update, moving between values or not
					if nextID == 0 {
						continue
					}
					id := 1 + rng.Intn(nextID)
					cur, _ := txn.First("rows", "id", fmt.Sprintf("row-%04d", id))
					if cur == nil {
						continue
					}
					next := *cur.(*bmRow)
					if rng.Intn(2) == 0 {
						next = *randomBmRow(rng, id)
					}
					next.Rev++
					if err := txn.Insert("rows", &next); err != nil {
						t.Fatal(err)
					}
				case 6, 7: // delete
					if nextID == 0 {
						continue
					}
					err := txn.Delete("rows", &bmRow{ID: fmt.Sprintf("row-%04d", 1+rng.Intn(nextID))})
					if err != nil && err != ErrNotFound {
						t.Fatal(err)
					}
				case 8:
					if _, err := txn.DeleteAll("rows", "group", fmt.Sprintf("g%d", rng.Intn(4))); err != nil {
						t.Fatal(err)
					}
				case 9:
					if _, err := txn.DeletePrefix("rows", "id_prefix", fmt.Sprintf("row-%03d", rng.Intn(1+nextID/10))); err != nil {
						t.Fatal(err)
					}
				}
				if rng.Intn(4) == 0 {
					checkQueries(t, txn, fmt.Sprintf("seed %d round %d, inside the transaction", seed, round))
				}
			}
			if rng.Intn(5) == 0 {
				txn.Abort()
			} else {
				txn.Commit()
			}

			read := db.Txn(false)
			checkQueries(t, read, fmt.Sprintf("seed %d round %d, after the transaction", seed, round))
			if round%10 == 0 {
				running, _ := read.Where("rows", "status", "running")
				all, _ := read.AllRows("rows")
				old = append(old, frozen{read, map[string][]string{
					"running": setIDs(t, running), "all": setIDs(t, all),
				}})
			}
		}
		// Old read transactions still see their version.
		for i, f := range old {
			checkQueries(t, f.txn, fmt.Sprintf("seed %d, old transaction %d", seed, i))
			running, _ := f.txn.Where("rows", "status", "running")
			if !reflect.DeepEqual(setIDs(t, running), f.sets["running"]) {
				t.Fatalf("seed %d: old transaction %d moved", seed, i)
			}
		}
	}
}

func TestBitmapIndexRowIDsAreReused(t *testing.T) {
	db, err := NewMemDB(bmSchema())
	if err != nil {
		t.Fatal(err)
	}
	insert := func(from, to int) {
		txn := db.Txn(true)
		for i := from; i < to; i++ {
			if err := txn.Insert("rows", &bmRow{ID: fmt.Sprintf("row-%04d", i), Status: "running"}); err != nil {
				t.Fatal(err)
			}
		}
		txn.Commit()
	}
	insert(0, 1000)
	for round := 0; round < 20; round++ {
		txn := db.Txn(true)
		if _, err := txn.DeleteAll("rows", "id_prefix", "row-0"); err != nil {
			t.Fatal(err)
		}
		txn.Commit()
		insert(0, 1000)
	}
	ct, _ := db.tables.get("rows")
	rows := db.Txn(false).root.ext.tables[ct.ord]
	if rows.next != 1000 || rows.all.Len() != 1000 || !rows.free.Empty() || rows.rows.Len() != 1000 {
		t.Fatalf("after churn: next id %d, %d in use, %d free, %d rows", rows.next, rows.all.Len(), rows.free.Len(), rows.rows.Len())
	}
}

func TestBitmapIndexUpdateLeavesUnchangedValuesAlone(t *testing.T) {
	db, err := NewMemDB(bmSchema())
	if err != nil {
		t.Fatal(err)
	}
	txn := db.Txn(true)
	for i := 0; i < 50; i++ {
		if err := txn.Insert("rows", &bmRow{ID: fmt.Sprintf("row-%04d", i), Status: "running", Tags: []string{"red"}}); err != nil {
			t.Fatal(err)
		}
	}
	txn.Commit()

	read := db.Txn(false)
	statusCh, _, err := read.WhereWatch("rows", "status", "running")
	if err != nil {
		t.Fatal(err)
	}
	pendingCh, _, _ := read.WhereWatch("rows", "status", "pending")
	tagCh, _, _ := read.WhereWatch("rows", "tags", "red")

	fired := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}

	// An update that keeps the indexed values changes no set...
	txn = db.Txn(true)
	if err := txn.Insert("rows", &bmRow{ID: "row-0007", Status: "running", Tags: []string{"red"}, Rev: 2}); err != nil {
		t.Fatal(err)
	}
	txn.Commit()
	if fired(statusCh) || fired(tagCh) || fired(pendingCh) {
		t.Fatal("an update that moved nothing fired a watch")
	}
	// ... but the set yields the new object.
	set, _ := db.Txn(false).Where("rows", "status", "running")
	found := false
	for obj := range set.All() {
		if r := obj.(*bmRow); r.ID == "row-0007" {
			found = r.Rev == 2
		}
	}
	if !found {
		t.Fatal("the set does not yield the updated row")
	}

	// A move fires exactly the sets it touches, including one that did not
	// exist yet.
	txn = db.Txn(true)
	if err := txn.Insert("rows", &bmRow{ID: "row-0007", Status: "pending", Tags: []string{"red"}, Rev: 3}); err != nil {
		t.Fatal(err)
	}
	txn.Commit()
	if !fired(statusCh) || !fired(pendingCh) || fired(tagCh) {
		t.Fatalf("after a move: running %v, pending %v, tag %v", fired(statusCh), fired(pendingCh), fired(tagCh))
	}
}

func TestBitmapIndexErrors(t *testing.T) {
	db, err := NewMemDB(bmSchema())
	if err != nil {
		t.Fatal(err)
	}
	txn := db.Txn(true)
	if err := txn.Insert("rows", &bmRow{ID: "a", Status: "running"}); err != nil {
		t.Fatal(err)
	}

	for what, err := range map[string]error{
		"Get on a bitmap index": func() error { _, err := txn.Get("rows", "status", "running"); return err }(),
		"First on a bitmap index": func() error {
			_, err := txn.First("rows", "status_prefix", "r")
			return err
		}(),
		"Where on an ordinary index": func() error { _, err := txn.Where("rows", "group", "g1"); return err }(),
		"Where on an unknown index":  func() error { _, err := txn.Where("rows", "nope", "x"); return err }(),
		"Where on an unknown table":  func() error { _, err := txn.Where("nope", "status", "x"); return err }(),
		"Where without a value":      func() error { _, err := txn.Where("rows", "status"); return err }(),
		"Where with a bad value":     func() error { _, err := txn.Where("rows", "status", 7); return err }(),
		"prefix Where on a bool":     func() error { _, err := txn.Where("rows", "active_prefix", true); return err }(),
		"AllRows of a plain table":   func() error { _, err := txn.AllRows("plain"); return err }(),
		"AllRows of nothing":         func() error { _, err := txn.AllRows("nope"); return err }(),
	} {
		if err == nil {
			t.Errorf("%s did not fail", what)
		}
	}

	// A row without a required bitmap value is refused, and the transaction
	// is left as it was.
	if err := txn.Insert("rows", &bmRow{ID: "b"}); err == nil || err.Error() != "missing value for index 'status'" {
		t.Fatalf("insert without a status: %v", err)
	}
	if obj, _ := txn.First("rows", "id", "b"); obj != nil {
		t.Fatal("a refused row is in the table")
	}
	if all, _ := txn.AllRows("rows"); all.Len() != 1 {
		t.Fatalf("%d rows after a refused insert", all.Len())
	}
	txn.Abort()

	for what, schema := range map[string]*DBSchema{
		"unique": {Tables: map[string]*TableSchema{"t": {Name: "t", Indexes: map[string]*IndexSchema{
			"id": {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
			"s":  {Name: "s", Unique: true, Indexer: &BitmapIndex{Indexer: &StringFieldIndex{Field: "Status"}}},
		}}}},
		"nested": {Tables: map[string]*TableSchema{"t": {Name: "t", Indexes: map[string]*IndexSchema{
			"id": {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
			"s":  {Name: "s", Indexer: &BitmapIndex{Indexer: &BitmapIndex{Indexer: &StringFieldIndex{Field: "Status"}}}},
		}}}},
		"empty": {Tables: map[string]*TableSchema{"t": {Name: "t", Indexes: map[string]*IndexSchema{
			"id": {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
			"s":  {Name: "s", Indexer: &BitmapIndex{}},
		}}}},
		"as the id": {Tables: map[string]*TableSchema{"t": {Name: "t", Indexes: map[string]*IndexSchema{
			"id": {Name: "id", Unique: true, Indexer: &BitmapIndex{Indexer: &StringFieldIndex{Field: "ID"}}},
		}}}},
	} {
		if _, err := NewMemDB(schema); err == nil {
			t.Errorf("a bitmap index that is %s was accepted", what)
		}
	}
}

func TestRowSetsOfDifferentStatesPanic(t *testing.T) {
	db, err := NewMemDB(bmSchema())
	if err != nil {
		t.Fatal(err)
	}
	txn := db.Txn(true)
	for _, table := range []string{"rows", "other"} {
		if err := txn.Insert(table, &bmRow{ID: "a", Status: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := txn.Where("rows", "status", "running")
	same, _ := txn.Where("rows", "active", false)
	if before.And(same).Len() != 1 {
		t.Fatal("two sets of one state do not combine")
	}
	other, _ := txn.Where("other", "status", "running")
	if err := txn.Insert("rows", &bmRow{ID: "b", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	after, _ := txn.Where("rows", "status", "running")
	if before.Len() != 1 || after.Len() != 2 {
		t.Fatalf("set sizes %d and %d", before.Len(), after.Len())
	}
	for what, combine := range map[string]func(){
		"before and after a write": func() { before.And(after) },
		"two tables":               func() { after.Or(other) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("combining sets of %s did not panic", what)
				}
			}()
			combine()
		}()
	}
	txn.Abort()
}

func TestBitmapIndexSnapshots(t *testing.T) {
	db, err := NewMemDB(bmSchema())
	if err != nil {
		t.Fatal(err)
	}
	txn := db.Txn(true)
	for i := 0; i < 10; i++ {
		if err := txn.Insert("rows", &bmRow{ID: fmt.Sprintf("row-%04d", i), Status: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	// Txn.Snapshot freezes uncommitted state.
	snap := txn.Snapshot()
	if err := txn.Insert("rows", &bmRow{ID: "later", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := txn.Delete("rows", &bmRow{ID: "row-0003"}); err != nil {
		t.Fatal(err)
	}
	txn.Commit()
	if s, _ := snap.Where("rows", "status", "running"); s.Len() != 10 {
		t.Fatalf("transaction snapshot sees %d rows", s.Len())
	}
	checkQueries(t, snap, "transaction snapshot")

	// MemDB.Snapshot is an independent, writable database.
	clone := db.Snapshot()
	w := clone.Txn(true)
	if _, err := w.DeleteAll("rows", "id"); err != nil {
		t.Fatal(err)
	}
	if err := w.Insert("rows", &bmRow{ID: "only", Status: "lost"}); err != nil {
		t.Fatal(err)
	}
	w.Commit()
	if s, _ := clone.Txn(false).AllRows("rows"); s.Len() != 1 {
		t.Fatalf("clone has %d rows", s.Len())
	}
	if s, _ := db.Txn(false).AllRows("rows"); s.Len() != 10 {
		t.Fatalf("original has %d rows after its clone was written", s.Len())
	}
	checkQueries(t, db.Txn(false), "original")
	checkQueries(t, clone.Txn(false), "clone")
}

// TestBitmapIndexConcurrentReaders runs set queries against a database that
// is being written, under the race detector.
func TestBitmapIndexConcurrentReaders(t *testing.T) {
	db, err := NewMemDB(bmSchema())
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				txn := db.Txn(false)
				running, err := txn.Where("rows", "status", "running")
				if err != nil {
					t.Error(err)
					return
				}
				active, _ := txn.Where("rows", "active", true)
				both := running.And(active)
				n := 0
				for obj := range both.All() {
					if r := obj.(*bmRow); r.Status != "running" || !r.Active {
						t.Errorf("row %s is in running AND active", r.ID)
						return
					}
					n++
				}
				if n != both.Len() {
					t.Errorf("set of %d rows yields %d", both.Len(), n)
					return
				}
			}
		}()
	}
	rng := rand.New(rand.NewSource(1))
	deadline := time.Now().Add(300 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		txn := db.Txn(true)
		for j := 0; j < 5; j++ {
			if err := txn.Insert("rows", randomBmRow(rng, rng.Intn(500))); err != nil {
				t.Fatal(err)
			}
		}
		if i%3 == 0 {
			_ = txn.Delete("rows", &bmRow{ID: fmt.Sprintf("row-%04d", rng.Intn(500))})
		}
		txn.Commit()
	}
	close(stop)
	wg.Wait()
}
