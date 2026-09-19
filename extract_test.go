// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// The extractors in extract.go are a second implementation of the built-in
// indexers. These tests pin the contract between the two: whenever an
// extractor claims an input ("handled"), it must produce exactly what the
// exported method produces -- and the everyday inputs must actually be
// claimed, or the fast path silently stops existing.

type exName string

type exInner struct {
	Deep  string
	DeepN int16
}

type exPtrInner struct {
	ViaPtr string
}

type exObj struct {
	exInner
	*exPtrInner

	ID      string
	Named   exName
	Empty   string
	Ptr     *string
	NilPtr  *string
	Tags    []string
	NoTags  []string
	NamedTs []exName
	Meta    map[string]string
	NoMeta  map[string]string
	I       int
	I8      int8
	I16     int16
	I32     int32
	I64     int64
	U       uint
	U8      uint8
	U16     uint16
	U32     uint32
	U64     uint64
	B       bool
	UUID    string
	BadUUID string
	Bytes   []byte
	Iface   interface{}
	Float   float64
	hidden  string
}

func exampleObj() *exObj {
	p := "Pointed"
	return &exObj{
		exInner: exInner{Deep: "deep", DeepN: -7},
		ID:      "Obj-1",
		Named:   "NamedValue",
		Ptr:     &p,
		Tags:    []string{"One", "", "two", "One"},
		NamedTs: []exName{"x", "Y"},
		Meta:    map[string]string{"K": "V", "": "skipped", "k2": ""},
		I:       -42, I8: -128, I16: 300, I32: -70000, I64: 1 << 40,
		U: 42, U8: 255, U16: 65535, U32: 1 << 31, U64: 1<<64 - 1,
		B:       true,
		UUID:    "0a1B2c3D-4e5F-6071-8293-a4b5c6d7e8f9",
		BadUUID: "not-a-uuid",
		Bytes:   []byte("0123456789abcdef"),
		hidden:  "secret",
	}
}

// fromObject normalises both indexer kinds to (ok, sorted keys, error text).
func fromObject(ix Indexer, obj interface{}) (ok bool, keys []string, errText string, panicked interface{}) {
	defer func() { panicked = recover() }()
	var err error
	switch t := ix.(type) {
	case SingleIndexer:
		var val []byte
		ok, val, err = t.FromObject(obj)
		if ok && err == nil {
			keys = []string{string(val)}
		}
	case MultiIndexer:
		var vals [][]byte
		ok, vals, err = t.FromObject(obj)
		for _, v := range vals {
			keys = append(keys, string(v))
		}
	}
	if err != nil {
		errText = err.Error()
		ok, keys = false, nil
	}
	sort.Strings(keys)
	return ok, keys, errText, nil
}

func extractKeys(e *extractor, obj interface{}, suffix []byte) (ok, handled bool, keys []string, errText string) {
	var kl keyList
	kl.buf = append(kl.buf, "garbage-before"...) // must be preserved
	var tmp keyList
	ok, handled, err := e.appendKeys(&kl, &tmp, obj, suffix)
	if !bytes.HasPrefix(kl.buf, []byte("garbage-before")) {
		panic("extractor clobbered the buffer")
	}
	if err != nil {
		return false, handled, nil, err.Error()
	}
	if !handled || !ok {
		if len(kl.buf) != len("garbage-before") || kl.len() != 0 {
			panic("extractor left partial output behind")
		}
		return ok, handled, nil, ""
	}
	for i := 0; i < kl.len(); i++ {
		keys = append(keys, string(kl.key(i)))
	}
	// key(0) starts at 0, so strip the sentinel from the first key.
	if len(keys) > 0 {
		keys[0] = keys[0][len("garbage-before"):]
	}
	sort.Strings(keys)
	return ok, handled, keys, ""
}

