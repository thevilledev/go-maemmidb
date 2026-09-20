// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The typed indexers promise the keys of their reflection-based twins. These
// tests hold them to it three ways: exported method against exported method,
// the transactions' fast path against the exported methods, and a database
// built from typed indexers against one built from field indexers.

// typedTwins pairs every typed indexer with the field indexer it mirrors.
func typedTwins() []struct {
	name         string
	typed, field Indexer
} {
	return []struct {
		name         string
		typed, field Indexer
	}{
		{"string", &StringIndex[exObj]{Get: func(o *exObj) string { return o.ID }}, &StringFieldIndex{Field: "ID"}},
		{"string-lower", &StringIndex[exObj]{Get: func(o *exObj) string { return o.ID }, Lowercase: true}, &StringFieldIndex{Field: "ID", Lowercase: true}},
		{"string-empty", &StringIndex[exObj]{Get: func(o *exObj) string { return o.Empty }}, &StringFieldIndex{Field: "Empty"}},
		{"strings", &StringSliceIndex[exObj]{Get: func(o *exObj) []string { return o.Tags }}, &StringSliceFieldIndex{Field: "Tags"}},
		{"strings-lower", &StringSliceIndex[exObj]{Get: func(o *exObj) []string { return o.Tags }, Lowercase: true}, &StringSliceFieldIndex{Field: "Tags", Lowercase: true}},
		{"strings-none", &StringSliceIndex[exObj]{Get: func(o *exObj) []string { return o.NoTags }}, &StringSliceFieldIndex{Field: "NoTags"}},
		{"int", &IntIndex[exObj, int]{Get: func(o *exObj) int { return o.I }}, &IntFieldIndex{Field: "I"}},
		{"int8", &IntIndex[exObj, int8]{Get: func(o *exObj) int8 { return o.I8 }}, &IntFieldIndex{Field: "I8"}},
		{"int16", &IntIndex[exObj, int16]{Get: func(o *exObj) int16 { return o.I16 }}, &IntFieldIndex{Field: "I16"}},
		{"int32", &IntIndex[exObj, int32]{Get: func(o *exObj) int32 { return o.I32 }}, &IntFieldIndex{Field: "I32"}},
		{"int64", &IntIndex[exObj, int64]{Get: func(o *exObj) int64 { return o.I64 }}, &IntFieldIndex{Field: "I64"}},
		{"uint", &UintIndex[exObj, uint]{Get: func(o *exObj) uint { return o.U }}, &UintFieldIndex{Field: "U"}},
		{"uint8", &UintIndex[exObj, uint8]{Get: func(o *exObj) uint8 { return o.U8 }}, &UintFieldIndex{Field: "U8"}},
		{"uint16", &UintIndex[exObj, uint16]{Get: func(o *exObj) uint16 { return o.U16 }}, &UintFieldIndex{Field: "U16"}},
		{"uint32", &UintIndex[exObj, uint32]{Get: func(o *exObj) uint32 { return o.U32 }}, &UintFieldIndex{Field: "U32"}},
		{"uint64", &UintIndex[exObj, uint64]{Get: func(o *exObj) uint64 { return o.U64 }}, &UintFieldIndex{Field: "U64"}},
		{"bool", &BoolIndex[exObj]{Get: func(o *exObj) bool { return o.B }}, &BoolFieldIndex{Field: "B"}},
	}
}

func TestIntegerWidths(t *testing.T) {
	type myInt int16
	type myUint uint32
	got := []int{
		signedWidth[int8](), signedWidth[int16](), signedWidth[int32](), signedWidth[int64](), signedWidth[myInt](),
		unsignedWidth[uint8](), unsignedWidth[uint16](), unsignedWidth[uint32](), unsignedWidth[uint64](), unsignedWidth[myUint](),
	}
	want := []int{1, 2, 4, 8, 2, 1, 2, 4, 8, 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("widths %v, want %v", got, want)
	}
	if w, u := signedWidth[int](), unsignedWidth[uint](); w != int(reflect.TypeOf(int(0)).Size()) || u != w {
		t.Fatalf("int is %d bytes, uint %d", w, u)
	}
}

