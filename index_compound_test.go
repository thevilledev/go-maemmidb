// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"reflect"
	"testing"
)

// TestCompoundMultiIndexPrefixesAreIntact covers the one shape on which this
// package deliberately differs from upstream: AllowMissing with three or more
// levels and a multi-valued level in the middle. Upstream builds the prefix
// keys with append on a shared slice, so a sibling can overwrite a prefix that
// was already emitted (which bytes get clobbered depends on allocator size
// classes). Every key must come out intact, in depth-first order.
func TestCompoundMultiIndexPrefixesAreIntact(t *testing.T) {
	type row struct {
		A string
		B []string
		C []string
	}
	obj := &row{A: "a", B: []string{"b1", "b2", "b3"}, C: []string{"c1", "c2"}}
	ix := &CompoundMultiIndex{AllowMissing: true, Indexes: []Indexer{
		&StringFieldIndex{Field: "A"}, &StringSliceFieldIndex{Field: "B"}, &StringSliceFieldIndex{Field: "C"},
	}}
	want := []string{
		"a\x00",
		"a\x00b1\x00", "a\x00b1\x00c1\x00", "a\x00b1\x00c2\x00",
		"a\x00b2\x00", "a\x00b2\x00c1\x00", "a\x00b2\x00c2\x00",
		"a\x00b3\x00", "a\x00b3\x00c1\x00", "a\x00b3\x00c2\x00",
	}
	ok, vals, err := ix.FromObject(obj)
	if !ok || err != nil {
		t.Fatalf("FromObject: ok=%v err=%v", ok, err)
	}
	var got []string
	for _, v := range vals {
		got = append(got, string(v))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	// Keys must not share growable memory with each other.
	for i := range vals {
		vals[i] = append(vals[i], "|pk"...)
	}
	for i, v := range vals {
		if string(v) != want[i]+"|pk" {
			t.Fatalf("key %d was clobbered by an append to a sibling: %q", i, v)
		}
	}
}
