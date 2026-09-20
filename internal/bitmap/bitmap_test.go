// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package bitmap

import (
	"math/bits"
	"math/rand"
	"sort"
	"testing"
)

// model is the oracle: a plain Go set.
type model map[uint32]struct{}

func (m model) sorted() []uint32 {
	out := make([]uint32, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (m model) clone() model {
	c := make(model, len(m))
	for id := range m {
		c[id] = struct{}{}
	}
	return c
}

func ids(b Bitmap) []uint32 {
	out := make([]uint32, 0, b.Len())
	it := b.Iterate()
	for id, ok := it.Next(); ok; id, ok = it.Next() {
		out = append(out, id)
	}
	return out
}

// checkShape verifies the structural invariants of a set: counts add up,
// nothing is empty, child slices match the presence bits, and the tree is as
// low as its ids allow.
func checkShape(t testing.TB, b Bitmap) {
	t.Helper()
	if b.root == nil {
		return
	}
	if b.height() > maxHeight {
		t.Fatalf("height %d", b.height())
	}
	if b.height() > 0 && b.root.present == 1 {
		t.Fatalf("root of height %d only has child 0: not canonical", b.height())
	}
	var walk func(n *node, height uint8) uint32
	walk = func(n *node, height uint8) uint32 {
		if n.height != height {
			t.Fatalf("node of height %d at level %d", n.height, height)
		}
		if height == 0 {
			if n.kids != nil || n.present != 0 {
				t.Fatalf("chunk with directory fields")
			}
			c := popcount(n.words)
			if c == 0 || c != n.count {
				t.Fatalf("chunk count %d, bits %d", n.count, c)
			}
			return c
		}
		if n.words != [4]uint64{} {
			t.Fatalf("directory node with chunk bits")
		}
		if len(n.kids) == 0 || len(n.kids) != bits.OnesCount64(n.present) {
			t.Fatalf("%d children for presence %b", len(n.kids), n.present)
		}
		var sum uint32
		for _, k := range n.kids {
			sum += walk(k, height-1)
		}
		if sum != n.count {
			t.Fatalf("directory count %d, children hold %d", n.count, sum)
		}
		return sum
	}
	walk(b.root, b.height())
}

func checkEqual(t testing.TB, what string, b Bitmap, m model) {
	t.Helper()
	checkShape(t, b)
	want := m.sorted()
	got := ids(b)
	if len(got) != len(want) || b.Len() != len(want) || b.Empty() != (len(want) == 0) {
		t.Fatalf("%s: %d ids (Len %d), want %d", what, len(got), b.Len(), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: id %d is %d, want %d", what, i, got[i], want[i])
		}
	}
	if min, ok := b.Min(); ok != (len(want) > 0) || (ok && min != want[0]) {
		t.Fatalf("%s: Min = %d, %v", what, min, ok)
	}
}

// randomID draws from a mix of distributions: dense small ids (what row ids
// look like), clusters, and the whole range including both ends.
func randomID(rng *rand.Rand) uint32 {
	switch rng.Intn(10) {
	case 0:
		return rng.Uint32()
	case 1:
		return ^uint32(0) - uint32(rng.Intn(300))
	case 2:
		return uint32(rng.Intn(3)) << (8 + 6*uint(rng.Intn(4)))
	case 3, 4:
		return 1<<20 + uint32(rng.Intn(2000))
	default:
		return uint32(rng.Intn(5000))
	}
}

func TestSetClearAgainstModel(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var w Writer
		var b Bitmap
		m := model{}
		for op := 0; op < 4000; op++ {
			id := randomID(rng)
			if rng.Intn(3) == 0 {
				var was bool
				b, was = w.Clear(b, id)
				if _, in := m[id]; in != was {
					t.Fatalf("seed %d: Clear(%d) reported %v", seed, id, was)
				}
				delete(m, id)
			} else {
				var added bool
				b, added = w.Set(b, id)
				if _, in := m[id]; in == added {
					t.Fatalf("seed %d: Set(%d) reported %v", seed, id, added)
				}
				m[id] = struct{}{}
			}
			if op%97 == 0 {
				checkEqual(t, "during", b, m)
				for i := 0; i < 50; i++ {
					probe := randomID(rng)
					if _, in := m[probe]; b.Has(probe) != in {
						t.Fatalf("seed %d: Has(%d) = %v", seed, probe, !in)
					}
				}
			}
		}
		checkEqual(t, "final", b, m)

		// Emptying the set, in random order, must leave the zero Bitmap.
		rest := m.sorted()
		rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
		for _, id := range rest {
			b, _ = w.Clear(b, id)
		}
		if !b.Empty() || b.root != nil {
			t.Fatalf("seed %d: %d ids left after clearing everything", seed, b.Len())
		}
	}
}

// TestPersistence holds on to old versions and checks that later writes never
// change them -- with a Freeze between versions, as the contract demands --
// and that writes between two freezes do mutate in place (no copy per write).
func TestPersistence(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var w Writer
	var b Bitmap
	m := model{}
	type version struct {
		b Bitmap
		m model
	}
	var versions []version
	for round := 0; round < 60; round++ {
		for op := 0; op < 50; op++ {
			id := randomID(rng)
			if rng.Intn(4) == 0 {
				b, _ = w.Clear(b, id)
				delete(m, id)
			} else {
				b, _ = w.Set(b, id)
				m[id] = struct{}{}
			}
		}
		w.Freeze()
		versions = append(versions, version{b, m.clone()})
		for _, v := range versions {
			checkEqual(t, "old version", v.b, v.m)
		}
	}

	// In place: a second write into the same chunk keeps the root.
	var w2 Writer
	x, _ := w2.Set(Bitmap{}, 5)
	y, _ := w2.Set(x, 6)
	if x.root != y.root {
		t.Error("an owned chunk was copied")
	}
	w2.Freeze()
	z, _ := w2.Set(y, 7)
	if z.root == y.root || y.Has(7) || !z.Has(7) {
		t.Error("a frozen chunk was mutated")
	}
	// A no-op never copies and never draws an epoch.
	var w3 Writer
	if same, added := w3.Set(z, 7); added || !same.Same(z) {
		t.Error("setting a present id changed the set")
	}
	if same, was := w3.Clear(z, 8); was || !same.Same(z) || w3.epoch != 0 {
		t.Error("clearing an absent id changed the set")
	}
}

func build(rng *rand.Rand, n int) (Bitmap, model) {
	var w Writer
	var b Bitmap
	m := model{}
	for i := 0; i < n; i++ {
		id := randomID(rng)
		b, _ = w.Set(b, id)
		m[id] = struct{}{}
	}
	return b, m
}

func TestAlgebraAgainstModel(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		// Sizes from empty to a few thousand, so that heights differ.
		a, am := build(rng, rng.Intn(4)*rng.Intn(1500))
		b, bm := build(rng, rng.Intn(4)*rng.Intn(1500))
		if seed%5 == 0 {
			// Overlap heavily: b is a plus and minus a few ids.
			var w Writer
			b, bm = a, am.clone()
			for i := 0; i < 20; i++ {
				id := randomID(rng)
				if i%2 == 0 {
					b, _ = w.Set(b, id)
					bm[id] = struct{}{}
				} else {
					b, _ = w.Clear(b, id)
					delete(bm, id)
				}
			}
		}

		and, or, andNot, notAnd := model{}, model{}, model{}, model{}
		for id := range am {
			or[id] = struct{}{}
			if _, in := bm[id]; in {
				and[id] = struct{}{}
			} else {
				andNot[id] = struct{}{}
			}
		}
		for id := range bm {
			or[id] = struct{}{}
			if _, in := am[id]; !in {
				notAnd[id] = struct{}{}
			}
		}
		checkEqual(t, "And", And(a, b), and)
		checkEqual(t, "And swapped", And(b, a), and)
		checkEqual(t, "Or", Or(a, b), or)
		checkEqual(t, "Or swapped", Or(b, a), or)
		checkEqual(t, "AndNot", AndNot(a, b), andNot)
		checkEqual(t, "AndNot swapped", AndNot(b, a), notAnd)
		// The operands are untouched.
		checkEqual(t, "a", a, am)
		checkEqual(t, "b", b, bm)
	}
}