func TestTypedIndexersMatchFieldIndexers(t *testing.T) {
	obj := exampleObj()
	for _, twin := range typedTwins() {
		// Objects: by pointer and by value.
		for _, o := range []interface{}{obj, *obj} {
			wantOK, want, wantErr, _ := fromObject(twin.field, o)
			gotOK, got, gotErr, panicked := fromObject(twin.typed, o)
			if panicked != nil || gotErr != "" || wantErr != "" {
				t.Fatalf("%s: unexpected failure: %v %q %q", twin.name, panicked, gotErr, wantErr)
			}
			if gotOK != wantOK || !reflect.DeepEqual(got, want) {
				t.Errorf("%s (%T): typed %v %q, field %v %q", twin.name, o, gotOK, got, wantOK, want)
			}
		}

		// The transactions' fast path must claim a *T and agree.
		for _, suffix := range [][]byte{nil, []byte("|pk")} {
			e := compileExtractor(twin.typed)
			ok, handled, keys, errText := extractKeys(e, obj, suffix)
			if !handled || errText != "" {
				t.Fatalf("%s: fast path did not take a *exObj (%q)", twin.name, errText)
			}
			wantOK, want, _, _ := fromObject(twin.typed, obj)
			for i := range want {
				want[i] += string(suffix)
			}
			if ok != wantOK || !reflect.DeepEqual(keys, want) {
				t.Errorf("%s: fast path %v %q, FromObject %v %q", twin.name, ok, keys, wantOK, want)
			}
			// ... and must leave everything else to FromObject.
			for _, other := range []interface{}{*obj, (*exObj)(nil), "string", nil, &exInner{}} {
				if _, handled, _, _ := extractKeys(e, other, suffix); handled {
					t.Errorf("%s: fast path claimed a %T", twin.name, other)
				}
			}
		}
	}
}

func TestTypedIndexersRejectForeignObjects(t *testing.T) {
	for _, twin := range typedTwins() {
		for _, other := range []interface{}{(*exObj)(nil), "string", 7, nil, &exInner{}} {
			_, _, errText, panicked := fromObject(twin.typed, other)
			if panicked != nil {
				t.Errorf("%s: FromObject(%T) panicked: %v", twin.name, other, panicked)
			}
			if errText == "" {
				t.Errorf("%s: FromObject(%T) did not fail", twin.name, other)
			}
		}
	}
	if _, _, err := (&StringIndex[exObj]{}).FromObject(exampleObj()); err == nil {
		t.Error("a StringIndex without a function did not fail")
	}
}

