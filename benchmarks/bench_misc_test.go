// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package bench

import (
	"fmt"
	"testing"
)

// BenchmarkNewMemDB measures database construction (schema validation plus
// whatever per-index setup the implementation performs).
func BenchmarkNewMemDB(b *testing.B) {
	for _, sc := range []string{schemaS1, schemaWide, schemaS50} {
		b.Run("schema="+sc, func(b *testing.B) {
			schema := buildSchema(sc)
			b.ReportAllocs()
			for b.Loop() {
				db, err := NewMemDB(schema)
				must(b, err)
				sinkAny = db
			}
		})
	}
}

// BenchmarkTxn measures bare transaction overhead.
func BenchmarkTxn(b *testing.B) {
	f := getFixture(b, schemaS3, shapeUUID, 100_000, "")
	b.Run("read", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			txn := f.db.Txn(false)
			txn.Abort()
			sinkAny = txn
		}
	})
	b.Run("write-commit-empty", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			txn := f.db.Txn(true)
			txn.Commit()
			sinkAny = txn
		}
	})
	b.Run("write-abort-empty", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			txn := f.db.Txn(true)
			txn.Abort()
			sinkAny = txn
		}
	})
	b.Run("write-commit-defer", func(b *testing.B) {
		b.ReportAllocs()
		n := 0
		for b.Loop() {
			txn := f.db.Txn(true)
			txn.Defer(func() { n++ })
			txn.Commit()
		}
		sinkInt = n
	})
}

// BenchmarkSnapshot measures MemDB.Snapshot, which is O(1): one small
// allocation. It deliberately runs against the small database. A benchmark
// that does nothing but allocate 32 bytes is sensitive to how often the
// garbage collector runs, which depends on the size of the live heap -- and
// the two implementations' 100,000-row databases differ in size by a factor of
// two, so snapshotting those would measure the fixtures, not Snapshot.
func BenchmarkSnapshot(b *testing.B) {
	f := getFixture(b, schemaS3, shapeUUID, 1_000, "")
	b.ReportAllocs()
	for b.Loop() {
		sinkAny = f.db.Snapshot()
	}
}

// BenchmarkSnapshotWrite writes to a snapshot database (no watch tracking).
func BenchmarkSnapshotWrite(b *testing.B) {
	f := getFixture(b, schemaS3, shapeUUID, 100_000, "")
	b.ReportAllocs()
	j := 0
	for b.Loop() {
		snap := f.db.Snapshot()
		txn := snap.Txn(true)
		must(b, txn.Insert(tableMain, f.extra[j%extraRows]))
		j++
		txn.Commit()
	}
}

