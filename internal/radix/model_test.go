// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package radix

import (
	"bytes"
	"fmt"
	"math/bits"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// model is the oracle: a plain map, sorted on demand.
type model map[string]int

func (m model) clone() model {
	c := make(model, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (m model) keys() []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// values returns the values of the keys accepted by keep, in key order.
func (m model) values(keep func(string) bool, reverse bool) []int {
	var out []int
	for _, k := range m.keys() {
		if keep(k) {
			out = append(out, m[k])
		}
	}
	if reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

func drainFwd(it *Iterator) []int {
	var out []int
	for v, ok := it.Next(); ok; v, ok = it.Next() {
		out = append(out, v.(int))
	}
	return out
}

func drainRev(it *ReverseIterator) []int {
	var out []int
	for v, ok := it.Previous(); ok; v, ok = it.Previous() {
		out = append(out, v.(int))
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkShape verifies the structural invariants of a tree.
func checkShape(t testing.TB, tr Tree) {
	t.Helper()
	if tr.root.prefix != "" {
		t.Fatalf("root has prefix %q", tr.root.prefix)
	}
	var walk func(n *node, isRoot bool)
	walk = func(n *node, isRoot bool) {
		if !isRoot {
			if n.prefix == "" {
				t.Fatalf("non-root node with empty prefix")
			}
			if n.leaf == nil && n.kidCount() < 2 {
				t.Fatalf("node %q: no leaf and %d kids (should have been merged/removed)", n.prefix, n.kidCount())
			}
		}
		pop := 0
		for _, w := range n.bitmap {
			pop += bits.OnesCount64(w)
		}
		if pop != n.kidCount() || n.childless() != (pop == 0) {
			t.Fatalf("node %q: bitmap has %d bits, %d kids", n.prefix, pop, n.kidCount())
		}
		prev := -1
		for i, k := range n.kidList() {
			label := k.prefix[0]
			if int(label) <= prev {
				t.Fatalf("node %q: kids out of order", n.prefix)
			}
			prev = int(label)
			idx, ok := n.rank(label)
			if !ok || idx != i {
				t.Fatalf("node %q: rank(%d) = %d,%v want %d,true", n.prefix, label, idx, ok, i)
			}
			walk(k, false)
		}
	}
	walk(tr.root, true)
}

// checkTree compares every read operation of tr against the model.
func checkTree(t testing.TB, tr Tree, m model, probes [][]byte) {
	t.Helper()
	checkShape(t, tr)

	if n := tr.Len(); n != len(m) {
		t.Fatalf("Len = %d, want %d", n, len(m))
	}
	for k, want := range m {
		got, ok := tr.Get([]byte(k))
		if !ok || got.(int) != want {
			t.Fatalf("Get(%q) = %v,%v want %d", k, got, ok, want)
		}
		_, got, ok = tr.GetWatch([]byte(k))
		if !ok || got.(int) != want {
			t.Fatalf("GetWatch(%q) = %v,%v want %d", k, got, ok, want)
		}
	}

	var it Iterator
	var rit ReverseIterator
	for _, p := range probes {
		ps := string(p)

		if _, present := m[ps]; !present {
			if _, ok := tr.Get(p); ok {
				t.Fatalf("Get(%q) hit, want miss", p)
			}
			if _, _, ok := tr.GetWatch(p); ok {
				t.Fatalf("GetWatch(%q) hit, want miss", p)
			}
		}

		hasPrefix := func(k string) bool { return strings.HasPrefix(k, ps) }
		want := m.values(hasPrefix, false)
		it.SeekPrefixWatch(tr, p)
		if got := drainFwd(&it); !sameInts(got, want) {
			t.Fatalf("prefix %q forward: got %v want %v", p, got, want)
		}
		wantRev := m.values(hasPrefix, true)
		rit.SeekPrefixWatch(tr, p)
		if got := drainRev(&rit); !sameInts(got, wantRev) {
			t.Fatalf("prefix %q reverse: got %v want %v", p, got, wantRev)
		}

		_, first, ok := tr.FirstPrefix(p)
		if ok != (len(want) > 0) || (ok && first.(int) != want[0]) {
			t.Fatalf("FirstPrefix(%q) = %v,%v want %v", p, first, ok, want)
		}
		_, last, ok := tr.LastPrefix(p)
		if ok != (len(want) > 0) || (ok && last.(int) != want[len(want)-1]) {
			t.Fatalf("LastPrefix(%q) = %v,%v want %v", p, last, ok, want)
		}

		wantLB := m.values(func(k string) bool { return k >= ps }, false)
		it.SeekLowerBound(tr, p)
		if got := drainFwd(&it); !sameInts(got, wantLB) {
			t.Fatalf("lower bound %q: got %v want %v", p, got, wantLB)
		}
		wantRLB := m.values(func(k string) bool { return k <= ps }, true)
		rit.SeekReverseLowerBound(tr, p)
		if got := drainRev(&rit); !sameInts(got, wantRLB) {
			t.Fatalf("reverse lower bound %q: got %v want %v", p, got, wantRLB)
		}

		wantLP, found := 0, false
		best := -1
		for k, v := range m {
			if strings.HasPrefix(ps, k) && len(k) > best {
				best, wantLP, found = len(k), v, true
			}
		}
		gotLP, ok := tr.LongestPrefix(p)
		if ok != found || (ok && gotLP.(int) != wantLP) {
			t.Fatalf("LongestPrefix(%q) = %v,%v want %v,%v", p, gotLP, ok, wantLP, found)
		}
	}
}

// reach collects every node and leaf reachable from a root.
func reach(root *node) (map[*node]bool, map[*leaf]bool) {
	nodes, leaves := map[*node]bool{}, map[*leaf]bool{}
	var walk func(n *node)
	walk = func(n *node) {
		nodes[n] = true
		if n.leaf != nil {
			leaves[n.leaf] = true
		}
		for _, k := range n.kidList() {
			walk(k)
		}
	}
	walk(root)
	return nodes, leaves
}

// checkSeals verifies the notification invariant after a tracked commit:
// exactly the objects that left the tree are sealed.
func checkSeals(t testing.TB, before, after Tree) {
	t.Helper()
	oldNodes, oldLeaves := reach(before.root)
	newNodes, newLeaves := reach(after.root)
	for n := range oldNodes {
		if !newNodes[n] && !n.watch.sealed() {
			t.Fatalf("node %q left the tree but is not sealed", n.prefix)
		}
	}
	for l := range oldLeaves {
		if !newLeaves[l] && !l.watch.sealed() {
			t.Fatalf("leaf %p left the tree but is not sealed", l)
		}
	}
	for n := range newNodes {
		if n.watch.sealed() {
			t.Fatalf("node %q is live but sealed", n.prefix)
		}
	}
	for l := range newLeaves {
		if l.watch.sealed() {
			t.Fatalf("leaf %p is live but sealed", l)
		}
	}
}

// keyGen produces short keys over a tiny alphabet, so that keys are frequently
// prefixes of each other, share long paths, and include the empty key.
type keyGen struct {
	r        *rand.Rand
	alphabet []byte
	maxLen   int
}

func (g keyGen) key() []byte {
	n := g.r.Intn(g.maxLen + 1)
	k := make([]byte, n)
	for i := range k {
		k[i] = g.alphabet[g.r.Intn(len(g.alphabet))]
	}
	return k
}

type heldIter struct {
	fwd  *Iterator
	rev  *ReverseIterator
	want []int
	desc string
}

type heldTree struct {
	tr Tree
	m  model
}

func runModel(t *testing.T, seed int64, alphabet []byte, maxLen, txns int) {
	r := rand.New(rand.NewSource(seed))
	g := keyGen{r: r, alphabet: alphabet, maxLen: maxLen}
	probes := func() [][]byte {
		ps := make([][]byte, 0, 12)
		for i := 0; i < 12; i++ {
			ps = append(ps, g.key())
		}
		return append(ps, nil)
	}

	tr := New()
	m := model{}
	nf := &Notifier{}
	var history []heldTree
	next := 0

	for i := 0; i < txns; i++ {
		tracked := r.Intn(4) != 0
		var txn Txn
		if tracked {
			txn = tr.Txn(nf)
		} else {
			txn = tr.Txn(nil)
		}
		tm := m.clone()
		var held []heldIter

		ops := 1 + r.Intn(25)
		for j := 0; j < ops; j++ {
			k := g.key()
			switch r.Intn(12) {
			case 0, 1, 2, 3, 4, 5:
				next++
				old, existed := txn.Insert(k, next)
				want, had := tm[string(k)]
				if existed != had || (had && old.(int) != want) {
					t.Fatalf("seed %d: Insert(%q) returned %v,%v want %v,%v", seed, k, old, existed, want, had)
				}
				tm[string(k)] = next
			case 6, 7, 8:
				old, existed := txn.Delete(k)
				want, had := tm[string(k)]
				if existed != had || (had && old.(int) != want) {
					t.Fatalf("seed %d: Delete(%q) returned %v,%v want %v,%v", seed, k, old, existed, want, had)
				}
				delete(tm, string(k))
			case 9:
				matched := false
				for mk := range tm {
					if bytes.HasPrefix([]byte(mk), k) {
						delete(tm, mk)
						matched = true
					}
				}
				// The empty prefix always matches: it is the root itself.
				if got := txn.DeletePrefix(k); got != matched && len(k) > 0 {
					t.Fatalf("seed %d: DeletePrefix(%q) = %v want %v", seed, k, got, matched)
				}
			case 10:
				// An iterator over uncommitted state: freeze, then keep it
				// across later writes. It must keep showing this moment.
				txn.Freeze()
				ps := string(k)
				if r.Intn(2) == 0 {
					it := &Iterator{}
					it.SeekPrefixWatch(txn.Tree(), k)
					held = append(held, heldIter{fwd: it, desc: fmt.Sprintf("fwd %q", k),
						want: tm.values(func(s string) bool { return strings.HasPrefix(s, ps) }, false)})
				} else {
					it := &ReverseIterator{}
					it.SeekReverseLowerBound(txn.Tree(), k)
					held = append(held, heldIter{rev: it, desc: fmt.Sprintf("rlb %q", k),
						want: tm.values(func(s string) bool { return s <= ps }, true)})
				}
			case 11:
				// Read-your-writes without freezing.
				got, ok := txn.Tree().Get(k)
				want, had := tm[string(k)]
				if ok != had || (had && got.(int) != want) {
					t.Fatalf("seed %d: in-txn Get(%q) = %v,%v want %v,%v", seed, k, got, ok, want, had)
				}
			}
		}

		for _, h := range held {
			var got []int
			if h.fwd != nil {
				got = drainFwd(h.fwd)
			} else {
				got = drainRev(h.rev)
			}
			if !sameInts(got, h.want) {
				t.Fatalf("seed %d: held iterator %s: got %v want %v", seed, h.desc, got, h.want)
			}
		}

		if r.Intn(5) == 0 {
			// Abort: nothing changes and nobody is notified.
			checkTree(t, txn.Tree(), tm, probes())
			nf.Reset()
			checkTree(t, tr, m, probes())
			continue
		}

		before := tr
		tr = txn.Commit()
		m = tm
		if tracked {
			nf.Notify()
			checkSeals(t, before, tr)
		} else if nf.Pending() != 0 {
			t.Fatalf("seed %d: untracked txn recorded %d objects", seed, nf.Pending())
		}
		checkTree(t, tr, m, probes())

		// Persistence: every older version still reads exactly as it did.
		if r.Intn(4) == 0 {
			history = append(history, heldTree{tr, m.clone()})
		}
		if len(history) > 0 {
			h := history[r.Intn(len(history))]
			checkTree(t, h.tr, h.m, probes())
		}
	}
}

func TestModel(t *testing.T) {
	configs := []struct {
		name     string
		alphabet []byte
		maxLen   int
	}{
		{"binary-deep", []byte{'a', 'b'}, 10},
		{"prefixy", []byte{0, 'a', 'b', 0xff}, 5},
		{"wide", func() []byte {
			a := make([]byte, 256)
			for i := range a {
				a[i] = byte(i)
			}
			return a
		}(), 3},
		{"word-boundaries", []byte{0, 63, 64, 127, 128, 191, 192, 255}, 4},
	}
	for _, c := range configs {
		t.Run(c.name, func(t *testing.T) {
			for seed := int64(1); seed <= 40; seed++ {
				runModel(t, seed, c.alphabet, c.maxLen, 60)
			}
		})
	}
}

// TestKeysAreCopied: the tree must keep no reference to the key slices it is
// given -- callers build keys in scratch buffers they reuse. Segments of every
// storage class are covered: one byte, inline (up to 64 bytes) and long.
func TestKeysAreCopied(t *testing.T) {
	for _, size := range []int{1, 2, 15, 16, 17, 32, 33, 64, 65, 200} {
		scratch := make([]byte, size)
		txn := New().Txn(nil)
		m := model{}
		for i := 0; i < 20; i++ {
			for j := range scratch {
				scratch[j] = byte('a' + (i*7+j*3)%11)
			}
			scratch[size-1] = byte('A' + i)
			txn.Insert(scratch, i)
			m[string(scratch)] = i
		}
		for j := range scratch {
			scratch[j] = '!'
		}
		tr := txn.Commit()
		checkTree(t, tr, m, [][]byte{nil, []byte("a"), []byte("!")})

		// Updates and deletes through the same scratch buffer.
		txn = tr.Txn(nil)
		i := 0
		for k := range m {
			copy(scratch, k)
			if i%2 == 0 {
				txn.Insert(scratch, -i)
				m[k] = -i
			} else {
				txn.Delete(scratch)
				delete(m, k)
			}
			i++
		}
		for j := range scratch {
			scratch[j] = '?'
		}
		checkTree(t, txn.Commit(), m, [][]byte{nil})
	}
}

// TestDeepKeys exercises the iterator stack spill and the path buffer spill.
func TestDeepKeys(t *testing.T) {
	txn := New().Txn(nil)
	m := model{}
	key := []byte{}
	for i := 0; i < 200; i++ {
		key = append(key, byte('a'+i%3))
		txn.Insert(key, i)
		m[string(key)] = i
	}
	tr := txn.Commit()
	probes := [][]byte{nil, key[:50], key[:199], key, append(append([]byte{}, key[:120]...), 'z')}
	checkTree(t, tr, m, probes)

	txn = tr.Txn(nil)
	for i := 0; i < 200; i += 2 {
		if _, ok := txn.Delete(key[:i+1]); !ok {
			t.Fatalf("delete of depth %d failed", i+1)
		}
		delete(m, string(key[:i+1]))
	}
	checkTree(t, txn.Commit(), m, probes)
}
