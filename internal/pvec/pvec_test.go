// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package pvec

import (
	"math/rand"
	"sort"
	"testing"
)

type model map[uint32]interface{}

func (m model) clone() model {
	c := make(model, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// check compares a vector with its model: every set index, a sample of unset
// ones, the count, and the bookkeeping of every node.
func check(t testing.TB, what string, v Vector, m model, rng *rand.Rand) {
	t.Helper()
	if v.Len() != len(m) {
		t.Fatalf("%s: Len %d, want %d", what, v.Len(), len(m))
	}
	keys := make([]uint32, 0, len(m))
	for i := range m {
		keys = append(keys, i)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	cur := v.Cursor()
	for _, i := range keys {
		if got := v.Get(i); got != m[i] {
			t.Fatalf("%s: Get(%d) = %v, want %v", what, i, got, m[i])
		}
		if got := cur.Get(i); got != m[i] {
			t.Fatalf("%s: Cursor.Get(%d) = %v, want %v", what, i, got, m[i])
		}
	}
	for n := 0; n < 200; n++ {
		i := randomIndex(rng)
		if _, set := m[i]; !set && (v.Get(i) != nil || cur.Get(i) != nil) {
			t.Fatalf("%s: index %d is set", what, i)
		}
	}

	var walk func(n *node, height uint8) uint32
	walk = func(n *node, height uint8) uint32 {
		var used uint32
		if height == 0 {
			for _, val := range n.vals {
				if val != nil {
					used++
				}
			}
		} else {
			for _, k := range n.kids {
				if k != nil {
					used += walk(k, height-1)
				}
			}
		}
		if used == 0 || used != n.used {
			t.Fatalf("%s: node at height %d counts %d values, holds %d", what, height, n.used, used)
		}
		return used
	}
	if v.root != nil {
		walk(v.root, v.height)
	}
}

func randomIndex(rng *rand.Rand) uint32 {
	switch rng.Intn(8) {
	case 0:
		return rng.Uint32()
	case 1:
		return ^uint32(0) - uint32(rng.Intn(40))
	case 2:
		return 1<<19 + uint32(rng.Intn(100))
	default:
		return uint32(rng.Intn(3000))
	}
}

func TestAgainstModel(t *testing.T) {
	for seed := int64(1); seed <= 10; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var w Writer
		var v Vector
		m := model{}
		type version struct {
			v Vector
			m model
		}
		var versions []version
		for op := 0; op < 6000; op++ {
			i := randomIndex(rng)
			if rng.Intn(3) == 0 {
				v = w.Set(v, i, nil)
				delete(m, i)
			} else {
				val := op
				v = w.Set(v, i, val)
				m[i] = val
			}
			if op%500 == 499 {
				// A version that is kept must be frozen first.
				w.Freeze()
				versions = append(versions, version{v, m.clone()})
			}
		}
		check(t, "final", v, m, rng)
		for _, old := range versions {
			check(t, "old version", old.v, old.m, rng)
		}

		// Unsetting everything leaves the zero Vector.
		for i := range m {
			v = w.Set(v, i, nil)
		}
		if v.root != nil || v.Len() != 0 {
			t.Fatalf("seed %d: %d values left", seed, v.Len())
		}
	}
}

func TestOwnership(t *testing.T) {
	var w Writer
	a := w.Set(Vector{}, 3, "a")
	b := w.Set(a, 4, "b")
	if !a.Same(b) {
		t.Error("an owned leaf was copied")
	}
	w.Freeze()
	c := w.Set(b, 5, "c")
	if c.Same(b) || b.Get(5) != nil || c.Get(5) != "c" {
		t.Error("a frozen leaf was mutated")
	}
	if d := w.Set(c, 900, nil); !d.Same(c) {
		t.Error("unsetting an unset index changed the vector")
	}
	var empty Vector
	if got := w.Set(empty, 1<<30, nil); got.root != nil {
		t.Error("unsetting in an empty vector created nodes")
	}
}
