// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package bench

import (
	"fmt"
	"testing"
	"time"
)

// BenchmarkFirst is the point lookup on the primary key inside an existing
// read transaction, for every key shape, as a hit and as a miss.
func BenchmarkFirst(b *testing.B) {
	type cfg struct {
		schema string
		sh     shape
	}
	cfgs := []cfg{
		{schemaS3, shapeUUID}, {schemaS3, shapeSeq}, {schemaS3, shapePath},
		{schemaWide, shapeUUID}, {schemaFunc, shapeUUID},
	}
	for _, c := range cfgs {
		for _, size := range benchSizes() {
			for _, mode := range []string{"hit", "miss"} {
				b.Run(fmt.Sprintf("schema=%s/keys=%s/size=%d/%s", c.schema, c.sh, size, mode), func(b *testing.B) {
					f := getFixture(b, c.schema, c.sh, size, "")
					txn := f.db.Txn(false)
					b.ReportAllocs()
					j := 0
					for b.Loop() {
						var id string
						if mode == "hit" {
							id = f.rows[f.perm[j%size]].ID
						} else {
							id = f.extra[j%extraRows].ID
						}
						j++
						obj, err := txn.First(tableMain, "id", id)
						must(b, err)
						if (obj != nil) != (mode == "hit") {
							b.Fatalf("unexpected result for %s", mode)
						}
						sinkAny = obj
					}
				})
			}
		}
	}
}

// BenchmarkTxnFirst includes creating the read transaction, the way request
// handlers typically use the database.
func BenchmarkTxnFirst(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeUUID, size, "")
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				txn := f.db.Txn(false)
				obj, err := txn.First(tableMain, "id", f.rows[f.perm[j%size]].ID)
				j++
				must(b, err)
				sinkAny = obj
			}
		})
	}
}

// BenchmarkFirstIndex covers First on each kind of secondary index.
func BenchmarkFirstIndex(b *testing.B) {
	size := 100_000
	f := getFixture(b, schemaWide, shapeUUID, size, "")
	txn := f.db.Txn(false)
	cases := []struct {
		name string
		run  func(r *Row) (interface{}, error)
	}{
		{"nonunique-string", func(r *Row) (interface{}, error) { return txn.First(tableMain, "group", r.Group) }},
		{"unique-lowercase", func(r *Row) (interface{}, error) { return txn.First(tableMain, "name", r.Name) }},
		{"id-prefix", func(r *Row) (interface{}, error) { return txn.First(tableMain, "id_prefix", r.ID[:8]) }},
		{"string-slice", func(r *Row) (interface{}, error) { return txn.First(tableMain, "tags", r.Tags[0]) }},
		{"string-map", func(r *Row) (interface{}, error) { return txn.First(tableMain, "meta", "region", r.Meta["region"]) }},
		{"int", func(r *Row) (interface{}, error) { return txn.First(tableMain, "age", r.Age) }},
		{"uint", func(r *Row) (interface{}, error) { return txn.First(tableMain, "score", r.Score) }},
		{"bool", func(r *Row) (interface{}, error) { return txn.First(tableMain, "active", r.Active) }},
		{"uuid", func(r *Row) (interface{}, error) { return txn.First(tableMain, "uuid", r.UUID) }},
		{"compound", func(r *Row) (interface{}, error) { return txn.First(tableMain, "group_age", r.Group, r.Age) }},
		{"compound-prefix", func(r *Row) (interface{}, error) { return txn.First(tableMain, "group_age_prefix", r.Group) }},
		{"no-args", func(r *Row) (interface{}, error) { return txn.First(tableMain, "id") }},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				obj, err := c.run(f.rows[f.perm[j%size]])
				j++
				must(b, err)
				if obj == nil {
					b.Fatal("no result")
				}
				sinkAny = obj
			}
		})
	}
}