func TestTypedArgsMatchFieldIndexers(t *testing.T) {
	type named int
	argSets := [][]interface{}{
		{}, {nil}, {"abc"}, {"ABC"}, {""}, {"ÅÄÖ-ǅ"}, {"a", "B"},
		{true}, {false}, {1.5}, {named(4)},
	}
	for _, twin := range typedTwins() {
		sets := argSets
		// Integers: an argument of exactly the indexed type must encode like
		// the field indexer encodes it.
		switch twin.name {
		case "int":
			sets = append(sets, []interface{}{-42}, []interface{}{0})
		case "int8":
			sets = append(sets, []interface{}{int8(-128)}, []interface{}{int8(127)})
		case "int16":
			sets = append(sets, []interface{}{int16(300)})
		case "int32":
			sets = append(sets, []interface{}{int32(-70000)})
		case "int64":
			sets = append(sets, []interface{}{int64(1 << 40)})
		case "uint":
			sets = append(sets, []interface{}{uint(42)})
		case "uint8":
			sets = append(sets, []interface{}{uint8(255)})
		case "uint16":
			sets = append(sets, []interface{}{uint16(65535)})
		case "uint32":
			sets = append(sets, []interface{}{uint32(1 << 31)})
		case "uint64":
			sets = append(sets, []interface{}{uint64(1<<64 - 1)})
		}
		e := compileExtractor(twin.typed)
		for _, args := range sets {
			for _, prefix := range []bool{false, true} {
				name := fmt.Sprintf("%s args=%#v prefix=%v", twin.name, args, prefix)
				var got, want []byte
				var gotErr, wantErr error
				if prefix {
					tp, isPrefix := twin.typed.(PrefixIndexer)
					fp, fieldIsPrefix := twin.field.(PrefixIndexer)
					if isPrefix != fieldIsPrefix {
						t.Fatalf("%s: prefix support differs from the field indexer's", name)
					}
					if !isPrefix {
						if _, handled := e.appendArgs(nil, args, true); handled {
							t.Errorf("%s: fast path handled an unsupported prefix query", name)
						}
						continue
					}
					got, gotErr = tp.PrefixFromArgs(args...)
					want, wantErr = fp.PrefixFromArgs(args...)
				} else {
					got, gotErr = twin.typed.FromArgs(args...)
					want, wantErr = twin.field.FromArgs(args...)
				}
				if (gotErr == nil) != (wantErr == nil) {
					t.Errorf("%s: typed error %v, field error %v", name, gotErr, wantErr)
					continue
				}
				isNamed := false
				if len(args) == 1 {
					_, isNamed = args[0].(named)
				}
				if isNamed && twin.name != "int" {
					// The field indexer encodes at the width of the argument
					// (an int here), the typed one at the width of the index.
					continue
				}
				if string(got) != string(want) {
					t.Errorf("%s: typed %q, field %q", name, got, want)
				}
				fast, handled := e.appendArgs([]byte("pre"), args, prefix)
				switch {
				case !handled && string(fast) != "pre":
					t.Errorf("%s: unhandled call modified the buffer", name)
				case handled && gotErr != nil:
					t.Errorf("%s: fast path handled an input FromArgs rejects: %v", name, gotErr)
				case handled && string(fast) != "pre"+string(got):
					t.Errorf("%s: fast path %q, FromArgs %q", name, fast[3:], got)
				case !handled && gotErr == nil && !isNamed:
					// (Named integer types are left to FromArgs.)
					t.Errorf("%s: fast path declined a valid query", name)
				}
			}
		}
	}
}

// TestTypedIntArgsAnyWidth covers the one deliberate difference from the field
// indexers: a query may pass any integer type, and out-of-range values are an
// error instead of a silently different key.
func TestTypedIntArgsAnyWidth(t *testing.T) {
	ix := &IntIndex[exObj, int16]{Get: func(o *exObj) int16 { return o.I16 }}
	want, err := ix.FromArgs(int16(300))
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []interface{}{300, int32(300), int64(300)} {
		got, err := ix.FromArgs(arg)
		if err != nil || string(got) != string(want) {
			t.Errorf("FromArgs(%T) = %q, %v; want %q", arg, got, err, want)
		}
		fast, handled := compileExtractor(ix).appendArgs(nil, []interface{}{arg}, false)
		if !handled || string(fast) != string(want) {
			t.Errorf("fast path (%T) = %q, %v", arg, fast, handled)
		}
	}
	for _, arg := range []interface{}{40000, int64(-40000), uint(3), "3"} {
		if _, err := ix.FromArgs(arg); err == nil {
			t.Errorf("FromArgs(%#v) did not fail", arg)
		}
		if _, handled := compileExtractor(ix).appendArgs(nil, []interface{}{arg}, false); handled {
			t.Errorf("fast path handled %#v", arg)
		}
	}

	ux := &UintIndex[exObj, uint8]{Get: func(o *exObj) uint8 { return o.U8 }}
	if got, err := ux.FromArgs(uint64(255)); err != nil || string(got) != "\xff" {
		t.Errorf("FromArgs(uint64(255)) = %q, %v", got, err)
	}
	if _, err := ux.FromArgs(uint(256)); err == nil {
		t.Error("FromArgs(uint(256)) did not fail for a uint8 index")
	}
}

// typedRow is the row type of the end-to-end tests.
type typedRow struct {
	ID     string
	Group  string
	Tags   []string
	Age    int32
	Score  uint16
	Active bool
}