// BenchmarkIndexer measures the exported indexer methods directly. These are
// public API: callers use them outside of transactions, and upstream's own two
// indexer benchmarks live at this level.
func BenchmarkIndexer(b *testing.B) {
	rows := makeRows(shapeUUID, 0, 64, 10, 9)
	type fromObject interface {
		FromObject(interface{}) (bool, []byte, error)
	}
	type fromObjectMulti interface {
		FromObject(interface{}) (bool, [][]byte, error)
	}
	compound := &CompoundIndex{Indexes: []Indexer{
		&StringFieldIndex{Field: "Group"}, &IntFieldIndex{Field: "Age"}, &StringFieldIndex{Field: "Name"},
	}}
	compoundMulti := &CompoundMultiIndex{Indexes: []Indexer{
		&StringFieldIndex{Field: "Group"}, &StringSliceFieldIndex{Field: "Tags"}, &StringFieldIndex{Field: "Name"},
	}}

	singles := []struct {
		name string
		idx  fromObject
	}{
		{"StringFieldIndex", &StringFieldIndex{Field: "ID"}},
		{"StringFieldIndex-lowercase", &StringFieldIndex{Field: "Name", Lowercase: true}},
		{"StringFieldIndex-pointer", &StringFieldIndex{Field: "Parent"}},
		{"IntFieldIndex", &IntFieldIndex{Field: "Age"}},
		{"UintFieldIndex", &UintFieldIndex{Field: "Score"}},
		{"BoolFieldIndex", &BoolFieldIndex{Field: "Active"}},
		{"UUIDFieldIndex", &UUIDFieldIndex{Field: "UUID"}},
		{"FieldSetIndex", &FieldSetIndex{Field: "Parent"}},
		{"ConditionalIndex", &ConditionalIndex{Conditional: func(obj interface{}) (bool, error) { return obj.(*Row).Active, nil }}},
		{"CompoundIndex", compound},
	}
	for _, c := range singles {
		b.Run("FromObject/"+c.name, func(b *testing.B) {
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				_, v, err := c.idx.FromObject(rows[j%len(rows)])
				j++
				must(b, err)
				sinkInt = len(v)
			}
		})
	}

	multis := []struct {
		name string
		idx  fromObjectMulti
	}{
		{"StringSliceFieldIndex", &StringSliceFieldIndex{Field: "Tags"}},
		{"StringMapFieldIndex", &StringMapFieldIndex{Field: "Meta"}},
		{"CompoundMultiIndex", compoundMulti},
	}
	for _, c := range multis {
		b.Run("FromObject/"+c.name, func(b *testing.B) {
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				_, v, err := c.idx.FromObject(rows[j%len(rows)])
				j++
				must(b, err)
				sinkInt = len(v)
			}
		})
	}

	args := []struct {
		name string
		idx  Indexer
		args func(r *Row) []interface{}
	}{
		{"StringFieldIndex", &StringFieldIndex{Field: "ID"}, func(r *Row) []interface{} { return []interface{}{r.ID} }},
		{"StringFieldIndex-lowercase", &StringFieldIndex{Field: "Name", Lowercase: true}, func(r *Row) []interface{} { return []interface{}{r.Name} }},
		{"IntFieldIndex", &IntFieldIndex{Field: "Age"}, func(r *Row) []interface{} { return []interface{}{r.Age} }},
		{"UintFieldIndex", &UintFieldIndex{Field: "Score"}, func(r *Row) []interface{} { return []interface{}{r.Score} }},
		{"BoolFieldIndex", &BoolFieldIndex{Field: "Active"}, func(r *Row) []interface{} { return []interface{}{r.Active} }},
		{"UUIDFieldIndex-string", &UUIDFieldIndex{Field: "UUID"}, func(r *Row) []interface{} { return []interface{}{r.UUID} }},
		{"StringMapFieldIndex", &StringMapFieldIndex{Field: "Meta"}, func(r *Row) []interface{} { return []interface{}{"region", r.Meta["region"]} }},
		{"CompoundIndex", compound, func(r *Row) []interface{} { return []interface{}{r.Group, r.Age, r.Name} }},
		{"CompoundMultiIndex", compoundMulti, func(r *Row) []interface{} { return []interface{}{r.Group, r.Tags[0], r.Name} }},
	}
	for _, c := range args {
		b.Run("FromArgs/"+c.name, func(b *testing.B) {
			prepared := make([][]interface{}, len(rows))
			for i, r := range rows {
				prepared[i] = c.args(r)
			}
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				v, err := c.idx.FromArgs(prepared[j%len(prepared)]...)
				j++
				must(b, err)
				sinkInt = len(v)
			}
		})
	}

	b.Run("PrefixFromArgs/UUIDFieldIndex", func(b *testing.B) {
		idx := &UUIDFieldIndex{Field: "UUID"}
		b.ReportAllocs()
		j := 0
		for b.Loop() {
			v, err := idx.PrefixFromArgs(rows[j%len(rows)].UUID[:13])
			j++
			must(b, err)
			sinkInt = len(v)
		}
	})
}

// TestImpl reports which implementation the suite was built against, so that
// result files are self-describing.
func TestImpl(t *testing.T) {
	t.Logf("implementation: %s", Impl)
	fmt.Println("impl:", Impl)
}