// BenchmarkLast covers the reverse point lookups.
func BenchmarkLast(b *testing.B) {
	size := 100_000
	f := getFixture(b, schemaWide, shapeUUID, size, "")
	txn := f.db.Txn(false)
	cases := []struct {
		name string
		run  func(r *Row) (interface{}, error)
	}{
		{"unique", func(r *Row) (interface{}, error) { return txn.Last(tableMain, "id", r.ID) }},
		{"nonunique-string", func(r *Row) (interface{}, error) { return txn.Last(tableMain, "group", r.Group) }},
		{"int", func(r *Row) (interface{}, error) { return txn.Last(tableMain, "age", r.Age) }},
		{"no-args", func(r *Row) (interface{}, error) { return txn.Last(tableMain, "id") }},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				obj, err := c.run(f.rows[f.perm[j%size]])
				j++
				must(b, err)
				if obj == nil {
					b.Fatal("no result")
				}
				sinkAny = obj
			}
		})
	}
}

func drain(it ResultIterator) int {
	n := 0
	for obj := it.Next(); obj != nil; obj = it.Next() {
		n++
	}
	return n
}

// BenchmarkGetOne is Get on a unique key plus one Next: the fixed cost of
// constructing an iterator.
func BenchmarkGetOne(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeUUID, size, "")
			txn := f.db.Txn(false)
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				it, err := txn.Get(tableMain, "id", f.rows[f.perm[j%size]].ID)
				j++
				must(b, err)
				obj := it.Next()
				if obj == nil {
					b.Fatal("no result")
				}
				sinkAny = obj
			}
		})
	}
}

// BenchmarkIterate drains iterators; rows/op differs per sub-benchmark and is
// reported so the per-row cost can be derived.
func BenchmarkIterate(b *testing.B) {
	for _, size := range benchSizes() {
		for _, sh := range []shape{shapeUUID, shapeSeq, shapePath} {
			b.Run(fmt.Sprintf("scan=all/keys=%s/size=%d", sh, size), func(b *testing.B) {
				f := getFixture(b, schemaS3, sh, size, "")
				txn := f.db.Txn(false)
				b.ReportAllocs()
				rows := 0
				for b.Loop() {
					it, err := txn.Get(tableMain, "id")
					must(b, err)
					rows = drain(it)
				}
				if rows != size {
					b.Fatalf("scanned %d rows, want %d", rows, size)
				}
				b.ReportMetric(float64(rows), "rows/op")
			})
		}

		type scan struct {
			name string
			open func(txn *Txn, f *fixture, j int) (ResultIterator, error)
		}
		scans := []scan{
			{"group", func(txn *Txn, f *fixture, j int) (ResultIterator, error) {
				return txn.Get(tableMain, "group", groupName(j%f.groups))
			}},
			{"group-reverse", func(txn *Txn, f *fixture, j int) (ResultIterator, error) {
				return txn.GetReverse(tableMain, "group", groupName(j%f.groups))
			}},
			{"tags", func(txn *Txn, f *fixture, j int) (ResultIterator, error) {
				return txn.Get(tableMain, "tags", fmt.Sprintf("tag-%02d", j%50))
			}},
			{"all-reverse", func(txn *Txn, f *fixture, j int) (ResultIterator, error) {
				return txn.GetReverse(tableMain, "id")
			}},
		}
		for _, sc := range scans {
			b.Run(fmt.Sprintf("scan=%s/size=%d", sc.name, size), func(b *testing.B) {
				f := getFixture(b, schemaS3, shapeUUID, size, "")
				txn := f.db.Txn(false)
				b.ReportAllocs()
				j, rows := 0, 0
				for b.Loop() {
					it, err := sc.open(txn, f, j)
					j++
					must(b, err)
					rows = drain(it)
				}
				if rows == 0 {
					b.Fatal("empty scan")
				}
				b.ReportMetric(float64(rows), "rows/op")
			})
		}
	}
}

