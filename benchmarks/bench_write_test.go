// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package bench

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// All write benchmarks are steady-state: they leave the database at the same
// size they found it, so the faster implementation is not punished with a
// bigger tree just because it completes more iterations.

var writeSchemas = []string{schemaS1, schemaS3, schemaWide, schemaFunc}

// BenchmarkInsertAbort measures the insert path without commit: open a write
// txn, insert batch new rows, abort. batch=1 is dominated by the first
// copy-on-write descent, batch=100 by in-transaction node reuse.
func BenchmarkInsertAbort(b *testing.B) {
	for _, sc := range writeSchemas {
		for _, size := range benchSizes() {
			for _, batch := range []int{1, 100} {
				b.Run(fmt.Sprintf("schema=%s/size=%d/batch=%d", sc, size, batch), func(b *testing.B) {
					f := getFixture(b, sc, shapeUUID, size, "")
					b.ReportAllocs()
					j := 0
					for b.Loop() {
						txn := f.db.Txn(true)
						for k := 0; k < batch; k++ {
							must(b, txn.Insert(tableMain, f.extra[j%extraRows]))
							j++
						}
						txn.Abort()
					}
				})
			}
		}
	}
}

// BenchmarkInsertDeleteCommit measures two full single-row write transactions
// against the primary database: insert+commit, then delete+commit.
func BenchmarkInsertDeleteCommit(b *testing.B) {
	type cfg struct {
		schema string
		sh     shape
	}
	cfgs := []cfg{
		{schemaS1, shapeUUID}, {schemaS3, shapeUUID}, {schemaWide, shapeUUID}, {schemaFunc, shapeUUID},
		{schemaS3, shapeSeq}, {schemaS3, shapePath},
	}
	for _, c := range cfgs {
		for _, size := range benchSizes() {
			b.Run(fmt.Sprintf("schema=%s/keys=%s/size=%d", c.schema, c.sh, size), func(b *testing.B) {
				f := getFixture(b, c.schema, c.sh, size, "")
				b.ReportAllocs()
				j := 0
				for b.Loop() {
					r := f.extra[j%extraRows]
					j++
					txn := f.db.Txn(true)
					must(b, txn.Insert(tableMain, r))
					txn.Commit()
					txn = f.db.Txn(true)
					must(b, txn.Delete(tableMain, r))
					txn.Commit()
				}
			})
		}
	}
}

// BenchmarkInsertDeleteCommitManyTables is the same cycle in a 50-table
// database: it exposes any per-commit cost that scales with schema size.
func BenchmarkInsertDeleteCommitManyTables(b *testing.B) {
	f := getFixture(b, schemaS50, shapeUUID, 1_000, "")
	b.ReportAllocs()
	j := 0
	for b.Loop() {
		r := f.extra[j%extraRows]
		j++
		txn := f.db.Txn(true)
		must(b, txn.Insert(tableMain, r))
		txn.Commit()
		txn = f.db.Txn(true)
		must(b, txn.Delete(tableMain, r))
		txn.Commit()
	}
}

// BenchmarkUpdateCommit updates one existing row per transaction.
// keys=same replaces the object while every index key stays identical;
// keys=moved changes three secondary keys (delete + insert per index).
func BenchmarkUpdateCommit(b *testing.B) {
	for _, sc := range []string{schemaS3, schemaWide, schemaFunc} {
		for _, size := range benchSizes() {
			for _, mode := range []string{"same", "moved"} {
				b.Run(fmt.Sprintf("schema=%s/size=%d/keys=%s", sc, size, mode), func(b *testing.B) {
					f := getFixture(b, sc, shapeUUID, size, "update-"+mode)
					other := f.alt
					if mode == "moved" {
						other = f.moved
					}
					b.ReportAllocs()
					j := 0
					for b.Loop() {
						i := f.perm[j%size]
						// Alternate per full pass so every update really changes the row.
						row := other[i]
						if (j/size)%2 == 1 {
							row = f.rows[i]
						}
						j++
						txn := f.db.Txn(true)
						must(b, txn.Insert(tableMain, row))
						txn.Commit()
					}
				})
			}
		}
	}
}

// BenchmarkDeleteAbort deletes one existing row and aborts.
func BenchmarkDeleteAbort(b *testing.B) {
	for _, sc := range []string{schemaS3, schemaWide} {
		for _, size := range benchSizes() {
			b.Run(fmt.Sprintf("schema=%s/size=%d", sc, size), func(b *testing.B) {
				f := getFixture(b, sc, shapeUUID, size, "")
				b.ReportAllocs()
				j := 0
				for b.Loop() {
					txn := f.db.Txn(true)
					must(b, txn.Delete(tableMain, f.rows[f.perm[j%size]]))
					j++
					txn.Abort()
				}
			})
		}
	}
}

// BenchmarkDeleteAllAbort removes the ~100 rows of one group and aborts.
func BenchmarkDeleteAllAbort(b *testing.B) {
	for _, sc := range []string{schemaS3, schemaWide} {
		for _, size := range benchSizes() {
			b.Run(fmt.Sprintf("schema=%s/size=%d", sc, size), func(b *testing.B) {
				f := getFixture(b, sc, shapeUUID, size, "")
				b.ReportAllocs()
				j := 0
				for b.Loop() {
					txn := f.db.Txn(true)
					n, err := txn.DeleteAll(tableMain, "group", groupName(j%f.groups))
					must(b, err)
					if n == 0 {
						b.Fatal("DeleteAll matched nothing")
					}
					j++
					txn.Abort()
				}
			})
		}
	}
}

