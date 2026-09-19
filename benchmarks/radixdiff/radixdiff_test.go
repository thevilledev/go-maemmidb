// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

// Package radixdiff is a differential test of internal/radix against the
// original hashicorp/go-immutable-radix. It drives both trees with the same
// random transactions and demands identical answers from every read
// operation -- and identical watch behaviour: after each commit, exactly the
// same set of previously obtained watch channels must have fired.
package radixdiff

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	iradix "github.com/hashicorp/go-immutable-radix"

	"github.com/thevilledev/go-maemmidb/internal/radix"
)

func fired(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

type pair struct {
	old *iradix.Tree
	new radix.Tree
}

func drainOldFwd(it *iradix.Iterator) []int {
	var out []int
	for _, v, ok := it.Next(); ok; _, v, ok = it.Next() {
		out = append(out, v.(int))
	}
	return out
}

func drainOldRev(it *iradix.ReverseIterator) []int {
	var out []int
	for _, v, ok := it.Previous(); ok; _, v, ok = it.Previous() {
		out = append(out, v.(int))
	}
	return out
}

func drainNewFwd(it *radix.Iterator) []int {
	var out []int
	for v, ok := it.Next(); ok; v, ok = it.Next() {
		out = append(out, v.(int))
	}
	return out
}

func drainNewRev(it *radix.ReverseIterator) []int {
	var out []int
	for v, ok := it.Previous(); ok; v, ok = it.Previous() {
		out = append(out, v.(int))
	}
	return out
}

func same(a, b []int) bool {
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

// compareReads checks every read operation go-memdb uses.
func compareReads(t testing.TB, p pair, probes [][]byte) {
	t.Helper()
	var nf radix.Iterator
	var nr radix.ReverseIterator
	root := p.old.Root()
	for _, k := range probes {
		ov, ook := root.Get(k)
		nv, nok := p.new.Get(k)
		if ook != nok || (ook && ov.(int) != nv.(int)) {
			t.Fatalf("Get(%q): upstream %v,%v new %v,%v", k, ov, ook, nv, nok)
		}

		_, olv, olok := root.LongestPrefix(k)
		nlv, nlok := p.new.LongestPrefix(k)
		if olok != nlok || (olok && olv.(int) != nlv.(int)) {
			t.Fatalf("LongestPrefix(%q): upstream %v,%v new %v,%v", k, olv, olok, nlv, nlok)
		}

		of := root.Iterator()
		of.SeekPrefixWatch(k)
		nf.SeekPrefixWatch(p.new, k)
		if o, n := drainOldFwd(of), drainNewFwd(&nf); !same(o, n) {
			t.Fatalf("prefix %q forward: upstream %v new %v", k, o, n)
		}

		or := root.ReverseIterator()
		or.SeekPrefixWatch(k)
		nr.SeekPrefixWatch(p.new, k)
		if o, n := drainOldRev(or), drainNewRev(&nr); !same(o, n) {
			t.Fatalf("prefix %q reverse: upstream %v new %v", k, o, n)
		}

		of = root.Iterator()
		of.SeekLowerBound(k)
		nf.SeekLowerBound(p.new, k)
		if o, n := drainOldFwd(of), drainNewFwd(&nf); !same(o, n) {
			t.Fatalf("lower bound %q: upstream %v new %v", k, o, n)
		}

		or = root.ReverseIterator()
		or.SeekReverseLowerBound(k)
		nr.SeekReverseLowerBound(p.new, k)
		if o, n := drainOldRev(or), drainNewRev(&nr); !same(o, n) {
			t.Fatalf("reverse lower bound %q: upstream %v new %v", k, o, n)
		}
	}
}

// watchPair is the same logical watch taken in both implementations.
type watchPair struct {
	desc     string
	key      []byte
	old, new <-chan struct{}
}

// takeWatches obtains, for every probe, the exact-key watch and the prefix
// watch from both trees.
func takeWatches(p pair, probes [][]byte) []watchPair {
	var ws []watchPair
	root := p.old.Root()
	for _, k := range probes {
		och, _, _ := root.GetWatch(k)
		nw, _, _ := p.new.GetWatch(k)
		ws = append(ws, watchPair{fmt.Sprintf("GetWatch(%q)", k), k, och, nw.Chan()})

		it := root.Iterator()
		och = it.SeekPrefixWatch(k)
		pw, _, _ := p.new.FirstPrefix(k)
		ws = append(ws, watchPair{fmt.Sprintf("SeekPrefixWatch(%q)", k), k, och, pw.Chan()})
	}
	return ws
}

type gen struct {
	r        *rand.Rand
	alphabet []byte
	maxLen   int
}

func (g gen) key() []byte {
	k := make([]byte, g.r.Intn(g.maxLen+1))
	for i := range k {
		k[i] = g.alphabet[g.r.Intn(len(g.alphabet))]
	}
	return k
}

// deviations counts the tolerated extra notifications (see run).
var deviations int

func underAny(k []byte, prefixes [][]byte) bool {
	for _, p := range prefixes {
		if bytes.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

func run(t testing.TB, seed int64, alphabet []byte, maxLen, txns, maxOps int) {
	r := rand.New(rand.NewSource(seed))
	g := gen{r, alphabet, maxLen}
	p := pair{old: iradix.New(), new: radix.New()}
	nf := &radix.Notifier{}
	next := 0

	for i := 0; i < txns; i++ {
		probes := make([][]byte, 0, 10)
		for j := 0; j < 9; j++ {
			probes = append(probes, g.key())
		}
		probes = append(probes, nil)
		watches := takeWatches(p, probes)

		otxn := p.old.Txn()
		otxn.TrackMutate(true)
		ntxn := p.new.Txn(nf)

		ops := 1 + r.Intn(maxOps)
		var log []string
		var cut [][]byte // prefixes removed by DeletePrefix in this txn
		for j := 0; j < ops; j++ {
			k := g.key()
			op := r.Intn(10)
			if len(log) < 64 {
				log = append(log, fmt.Sprintf("op%d(%q)", op, k))
			}
			switch op {
			case 0, 1, 2, 3, 4:
				next++
				ov, ook := otxn.Insert(k, next)
				nv, nok := ntxn.Insert(k, next)
				if ook != nok || (ook && ov.(int) != nv.(int)) {
					t.Fatalf("seed %d: Insert(%q): upstream %v,%v new %v,%v", seed, k, ov, ook, nv, nok)
				}
			case 5, 6, 7:
				ov, ook := otxn.Delete(k)
				nv, nok := ntxn.Delete(k)
				if ook != nok || (ook && ov.(int) != nv.(int)) {
					t.Fatalf("seed %d: Delete(%q): upstream %v,%v new %v,%v", seed, k, ov, ook, nv, nok)
				}
			case 8:
				if len(k) == 0 {
					continue // upstream reports true for the empty prefix even on an empty tree; so do we
				}
				o, n := otxn.DeletePrefix(k), ntxn.DeletePrefix(k)
				if o != n {
					t.Fatalf("seed %d: DeletePrefix(%q): upstream %v new %v", seed, k, o, n)
				}
				if n {
					cut = append(cut, k)
				}
			case 9:
				ov, ook := otxn.Get(k)
				nv, nok := ntxn.Tree().Get(k)
				if ook != nok || (ook && ov.(int) != nv.(int)) {
					t.Fatalf("seed %d: in-txn Get(%q): upstream %v,%v new %v,%v", seed, k, ov, ook, nv, nok)
				}
			}
		}

		if r.Intn(6) == 0 {
			// Abort: neither side may notify anyone.
			nf.Reset()
			for _, w := range watches {
				if fired(w.old) || fired(w.new) {
					t.Fatalf("seed %d: %s fired on abort", seed, w.desc)
				}
			}
			continue
		}

		p.old = otxn.CommitOnly()
		otxn.Notify()
		p.new = ntxn.Commit()
		nf.Notify()

		for _, w := range watches {
			o, n := fired(w.old), fired(w.new)
			if o == n {
				continue
			}
			if n && underAny(w.key, cut) {
				// Known upstream defect, deliberately not reproduced: when
				// DeletePrefix cuts a subtree whose root node was already
				// written earlier in the same transaction, iradix empties
				// that node before walking it to collect the channels to
				// close, so watchers inside the removed subtree are never
				// notified. We notify them. Anything else must match.
				deviations++
				continue
			}
			t.Fatalf("seed %d txn %d: %s: upstream fired=%v, new fired=%v\nops: %v", seed, i, w.desc, o, n, log)
		}
		compareReads(t, p, probes)
	}
}

var alphabets = map[string][]byte{
	"binary":  {'a', 'b'},
	"prefixy": {0, 'a', 'b', 0xff},
	"bounds":  {0, 63, 64, 127, 128, 191, 192, 255},
}

func TestDifferential(t *testing.T) {
	for name, alphabet := range alphabets {
		t.Run(name, func(t *testing.T) {
			for seed := int64(1); seed <= 60; seed++ {
				run(t, seed, alphabet, 6, 80, 12)
			}
		})
	}
	t.Logf("tolerated upstream DeletePrefix notification defects: %d", deviations)
}

// TestDifferentialLargeTxn pushes upstream past its 8192-entry tracking cap,
// where it falls back to diffing the old and new trees to find whom to notify.
func TestDifferentialLargeTxn(t *testing.T) {
	if testing.Short() {
		t.Skip("large transactions")
	}
	wide := make([]byte, 64)
	for i := range wide {
		wide[i] = byte(i * 4)
	}
	for seed := int64(1); seed <= 3; seed++ {
		run(t, seed, wide, 4, 4, 40_000)
	}
}

func FuzzDifferential(f *testing.F) {
	f.Add(int64(1), uint8(0))
	f.Add(int64(42), uint8(1))
	f.Add(int64(7), uint8(2))
	names := []string{"binary", "prefixy", "bounds"}
	f.Fuzz(func(t *testing.T, seed int64, which uint8) {
		run(t, seed, alphabets[names[int(which)%len(names)]], 6, 30, 12)
	})
}