func TestExtractorsMatchIndexers(t *testing.T) {
	obj := exampleObj()
	var typedNil *exObj
	objects := map[string]interface{}{
		"pointer":   obj,
		"by-value":  *obj,
		"typed-nil": typedNil,
		"nil":       nil,
		"string":    "just a string",
		"other":     &struct{ ID int }{7},
	}

	fields := []string{
		"ID", "Named", "Empty", "Ptr", "NilPtr", "Tags", "NoTags", "NamedTs", "Meta", "NoMeta",
		"I", "I8", "I16", "I32", "I64", "U", "U8", "U16", "U32", "U64", "B", "UUID", "BadUUID",
		"Bytes", "Iface", "Float", "hidden", "Deep", "DeepN", "ViaPtr", "Missing",
	}

	var indexers []Indexer
	for _, f := range fields {
		indexers = append(indexers,
			&StringFieldIndex{Field: f}, &StringFieldIndex{Field: f, Lowercase: true},
			&StringSliceFieldIndex{Field: f}, &StringSliceFieldIndex{Field: f, Lowercase: true},
			&StringMapFieldIndex{Field: f}, &StringMapFieldIndex{Field: f, Lowercase: true},
			&IntFieldIndex{Field: f}, &UintFieldIndex{Field: f}, &BoolFieldIndex{Field: f},
			&UUIDFieldIndex{Field: f}, &FieldSetIndex{Field: f},
		)
	}
	boom := errors.New("boom")
	indexers = append(indexers,
		&ConditionalIndex{Conditional: func(o interface{}) (bool, error) { return true, nil }},
		&ConditionalIndex{Conditional: func(o interface{}) (bool, error) { return false, nil }},
		&ConditionalIndex{Conditional: func(o interface{}) (bool, error) { return false, boom }},
		&CompoundIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &IntFieldIndex{Field: "I16"}, &BoolFieldIndex{Field: "B"}}},
		&CompoundIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &StringFieldIndex{Field: "Empty"}, &UintFieldIndex{Field: "U8"}}},
		&CompoundIndex{AllowMissing: true, Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &StringFieldIndex{Field: "Empty"}, &UintFieldIndex{Field: "U8"}}},
		&CompoundIndex{AllowMissing: true, Indexes: []Indexer{&StringFieldIndex{Field: "Empty"}, &StringFieldIndex{Field: "ID"}}},
		&CompoundIndex{Indexes: []Indexer{&UUIDFieldIndex{Field: "UUID"}, &StringFieldIndex{Field: "Ptr", Lowercase: true}}},
		&CompoundIndex{Indexes: []Indexer{&UUIDFieldIndex{Field: "BadUUID"}}},
		&CompoundIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &StringSliceFieldIndex{Field: "Tags"}}},
		&CompoundIndex{Indexes: []Indexer{&StringFieldIndex{Field: "Missing"}}},
		&CompoundIndex{Indexes: []Indexer{&CompoundIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}}}, &IntFieldIndex{Field: "I"}}},
		&CompoundIndex{},
		&CompoundMultiIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &StringSliceFieldIndex{Field: "Tags"}}},
		&CompoundMultiIndex{Indexes: []Indexer{&StringSliceFieldIndex{Field: "Tags"}, &StringMapFieldIndex{Field: "Meta"}, &IntFieldIndex{Field: "I8"}}},
		&CompoundMultiIndex{Indexes: []Indexer{&StringSliceFieldIndex{Field: "Tags", Lowercase: true}, &StringSliceFieldIndex{Field: "NamedTs"}}},
		&CompoundMultiIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &StringSliceFieldIndex{Field: "NoTags"}}},
		&CompoundMultiIndex{Indexes: []Indexer{&StringFieldIndex{Field: "Empty"}, &StringSliceFieldIndex{Field: "Tags"}}},
		&CompoundMultiIndex{Indexes: []Indexer{&StringFieldIndex{Field: "Missing"}, &StringSliceFieldIndex{Field: "Tags"}}},
		// AllowMissing with two levels: upstream's prefix handling is sound.
		&CompoundMultiIndex{AllowMissing: true, Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &StringSliceFieldIndex{Field: "Tags"}}},
		&CompoundMultiIndex{AllowMissing: true, Indexes: []Indexer{&StringSliceFieldIndex{Field: "Tags"}, &StringFieldIndex{Field: "Empty"}}},
		&CompoundMultiIndex{AllowMissing: true, Indexes: []Indexer{&StringFieldIndex{Field: "Empty"}, &StringSliceFieldIndex{Field: "Tags"}}},
		&CompoundMultiIndex{AllowMissing: true, Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &StringFieldIndex{Field: "Named"}, &StringFieldIndex{Field: "Ptr"}}},
		&CompoundMultiIndex{},
	)

	handledCount := 0
	for _, ix := range indexers {
		e := compileExtractor(ix)
		for oname, o := range objects {
			for _, suffix := range [][]byte{nil, []byte("|pk\x00")} {
				name := fmt.Sprintf("%T%+v/%s", ix, reflect.ValueOf(ix).Elem().Interface(), oname)
				if _, isCond := ix.(*ConditionalIndex); isCond {
					name = fmt.Sprintf("%T/%s", ix, oname)
				}

				wantOK, wantKeys, wantErr, panicked := fromObject(ix, o)
				ok, handled, keys, errText := extractKeys(e, o, suffix)
				if !handled {
					continue
				}
				handledCount++
				if panicked != nil {
					t.Errorf("%s: fast path handled an input on which the indexer panics (%v)", name, panicked)
					continue
				}
				for i := range wantKeys {
					wantKeys[i] += string(suffix)
				}
				sort.Strings(wantKeys)
				if ok != wantOK || errText != wantErr || !reflect.DeepEqual(keys, wantKeys) {
					t.Errorf("%s:\n  indexer   ok=%v err=%q keys=%q\n  extractor ok=%v err=%q keys=%q",
						name, wantOK, wantErr, wantKeys, ok, errText, keys)
				}
			}
		}
	}
	if handledCount == 0 {
		t.Fatal("no input took the fast path")
	}

	// The everyday cases must take the fast path, otherwise it has silently
	// stopped existing.
	mustHandle := []Indexer{
		&StringFieldIndex{Field: "ID"}, &StringFieldIndex{Field: "Named", Lowercase: true},
		&StringFieldIndex{Field: "Ptr"}, &StringFieldIndex{Field: "NilPtr"}, &StringFieldIndex{Field: "Deep"},
		&StringFieldIndex{Field: "hidden"},
		&IntFieldIndex{Field: "I8"}, &IntFieldIndex{Field: "DeepN"}, &UintFieldIndex{Field: "U64"},
		&BoolFieldIndex{Field: "B"}, &UUIDFieldIndex{Field: "UUID"}, &FieldSetIndex{Field: "NilPtr"},
		&ConditionalIndex{Conditional: func(o interface{}) (bool, error) { return true, nil }},
		&CompoundIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &IntFieldIndex{Field: "I16"}}},
	}
	if unsafeFields {
		mustHandle = append(mustHandle,
			&StringSliceFieldIndex{Field: "Tags"}, &StringSliceFieldIndex{Field: "NamedTs"},
			&StringMapFieldIndex{Field: "Meta"})
	}
	for _, ix := range mustHandle {
		if _, handled, _, _ := extractKeys(compileExtractor(ix), obj, nil); !handled {
			t.Errorf("%T%+v: a plain pointer-to-struct object did not take the fast path", ix, ix)
		}
	}
}