func typedSchemas() (typed, field *DBSchema) {
	build := func(id, group, tags, age, score, active, compound, multi Indexer) *DBSchema {
		return &DBSchema{Tables: map[string]*TableSchema{"rows": {Name: "rows", Indexes: map[string]*IndexSchema{
			"id":       {Name: "id", Unique: true, Indexer: id},
			"group":    {Name: "group", Indexer: group},
			"tags":     {Name: "tags", AllowMissing: true, Indexer: tags},
			"age":      {Name: "age", Indexer: age},
			"score":    {Name: "score", Indexer: score},
			"active":   {Name: "active", Indexer: active},
			"compound": {Name: "compound", Indexer: compound},
			"multi":    {Name: "multi", AllowMissing: true, Indexer: multi},
		}}}}
	}
	tID := &StringIndex[typedRow]{Get: func(r *typedRow) string { return r.ID }}
	tGroup := &StringIndex[typedRow]{Get: func(r *typedRow) string { return r.Group }, Lowercase: true}
	tTags := &StringSliceIndex[typedRow]{Get: func(r *typedRow) []string { return r.Tags }}
	tAge := &IntIndex[typedRow, int32]{Get: func(r *typedRow) int32 { return r.Age }}
	tScore := &UintIndex[typedRow, uint16]{Get: func(r *typedRow) uint16 { return r.Score }}
	tActive := &BoolIndex[typedRow]{Get: func(r *typedRow) bool { return r.Active }}
	typed = build(tID, tGroup, tTags, tAge, tScore, tActive,
		&CompoundIndex{Indexes: []Indexer{tGroup, tAge, tActive}},
		&CompoundMultiIndex{Indexes: []Indexer{tGroup, tTags}})

	fID := &StringFieldIndex{Field: "ID"}
	fGroup := &StringFieldIndex{Field: "Group", Lowercase: true}
	fTags := &StringSliceFieldIndex{Field: "Tags"}
	fAge := &IntFieldIndex{Field: "Age"}
	fScore := &UintFieldIndex{Field: "Score"}
	fActive := &BoolFieldIndex{Field: "Active"}
	field = build(fID, fGroup, fTags, fAge, fScore, fActive,
		&CompoundIndex{Indexes: []Indexer{fGroup, fAge, fActive}},
		&CompoundMultiIndex{Indexes: []Indexer{fGroup, fTags}})
	return typed, field
}

func typedRows() []*typedRow {
	var rows []*typedRow
	for i := 0; i < 200; i++ {
		r := &typedRow{
			ID:     fmt.Sprintf("row-%03d", i),
			Group:  fmt.Sprintf("Group-%d", i%7),
			Age:    int32(i%50) - 10,
			Score:  uint16(i * 300),
			Active: i%3 == 0,
		}
		for j := 0; j < i%4; j++ {
			r.Tags = append(r.Tags, fmt.Sprintf("tag-%d", (i+j)%9))
		}
		rows = append(rows, r)
	}
	return rows
}

func drain(t *testing.T, it ResultIterator, err error) []string {
	t.Helper()
	if err != nil {
		return []string{"error: " + err.Error()}
	}
	var ids []string
	for obj := range All(it) {
		ids = append(ids, obj.(*typedRow).ID)
	}
	return ids
}

