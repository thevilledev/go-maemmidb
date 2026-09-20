// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build !upstream && !base

package bench

import (
	"fmt"
	"runtime"
	"testing"

	mm "github.com/thevilledev/go-maemmidb"
)

// The benchmarks in this file measure go-maemmidb's extensions, which
// hashicorp/go-memdb does not have: they are excluded from the upstream build
// (and from the build against an older revision of this package) and compared
// with the equivalent use of the common API instead, in the same process.
// benchgate does not gate them; their names all start with BenchmarkExt.

const extTable = "rows"

// extSchema describes one table two ways: with three ordinary secondary
// indexes on low-cardinality columns, or with the same three as bitmap indexes.
func extSchema(bitmaps bool) *mm.DBSchema {
	wrap := func(ix mm.Indexer) mm.Indexer {
		if bitmaps {
			return &mm.BitmapIndex{Indexer: ix}
		}
		return ix
	}
	return &mm.DBSchema{Tables: map[string]*mm.TableSchema{extTable: {Name: extTable, Indexes: map[string]*mm.IndexSchema{
		"id":     {Name: "id", Unique: true, Indexer: &mm.StringFieldIndex{Field: "ID"}},
		"region": {Name: "region", Indexer: wrap(&mm.StringIndex[Row]{Get: func(r *Row) string { return r.Meta["region"] }})},
		"active": {Name: "active", Indexer: wrap(&mm.BoolFieldIndex{Field: "Active"})},
		"tags":   {Name: "tags", Indexer: wrap(&mm.StringSliceFieldIndex{Field: "Tags"})},
	}}}}
}

var extDBs = map[string]*mm.MemDB{}

func extDB(b *testing.B, bitmaps bool, size int) (*mm.MemDB, []*Row) {
	b.Helper()
	rows := makeRows(shapeUUID, 0, size, groupCount(size), 3)
	key := fmt.Sprintf("%v/%d", bitmaps, size)
	if db, ok := extDBs[key]; ok {
		return db, rows
	}
	db, err := mm.NewMemDB(extSchema(bitmaps))
	must(b, err)
	txn := db.Txn(true)
	for _, r := range rows {
		must(b, txn.Insert(extTable, r))
	}
	txn.Commit()
	extDBs[key] = db
	return db, rows
}

// BenchmarkExtCount answers "how many rows are in region eu-north, active, and
// tagged tag-07?" -- by walking the most selective ordinary index and filtering
// on the other two columns, and by intersecting three bitmap sets.
func BenchmarkExtCount(b *testing.B) {
	for _, size := range benchSizes() {
		want := -1
		b.Run(fmt.Sprintf("size=%d/impl=scan", size), func(b *testing.B) {
			db, _ := extDB(b, false, size)
			txn := db.Txn(false)
			b.ReportAllocs()
			for b.Loop() {
				it, err := txn.Get(extTable, "tags", "tag-07")
				must(b, err)
				n := 0
				for obj := it.Next(); obj != nil; obj = it.Next() {
					if r := obj.(*Row); r.Active && r.Meta["region"] == "eu-north" {
						n++
					}
				}
				sinkInt, want = n, n
			}
		})
		b.Run(fmt.Sprintf("size=%d/impl=bitmap", size), func(b *testing.B) {
			db, _ := extDB(b, true, size)
			txn := db.Txn(false)
			b.ReportAllocs()
			for b.Loop() {
				tagged, err := txn.Where(extTable, "tags", "tag-07")
				must(b, err)
				active, _ := txn.Where(extTable, "active", true)
				region, _ := txn.Where(extTable, "region", "eu-north")
				sinkInt = tagged.And(active, region).Len()
			}
			if want >= 0 && sinkInt != want {
				b.Fatalf("bitmap count %d, scan count %d", sinkInt, want)
			}
		})
	}
}

// BenchmarkExtCountOne is the single-column count: the size of one set against
// a walk of one index.
func BenchmarkExtCountOne(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d/impl=scan", size), func(b *testing.B) {
			db, _ := extDB(b, false, size)
			txn := db.Txn(false)
			b.ReportAllocs()
			for b.Loop() {
				it, err := txn.Get(extTable, "region", "eu-north")
				must(b, err)
				n := 0
				for obj := it.Next(); obj != nil; obj = it.Next() {
					n++
				}
				sinkInt = n
			}
		})
		b.Run(fmt.Sprintf("size=%d/impl=bitmap", size), func(b *testing.B) {
			db, _ := extDB(b, true, size)
			txn := db.Txn(false)
			b.ReportAllocs()
			for b.Loop() {
				region, err := txn.Where(extTable, "region", "eu-north")
				must(b, err)
				sinkInt = region.Len()
			}
		})
	}
}

// BenchmarkExtIterate visits the rows of "region eu-north AND active" (a tenth
// of the table): a filtered walk of the region index against the iteration of
// an intersection.
func BenchmarkExtIterate(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d/impl=scan", size), func(b *testing.B) {
			db, _ := extDB(b, false, size)
			txn := db.Txn(false)
			b.ReportAllocs()
			for b.Loop() {
				it, err := txn.Get(extTable, "region", "eu-north")
				must(b, err)
				n := 0
				for obj := it.Next(); obj != nil; obj = it.Next() {
					if obj.(*Row).Active {
						n++
					}
				}
				sinkInt = n
			}
		})
		b.Run(fmt.Sprintf("size=%d/impl=bitmap", size), func(b *testing.B) {
			db, _ := extDB(b, true, size)
			txn := db.Txn(false)
			b.ReportAllocs()
			for b.Loop() {
				region, err := txn.Where(extTable, "region", "eu-north")
				must(b, err)
				active, _ := txn.Where(extTable, "active", true)
				n := 0
				for obj := range region.And(active).All() {
					sinkAny = obj
					n++
				}
				sinkInt = n
			}
		})
	}
}