// BenchmarkDeletePrefixAbort removes 100 rows sharing an id prefix and aborts.
func BenchmarkDeletePrefixAbort(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("schema=%s/size=%d", schemaS3, size), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeSeq, size, "")
			hundreds := size / 100
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				// "row-%010d" minus its last two digits selects 100 rows.
				prefix := fmt.Sprintf("row-%08d", j%hundreds)
				j++
				txn := f.db.Txn(true)
				ok, err := txn.DeletePrefix(tableMain, "id_prefix", prefix)
				must(b, err)
				if !ok {
					b.Fatal("DeletePrefix matched nothing")
				}
				txn.Abort()
			}
		})
	}
}

// BenchmarkChanges measures change tracking: n inserts with TrackChanges plus
// the Changes() call. dup=true writes every row twice so that Changes() has to
// collapse duplicates.
func BenchmarkChanges(b *testing.B) {
	for _, n := range []int{10, 1000} {
		for _, dup := range []bool{false, true} {
			b.Run(fmt.Sprintf("n=%d/dup=%v", n, dup), func(b *testing.B) {
				f := getFixture(b, schemaS3, shapeUUID, 100_000, "")
				b.ReportAllocs()
				for b.Loop() {
					txn := f.db.Txn(true)
					txn.TrackChanges()
					for k := 0; k < n; k++ {
						must(b, txn.Insert(tableMain, f.extra[k]))
						if dup {
							must(b, txn.Insert(tableMain, f.extra[k]))
						}
					}
					sinkInt = len(txn.Changes())
					txn.Abort()
				}
			})
		}
	}
}

// BenchmarkReadYourWrites interleaves writes with reads of the same index
// inside one write transaction (upstream clones the index txn on every read).
func BenchmarkReadYourWrites(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeUUID, size, "")
			b.ReportAllocs()
			for b.Loop() {
				txn := f.db.Txn(true)
				for k := 0; k < 10; k++ {
					r := f.extra[k]
					must(b, txn.Insert(tableMain, r))
					obj, err := txn.First(tableMain, "id", r.ID)
					must(b, err)
					if obj == nil {
						b.Fatal("own write not visible")
					}
				}
				txn.Abort()
			}
		})
	}
}

// BenchmarkIterateInWriteTxn creates an iterator over a dirty index.
func BenchmarkIterateInWriteTxn(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeUUID, size, "")
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				txn := f.db.Txn(true)
				must(b, txn.Insert(tableMain, f.extra[j%extraRows]))
				it, err := txn.Get(tableMain, "group", groupName(j%f.groups))
				must(b, err)
				n := 0
				for obj := it.Next(); obj != nil; obj = it.Next() {
					n++
				}
				sinkInt = n
				must(b, txn.Insert(tableMain, f.extra[(j+1)%extraRows]))
				j++
				txn.Abort()
			}
		})
	}
}

// BenchmarkTxnSnapshot snapshots a write transaction that has pending writes.
func BenchmarkTxnSnapshot(b *testing.B) {
	f := getFixture(b, schemaWide, shapeUUID, 100_000, "")
	b.ReportAllocs()
	j := 0
	for b.Loop() {
		txn := f.db.Txn(true)
		must(b, txn.Insert(tableMain, f.extra[j%extraRows]))
		j++
		sinkAny = txn.Snapshot()
		txn.Abort()
	}
}

// BenchmarkBulkLoad loads size rows in one transaction into a fresh database.
// Besides the load time it reports, from the first load of each run, the
// resulting heap footprint and the duration of a full garbage collection with
// that database (and nothing else of note) live.
func BenchmarkBulkLoad(b *testing.B) {
	for _, sc := range []string{schemaS3, schemaWide} {
		for _, size := range benchSizes() {
			b.Run(fmt.Sprintf("schema=%s/size=%d", sc, size), func(b *testing.B) {
				dropFixtures()
				rows := makeRows(shapeUUID, 0, size, groupCount(size), 3)
				schema := buildSchema(sc)
				load := func() *MemDB {
					db, err := NewMemDB(schema)
					must(b, err)
					txn := db.Txn(true)
					for _, r := range rows {
						must(b, txn.Insert(tableMain, r))
					}
					txn.Commit()
					return db
				}

				// Footprint, measured once and outside the timed loop.
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				db := load()
				runtime.GC()
				runtime.ReadMemStats(&after)
				start := time.Now()
				runtime.GC()
				gcMillis := float64(time.Since(start).Microseconds()) / 1000
				runtime.KeepAlive(db)
				db = nil

				b.ReportAllocs()
				for b.Loop() {
					sinkAny = load()
				}
				sinkAny = nil
				b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(size), "heapB/row")
				b.ReportMetric(float64(after.HeapObjects-before.HeapObjects)/float64(size), "heapobjs/row")
				b.ReportMetric(gcMillis, "gc-ms")
			})
		}
	}
}