// TestAlgebraShares pins the structural sharing the operations promise.
func TestAlgebraShares(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	a, _ := build(rng, 3000)
	var w Writer
	sub := a
	for _, id := range ids(a)[:500] {
		sub, _ = w.Clear(sub, id)
	}
	var empty Bitmap
	for name, c := range map[string]struct{ got, want Bitmap }{
		"a&a":       {And(a, a), a},
		"a|a":       {Or(a, a), a},
		"a&^a":      {AndNot(a, a), empty},
		"a|0":       {Or(a, empty), a},
		"0|a":       {Or(empty, a), a},
		"a&0":       {And(a, empty), empty},
		"a&^0":      {AndNot(a, empty), a},
		"0&^a":      {AndNot(empty, a), empty},
		"sub&a":     {And(sub, a), sub},
		"a&sub":     {And(a, sub), sub},
		"sub|a":     {Or(sub, a), a},
		"a|sub":     {Or(a, sub), a},
		"sub&^a":    {AndNot(sub, a), empty},
		"a&^(a&^a)": {AndNot(a, AndNot(a, a)), a},
	} {
		if !c.got.Same(c.want) {
			t.Errorf("%s is not the operand itself", name)
		}
	}

	// Heights that differ: a set of small ids against one of huge ids.
	var small, huge Bitmap
	small, _ = w.Set(small, 3)
	huge, _ = w.Set(huge, 1<<31)
	huge, _ = w.Set(huge, 3)
	if got := And(small, huge); !got.Same(small) {
		t.Error("small&huge is not small")
	}
	// (Not necessarily huge itself: the chunk both hold may be taken from
	// either operand.)
	if got := Or(small, huge); got.Len() != 2 || !got.Has(3) || !got.Has(1<<31) {
		t.Errorf("small|huge = %v", ids(got))
	}
	if got := AndNot(huge, small); got.Len() != 1 || !got.Has(1<<31) {
		t.Errorf("huge&^small = %v", ids(got))
	}
	if got := AndNot(small, huge); !got.Empty() {
		t.Errorf("small&^huge = %v", ids(got))
	}
}