func TestExtractorArgsMatchIndexers(t *testing.T) {
	type named int
	argSets := [][]interface{}{
		{}, {nil}, {"abc"}, {"ABC"}, {""}, {"ÅÄÖ-ǅ"}, {"a", "B"}, {"a", "b", "c"}, {"a", nil},
		{1}, {int8(-3)}, {int16(9)}, {int32(-5)}, {int64(1 << 40)}, {named(4)},
		{uint(1)}, {uint8(200)}, {uint16(9)}, {uint32(7)}, {uint64(1 << 63)},
		{true}, {false}, {1.5}, {[]byte("0123456789abcdef")}, {[]byte("short")},
		{"0a1B2c3D-4e5F-6071-8293-a4b5c6d7e8f9"}, {"0a1b2c3d4e5f60718293a4b5c6d7e8f9----"},
		{"0a1B2c3D"}, {"0a1B2c3D-4e5F"}, {"0a1"}, {"zz"}, {"-----"}, {"0a1B2c3D-4e5F-6071-8293-a4b5c6d7e8f9-extra"},
		{"abc", 7}, {"abc", "x", true}, {"abc", 7, false}, {"id", int16(3), true}, {"id", int16(3)}, {"id"},
	}
	indexers := []Indexer{
		&StringFieldIndex{Field: "ID"}, &StringFieldIndex{Field: "ID", Lowercase: true},
		&StringSliceFieldIndex{Field: "Tags"}, &StringSliceFieldIndex{Field: "Tags", Lowercase: true},
		&StringMapFieldIndex{Field: "Meta"}, &StringMapFieldIndex{Field: "Meta", Lowercase: true},
		&IntFieldIndex{Field: "I"}, &UintFieldIndex{Field: "U"}, &BoolFieldIndex{Field: "B"},
		&UUIDFieldIndex{Field: "UUID"}, &FieldSetIndex{Field: "Ptr"},
		&ConditionalIndex{Conditional: func(o interface{}) (bool, error) { return true, nil }},
		&CompoundIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &IntFieldIndex{Field: "I16"}, &BoolFieldIndex{Field: "B"}}},
		&CompoundIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID", Lowercase: true}, &UUIDFieldIndex{Field: "UUID"}}},
		&CompoundIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &StringMapFieldIndex{Field: "Meta"}}},
		&CompoundIndex{},
		&CompoundMultiIndex{Indexes: []Indexer{&StringFieldIndex{Field: "ID"}, &StringSliceFieldIndex{Field: "Tags"}}},
	}

	handledCount := 0
	for _, ix := range indexers {
		e := compileExtractor(ix)
		for _, args := range argSets {
			for _, prefix := range []bool{false, true} {
				name := fmt.Sprintf("%T%+v args=%#v prefix=%v", ix, ix, args, prefix)
				got, handled := e.appendArgs([]byte("pre"), args, prefix)
				if !handled {
					if string(got) != "pre" {
						t.Errorf("%s: unhandled call modified the buffer: %q", name, got)
					}
					continue
				}
				handledCount++

				var want []byte
				var err error
				if prefix {
					pi, ok := ix.(PrefixIndexer)
					if !ok {
						t.Errorf("%s: fast path handled a prefix query the indexer does not support", name)
						continue
					}
					want, err = pi.PrefixFromArgs(args...)
				} else {
					want, err = ix.FromArgs(args...)
				}
				if err != nil {
					t.Errorf("%s: fast path handled an input the indexer rejects: %v", name, err)
					continue
				}
				if string(got) != "pre"+string(want) {
					t.Errorf("%s: indexer %q, extractor %q", name, want, got[3:])
				}
			}
		}
	}
	if handledCount < 60 {
		t.Fatalf("only %d argument sets took the fast path", handledCount)
	}
}