func TestTypedSchemaBehavesLikeFieldSchema(t *testing.T) {
	typedSchema, fieldSchema := typedSchemas()
	typedDB, err := NewMemDB(typedSchema)
	if err != nil {
		t.Fatal(err)
	}
	fieldDB, err := NewMemDB(fieldSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range []*MemDB{typedDB, fieldDB} {
		txn := db.Txn(true)
		for _, r := range typedRows() {
			if err := txn.Insert("rows", r); err != nil {
				t.Fatal(err)
			}
		}
		// Updates that move rows between index values, and deletes.
		for i, r := range typedRows() {
			switch {
			case i%10 == 0:
				moved := *r
				moved.Group, moved.Age, moved.Tags = "Moved", r.Age+100, []string{"moved"}
				if err := txn.Insert("rows", &moved); err != nil {
					t.Fatal(err)
				}
			case i%17 == 0:
				if err := txn.Delete("rows", r); err != nil {
					t.Fatal(err)
				}
			}
		}
		txn.Commit()
	}

	queries := []struct {
		index string
		args  []interface{}
	}{
		{"id", nil}, {"id", []interface{}{"row-042"}}, {"id", []interface{}{"nope"}}, {"id_prefix", []interface{}{"row-1"}},
		{"group", []interface{}{"group-3"}}, {"group", []interface{}{"GROUP-3"}}, {"group_prefix", []interface{}{"mo"}},
		{"tags", []interface{}{"tag-4"}}, {"tags_prefix", []interface{}{"tag"}}, {"tags", []interface{}{"moved"}},
		{"age", []interface{}{int32(5)}}, {"age", []interface{}{int32(-10)}}, {"age", []interface{}{int32(120)}},
		{"score", []interface{}{uint16(600)}}, {"active", []interface{}{true}}, {"active", []interface{}{false}},
		{"compound", []interface{}{"group-2", int32(7), true}}, {"compound_prefix", []interface{}{"group-2"}},
		{"multi", []interface{}{"group-1", "tag-3"}}, {"multi", []interface{}{"group-1"}},
		{"id", []interface{}{7}}, {"age", []interface{}{"x"}}, {"nope", nil},
	}
	typedTxn, fieldTxn := typedDB.Txn(false), fieldDB.Txn(false)
	for _, q := range queries {
		name := fmt.Sprintf("%s %v", q.index, q.args)
		tIt, tErr := typedTxn.Get("rows", q.index, q.args...)
		fIt, fErr := fieldTxn.Get("rows", q.index, q.args...)
		got, want := drain(t, tIt, tErr), drain(t, fIt, fErr)
		if tErr != nil && fErr != nil {
			continue // both refuse; the wording is each indexer's own
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Get %s:\n typed %v\n field %v", name, got, want)
		}
		tIt, tErr = typedTxn.GetReverse("rows", q.index, q.args...)
		fIt, fErr = fieldTxn.GetReverse("rows", q.index, q.args...)
		if got, want := drain(t, tIt, tErr), drain(t, fIt, fErr); !reflect.DeepEqual(got, want) {
			t.Errorf("GetReverse %s:\n typed %v\n field %v", name, got, want)
		}
	}
	for _, age := range []int32{-10, 0, 17, 39, 200} {
		tIt, tErr := typedTxn.LowerBound("rows", "age", age)
		fIt, fErr := fieldTxn.LowerBound("rows", "age", age)
		if got, want := drain(t, tIt, tErr), drain(t, fIt, fErr); !reflect.DeepEqual(got, want) {
			t.Errorf("LowerBound age %d:\n typed %v\n field %v", age, got, want)
		}
	}
}

func TestTableAndKeys(t *testing.T) {
	typedSchema, fieldSchema := typedSchemas()
	rows := NewTable[typedRow]("rows")
	byID := rows.StringKey("id")
	byGroup := rows.StringKey("group")
	byTag := rows.StringKey("tags")
	byAge := IntKeyOf[int32](rows, "age")
	byScore := UintKeyOf[uint16](rows, "score")

	// The same handles serve two databases with different schemas.
	for name, schema := range map[string]*DBSchema{"typed": typedSchema, "field": fieldSchema} {
		db, err := NewMemDB(schema)
		if err != nil {
			t.Fatal(err)
		}
		txn := db.Txn(true)
		for _, r := range typedRows() {
			if err := rows.Insert(txn, r); err != nil {
				t.Fatal(err)
			}
		}
		if err := rows.Delete(txn, &typedRow{ID: "row-199"}); err != nil {
			t.Fatal(err)
		}

		// Inside the write transaction and after commit.
		for pass := 0; pass < 2; pass++ {
			ids := func(r Rows[typedRow], err error) []string {
				t.Helper()
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				var out []string
				for row := range r.All() {
					out = append(out, row.ID)
				}
				return out
			}
			untyped := func(index string, args ...interface{}) []string {
				t.Helper()
				it, err := txn.Get("rows", index, args...)
				return drain(t, it, err)
			}

			if r, err := byID.First(txn, "row-042"); err != nil || r == nil || r.ID != "row-042" {
				t.Fatalf("%s: First = %v, %v", name, r, err)
			}
			if r, err := byID.First(txn, "row-199"); err != nil || r != nil {
				t.Fatalf("%s: First of a deleted row = %v, %v", name, r, err)
			}
			if r, err := byID.FirstPrefix(txn, "row-15"); err != nil || r == nil || r.ID != "row-150" {
				t.Fatalf("%s: FirstPrefix = %v, %v", name, r, err)
			}
			if r, err := rows.First(txn, "id", "row-007"); err != nil || r == nil || r.ID != "row-007" {
				t.Fatalf("%s: Table.First = %v, %v", name, r, err)
			}
			if r, err := rows.Last(txn, "id"); err != nil || r == nil || r.ID != "row-198" {
				t.Fatalf("%s: Table.Last = %v, %v", name, r, err)
			}
			if got, want := ids(byGroup.Get(txn, "GROUP-3")), untyped("group", "group-3"); !reflect.DeepEqual(got, want) || len(got) == 0 {
				t.Errorf("%s: group: %v, want %v", name, got, want)
			}
			if got, want := ids(byGroup.GetPrefix(txn, "group-")), untyped("group_prefix", "group-"); !reflect.DeepEqual(got, want) || len(got) == 0 {
				t.Errorf("%s: group prefix: %v, want %v", name, got, want)
			}
			if got, want := ids(byTag.Get(txn, "tag-4")), untyped("tags", "tag-4"); !reflect.DeepEqual(got, want) || len(got) == 0 {
				t.Errorf("%s: tags: %v, want %v", name, got, want)
			}
			if got, want := ids(byAge.Get(txn, 5)), untyped("age", int32(5)); !reflect.DeepEqual(got, want) || len(got) == 0 {
				t.Errorf("%s: age: %v, want %v", name, got, want)
			}
			if got, want := ids(byAge.LowerBound(txn, 38)), func() []string {
				it, err := txn.LowerBound("rows", "age", int32(38))
				return drain(t, it, err)
			}(); !reflect.DeepEqual(got, want) || len(got) == 0 {
				t.Errorf("%s: age lower bound: %v, want %v", name, got, want)
			}
			if r, err := byAge.First(txn, -10); err != nil || r == nil || r.Age != -10 {
				t.Errorf("%s: IntKey.First = %v, %v", name, r, err)
			}
			if got, want := ids(byScore.Get(txn, 600)), untyped("score", uint16(600)); !reflect.DeepEqual(got, want) || len(got) == 0 {
				t.Errorf("%s: score: %v, want %v", name, got, want)
			}
			if got, want := ids(byScore.LowerBound(txn, 59000)), func() []string {
				it, err := txn.LowerBound("rows", "score", uint16(59000))
				return drain(t, it, err)
			}(); !reflect.DeepEqual(got, want) || len(got) == 0 {
				t.Errorf("%s: score lower bound: %v, want %v", name, got, want)
			}
			if r, err := byScore.First(txn, 300); err != nil || r == nil || r.ID != "row-001" {
				t.Errorf("%s: UintKey.First = %v, %v", name, r, err)
			}

			if pass == 0 {
				txn.Commit()
				txn = db.Txn(false)
			}
		}

		// A watch obtained through a typed query fires like any other.
		r, err := byGroup.Get(txn, "group-3")
		if err != nil {
			t.Fatal(err)
		}
		w := db.Txn(true)
		if err := rows.Insert(w, &typedRow{ID: "new", Group: "group-3"}); err != nil {
			t.Fatal(err)
		}
		w.Commit()
		select {
		case <-r.WatchCh():
		default:
			t.Errorf("%s: watch channel of a typed query did not fire", name)
		}
	}
}

func TestKeyErrorsAndFallback(t *testing.T) {
	custom := &DBSchema{Tables: map[string]*TableSchema{"rows": {Name: "rows", Indexes: map[string]*IndexSchema{
		"id":   {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
		"uuid": {Name: "uuid", Indexer: &UUIDFieldIndex{Field: "Group"}},
		// An indexer the typed keys know nothing about: reversed strings.
		"rev": {Name: "rev", Indexer: reversedIndex{}},
	}}}}
	db, err := NewMemDB(custom)
	if err != nil {
		t.Fatal(err)
	}
	rows := NewTable[typedRow]("rows")
	txn := db.Txn(true)
	for _, r := range []*typedRow{
		{ID: "abc", Group: "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9"},
		{ID: "xyz", Group: "ffffffff-4e5f-6071-8293-a4b5c6d7e8f9"},
	} {
		if err := rows.Insert(txn, r); err != nil {
			t.Fatal(err)
		}
	}
	txn.Commit()
	txn = db.Txn(false)

	if r, err := rows.StringKey("rev").First(txn, "cba"); err != nil || r == nil || r.ID != "abc" {
		t.Errorf("custom indexer through a typed key: %v, %v", r, err)
	}
	if r, err := rows.StringKey("uuid").First(txn, "ffffffff-4e5f-6071-8293-a4b5c6d7e8f9"); err != nil || r == nil || r.ID != "xyz" {
		t.Errorf("uuid through a typed key: %v, %v", r, err)
	}
	if r, err := rows.StringKey("uuid").FirstPrefix(txn, "0a1b"); err != nil || r == nil || r.ID != "abc" {
		t.Errorf("uuid prefix through a typed key: %v, %v", r, err)
	}
	if _, err := rows.StringKey("uuid").First(txn, "not-a-uuid"); err == nil || !strings.Contains(err.Error(), "index error") {
		t.Errorf("malformed uuid: %v", err)
	}
	if _, err := rows.StringKey("nope").First(txn, "x"); err == nil || err.Error() != "invalid index 'nope'" {
		t.Errorf("unknown index: %v", err)
	}
	if _, err := rows.StringKey("id_prefix").First(txn, "x"); err == nil {
		t.Error("a _prefix name was accepted as a key")
	}
	if _, err := NewTable[typedRow]("nope").StringKey("id").Get(txn, "x"); err == nil || err.Error() != "invalid table 'nope'" {
		t.Errorf("unknown table: %v", err)
	}
	if _, err := IntKeyOf[int](rows, "id").First(txn, 3); err == nil {
		t.Error("an integer key on a string index did not fail")
	}
}

// reversedIndex indexes the reversed ID. It exists to be unknown to the fast
// paths.
type reversedIndex struct{}

func reverse(s string) []byte {
	out := make([]byte, 0, len(s)+1)
	for i := len(s) - 1; i >= 0; i-- {
		out = append(out, s[i])
	}
	return append(out, 0)
}

func (reversedIndex) FromObject(obj interface{}) (bool, []byte, error) {
	return true, reverse(obj.(*typedRow).ID), nil
}

func (reversedIndex) FromArgs(args ...interface{}) ([]byte, error) {
	s, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("want a string")
	}
	// The argument is the reversed ID itself.
	return append([]byte(s), 0), nil
}

func TestTypedKeysDoNotAllocate(t *testing.T) {
	typedSchema, _ := typedSchemas()
	db, err := NewMemDB(typedSchema)
	if err != nil {
		t.Fatal(err)
	}
	rows := NewTable[typedRow]("rows")
	txn := db.Txn(true)
	for _, r := range typedRows() {
		if err := rows.Insert(txn, r); err != nil {
			t.Fatal(err)
		}
	}
	txn.Commit()
	txn = db.Txn(false)

	byID, byGroup, byAge := rows.StringKey("id"), rows.StringKey("group"), IntKeyOf[int32](rows, "age")
	keys := []string{"row-001", "row-150", "missing"}
	var sink *typedRow
	if n := testing.AllocsPerRun(200, func() {
		for _, k := range keys {
			sink, _ = byID.First(txn, k)
		}
		sink, _ = byGroup.First(txn, "group-3")
		sink, _ = byAge.First(txn, 1000)
	}); n != 0 {
		t.Errorf("typed lookups allocate %v times per run", n)
	}
	_ = sink
}