// BenchmarkLowerBound seeks and reads ten rows, forwards and backwards.
func BenchmarkLowerBound(b *testing.B) {
	size := 100_000
	f := getFixture(b, schemaWide, shapeUUID, size, "")
	txn := f.db.Txn(false)
	cases := []struct {
		name string
		open func(r *Row) (ResultIterator, error)
	}{
		{"int", func(r *Row) (ResultIterator, error) { return txn.LowerBound(tableMain, "age", r.Age) }},
		{"int-reverse", func(r *Row) (ResultIterator, error) { return txn.ReverseLowerBound(tableMain, "age", r.Age) }},
		{"uint", func(r *Row) (ResultIterator, error) { return txn.LowerBound(tableMain, "score", r.Score/2) }},
		{"string", func(r *Row) (ResultIterator, error) { return txn.LowerBound(tableMain, "id", r.ID[:6]) }},
		{"string-reverse", func(r *Row) (ResultIterator, error) { return txn.ReverseLowerBound(tableMain, "id", r.ID[:6]) }},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				it, err := c.open(f.rows[f.perm[j%size]])
				j++
				must(b, err)
				for k := 0; k < 10; k++ {
					sinkAny = it.Next()
				}
			}
		})
	}
}

// BenchmarkLongestPrefix uses a terminator-free custom indexer, the only kind
// LongestPrefix is meaningful for.
func BenchmarkLongestPrefix(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			f := getFixture(b, schemaFunc, shapeUUID, size, "")
			txn := f.db.Txn(false)
			// Keys are built up front: formatting one costs more than the
			// lookup being measured.
			keys := make([]string, 4096)
			for k := range keys {
				i := f.perm[k%size]
				keys[k] = fmt.Sprintf("/svc%02d/region%d/node%07d/disk/0", i%50, i%7, i)
			}
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				key := keys[j%len(keys)]
				j++
				obj, err := txn.LongestPrefix("paths", "id_prefix", key)
				must(b, err)
				if obj == nil {
					b.Fatal("no result")
				}
				sinkAny = obj
			}
		})
	}
}

// BenchmarkFilterIterator drains a filtered group scan.
func BenchmarkFilterIterator(b *testing.B) {
	f := getFixture(b, schemaS3, shapeUUID, 100_000, "")
	txn := f.db.Txn(false)
	odd := func(raw interface{}) bool { return raw.(*Row).Age%2 == 1 }
	b.ReportAllocs()
	j := 0
	for b.Loop() {
		it, err := txn.Get(tableMain, "group", groupName(j%f.groups))
		j++
		must(b, err)
		fi := NewFilterIterator(it, odd)
		n := 0
		for obj := fi.Next(); obj != nil; obj = fi.Next() {
			n++
		}
		sinkInt = n
	}
}

// BenchmarkParallelTxnFirst measures read scalability: every iteration opens
// its own read transaction. Run with -cpu 1,4,8.
func BenchmarkParallelTxnFirst(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeUUID, size, "")
			b.ReportAllocs()
			// RunParallel cannot use b.Loop, so the setup above has to be
			// taken off the clock by hand.
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				j := 0
				for pb.Next() {
					txn := f.db.Txn(false)
					obj, err := txn.First(tableMain, "id", f.rows[f.perm[j%size]].ID)
					j += 7
					if err != nil || obj == nil {
						panic("lookup failed")
					}
				}
			})
		})
	}
}

// BenchmarkParallelTxnFirstWithWriter is the same read load while one writer
// commits single-row updates at a fixed rate (~2000 commits/s). The rate is
// fixed so that a faster writer does not translate into more reader churn.
func BenchmarkParallelTxnFirstWithWriter(b *testing.B) {
	size := 100_000
	f := getFixture(b, schemaS3, shapeUUID, size, "parallel-writer")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(500 * time.Microsecond)
		defer tick.Stop()
		for j := 0; ; j++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			i := f.perm[j%size]
			row := f.alt[i]
			if (j/size)%2 == 1 {
				row = f.rows[i]
			}
			txn := f.db.Txn(true)
			if err := txn.Insert(tableMain, row); err != nil {
				panic(err)
			}
			txn.Commit()
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		j := 0
		for pb.Next() {
			txn := f.db.Txn(false)
			obj, err := txn.First(tableMain, "id", f.rows[f.perm[j%size]].ID)
			j += 7
			if err != nil || obj == nil {
				panic("lookup failed")
			}
		}
	})
	b.StopTimer()
	close(stop)
	<-done
}