// TestAppendUUIDMatchesParseString checks the hex fast path against the
// original parser on every shape of input, valid or not.
func TestAppendUUIDMatchesParseString(t *testing.T) {
	u := &UUIDFieldIndex{}
	inputs := []string{
		"", "0", "00", "0-0", "0a1B2c3D-4e5F-6071-8293-a4b5c6d7e8f9", "0a1b2c3d4e5f60718293a4b5c6d7e8f9",
		"0a1b2c3d4e5f60718293a4b5c6d7e8f9----", "-0a1b2c3d4e5f60718293a4b5c6d7e8f9---", "0a1B2c3D-4e5F-6071-8293-a4b5c6d7e8f",
		"0a1B2c3D-4e5F-6071-8293-a4b5c6d7e8fg", "------", "-----", "----", "0a1B2c3D-4e5F-6071-8293-a4b5c6d7e8f9a",
		"0a1B2c3D-4e5F", "0a1B2c3D-4e5", "gg", "0A", "Ff-fF",
	}
	for _, in := range inputs {
		for _, enforce := range []bool{true, false} {
			want, err := u.parseString(in, enforce)
			got, ok := appendUUID([]byte("pre"), in, enforce)
			if ok != (err == nil) {
				t.Errorf("parseString(%q,%v) err=%v but fast path ok=%v", in, enforce, err, ok)
				continue
			}
			if ok && string(got) != "pre"+string(want) {
				t.Errorf("parseString(%q,%v) = %x, fast path %x", in, enforce, want, got[3:])
			}
			if !ok && string(got) != "pre" {
				t.Errorf("parseString(%q,%v): rejected input modified the buffer", in, enforce)
			}
		}
	}
}
