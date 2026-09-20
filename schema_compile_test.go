// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"fmt"
	"testing"
)

// TestNameIndex checks name resolution against a map, for schemas whose names
// all differ in length, all share one (the bisected case), and everything in
// between -- including the names that must NOT resolve.
func TestNameIndex(t *testing.T) {
	sets := map[string][]string{
		"none":     nil,
		"one":      {"id"},
		"typical":  {"id", "id_prefix", "name", "name_prefix", "age", "age_prefix"},
		"crowded":  {},
		"mixed":    {"a", "b", "c", "d", "e", "f", "ab", "abc", "", "a-much-longer-name-than-the-others"},
		"boundary": {"aaaa", "aaab", "aaac", "aaad", "aaae"},
	}
	for i := 0; i < 60; i++ {
		sets["crowded"] = append(sets["crowded"], fmt.Sprintf("table-%02d", i))
	}
	for label, names := range sets {
		want := map[string]int{}
		var entries []nameEntry[int]
		for i, name := range names {
			want[name] = i
			entries = append(entries, nameEntry[int]{name, i})
		}
		x := newNameIndex(entries)
		for name, val := range want {
			// A fresh copy of the name, so that the lookup cannot succeed by
			// address alone.
			if got, ok := x.get(string([]byte(name))); !ok || got != val {
				t.Errorf("%s: get(%q) = %d, %v; want %d", label, name, got, ok, val)
			}
		}
		for _, miss := range []string{"", "x", "id_", "table-60", "table-6", "table-000", "aaa", "aaaf", "aaa0", "zzzz", "a-much-longer-name-than-the-otherz", "a-much-longer-name-than-the-others-and-more"} {
			if _, present := want[miss]; present {
				continue
			}
			if got, ok := x.get(miss); ok {
				t.Errorf("%s: get(%q) found %d", label, miss, got)
			}
		}
	}
}