func FuzzBitmap(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	f.Add([]byte{255, 255, 255, 255, 0, 0, 0, 0, 128, 0, 0, 1, 7, 7, 7, 7})
	f.Fuzz(func(t *testing.T, data []byte) {
		var w Writer
		var sets [2]Bitmap
		models := [2]model{{}, {}}
		for len(data) >= 5 {
			op, raw := data[0], data[1:5]
			data = data[5:]
			id := uint32(raw[0]) | uint32(raw[1])<<8 | uint32(raw[2])<<16 | uint32(raw[3])<<24
			if op&8 != 0 {
				id &= 0x3ff // keep a good share of the ids close together
			}
			s := int(op>>4) & 1
			switch op & 3 {
			case 0, 1:
				sets[s], _ = w.Set(sets[s], id)
				models[s][id] = struct{}{}
			case 2:
				sets[s], _ = w.Clear(sets[s], id)
				delete(models[s], id)
			case 3:
				w.Freeze()
			}
		}
		and, or, andNot := model{}, model{}, model{}
		for id := range models[0] {
			or[id] = struct{}{}
			if _, in := models[1][id]; in {
				and[id] = struct{}{}
			} else {
				andNot[id] = struct{}{}
			}
		}
		for id := range models[1] {
			or[id] = struct{}{}
		}
		checkEqual(t, "set 0", sets[0], models[0])
		checkEqual(t, "set 1", sets[1], models[1])
		checkEqual(t, "and", And(sets[0], sets[1]), and)
		checkEqual(t, "or", Or(sets[0], sets[1]), or)
		checkEqual(t, "andnot", AndNot(sets[0], sets[1]), andNot)
	})
}