// BenchmarkExtWrite is what the bitmap indexes cost and save on the write
// side: a row inserted and deleted again, and a row replaced by a copy with
// the same indexed values (which an ordinary index has to follow, because it
// points at the row object, and a bitmap index does not).
func BenchmarkExtWrite(b *testing.B) {
	for _, size := range benchSizes() {
		for _, impl := range []string{"classic", "bitmap"} {
			b.Run(fmt.Sprintf("op=insert-delete/size=%d/impl=%s", size, impl), func(b *testing.B) {
				db, _ := extDB(b, impl == "bitmap", size)
				extra := makeRows(shapeUUID, size, extraRows, groupCount(size), 5)
				b.ReportAllocs()
				j := 0
				for b.Loop() {
					r := extra[j%extraRows]
					j++
					txn := db.Txn(true)
					must(b, txn.Insert(extTable, r))
					txn.Commit()
					txn = db.Txn(true)
					must(b, txn.Delete(extTable, r))
					txn.Commit()
				}
			})
			b.Run(fmt.Sprintf("op=update-same-keys/size=%d/impl=%s", size, impl), func(b *testing.B) {
				db, rows := extDB(b, impl == "bitmap", size)
				alt := make([]*Row, len(rows))
				for i, r := range rows {
					c := *r
					c.Version++
					alt[i] = &c
				}
				b.ReportAllocs()
				j := 0
				for b.Loop() {
					i := j % len(rows)
					next := alt[i]
					if j/len(rows)%2 == 1 {
						next = rows[i]
					}
					j++
					txn := db.Txn(true)
					must(b, txn.Insert(extTable, next))
					txn.Commit()
				}
			})
		}
	}
}

// BenchmarkExtFootprint reports the heap a table takes with each kind of
// index. The timed loop is only there to make it a benchmark.
func BenchmarkExtFootprint(b *testing.B) {
	for _, size := range benchSizes() {
		for _, impl := range []string{"classic", "bitmap"} {
			b.Run(fmt.Sprintf("size=%d/impl=%s", size, impl), func(b *testing.B) {
				clear(extDBs)
				dropFixtures()
				rows := makeRows(shapeUUID, 0, size, groupCount(size), 3)
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				db, err := mm.NewMemDB(extSchema(impl == "bitmap"))
				must(b, err)
				txn := db.Txn(true)
				for _, r := range rows {
					must(b, txn.Insert(extTable, r))
				}
				txn.Commit()
				runtime.GC()
				runtime.ReadMemStats(&after)
				for b.Loop() {
					sinkAny = db
				}
				runtime.KeepAlive(rows)
				b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(size), "heapB/row")
				b.ReportMetric(float64(after.HeapObjects-before.HeapObjects)/float64(size), "heapobjs/row")
			})
		}
	}
}

// BenchmarkExtTypedFirst is a point lookup three ways: the untyped First, the
// same through Table[T] (which still boxes its argument), and through a
// StringKey, which neither boxes nor resolves names.
func BenchmarkExtTypedFirst(b *testing.B) {
	table := mm.NewTable[Row](extTable)
	byID := table.StringKey("id")
	for _, size := range benchSizes() {
		db, rows := extDB(b, false, size)
		txn := db.Txn(false)
		b.Run(fmt.Sprintf("size=%d/impl=untyped", size), func(b *testing.B) {
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				obj, err := txn.First(extTable, "id", rows[j%len(rows)].ID)
				j++
				must(b, err)
				sinkAny = obj
			}
		})
		b.Run(fmt.Sprintf("size=%d/impl=table", size), func(b *testing.B) {
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				r, err := table.First(txn, "id", rows[j%len(rows)].ID)
				j++
				must(b, err)
				sinkAny = r
			}
		})
		b.Run(fmt.Sprintf("size=%d/impl=key", size), func(b *testing.B) {
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				r, err := byID.First(txn, rows[j%len(rows)].ID)
				j++
				must(b, err)
				sinkAny = r
			}
		})
	}
}

// BenchmarkExtTypedInsert compares the two ways of telling the database where
// a string lives: a field name, resolved once and read through an offset, and
// an accessor function.
func BenchmarkExtTypedInsert(b *testing.B) {
	schema := func(typed bool) *mm.DBSchema {
		id := mm.Indexer(&mm.StringFieldIndex{Field: "ID"})
		name := mm.Indexer(&mm.StringFieldIndex{Field: "Name"})
		age := mm.Indexer(&mm.IntFieldIndex{Field: "Age"})
		if typed {
			id = &mm.StringIndex[Row]{Get: func(r *Row) string { return r.ID }}
			name = &mm.StringIndex[Row]{Get: func(r *Row) string { return r.Name }}
			age = &mm.IntIndex[Row, int]{Get: func(r *Row) int { return r.Age }}
		}
		return &mm.DBSchema{Tables: map[string]*mm.TableSchema{extTable: {Name: extTable, Indexes: map[string]*mm.IndexSchema{
			"id":   {Name: "id", Unique: true, Indexer: id},
			"name": {Name: "name", Indexer: name},
			"age":  {Name: "age", Indexer: age},
		}}}}
	}
	rows := makeRows(shapeUUID, 0, 1_000, 10, 3)
	for _, impl := range []string{"field", "func"} {
		b.Run("impl="+impl, func(b *testing.B) {
			db, err := mm.NewMemDB(schema(impl == "func"))
			must(b, err)
			b.ReportAllocs()
			for b.Loop() {
				txn := db.Txn(true)
				for _, r := range rows[:100] {
					must(b, txn.Insert(extTable, r))
				}
				txn.Abort()
			}
		})
	}
}
