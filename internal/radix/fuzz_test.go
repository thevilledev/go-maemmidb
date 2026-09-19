// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package radix

import (
	"strings"
	"testing"
)

// FuzzTreeOps interprets the input as a program of tree operations and checks
// the tree against the map oracle, including the notification invariant at
// every tracked commit.
//
// Encoding: each instruction is an opcode byte, a length byte and that many
// key bytes; key bytes are folded onto a four-symbol alphabet so that keys
// collide, nest and share prefixes as often as possible.
func FuzzTreeOps(f *testing.F) {
	f.Add([]byte("\x00\x03abc\x00\x02ab\x05\x00\x01\x03abc\x05\x00"))
	f.Add([]byte("\x00\x00\x00\x01a\x02\x00\x05\x00\x00\x02aa\x04\x01a\x00\x03aab\x05\x00"))
	f.Add([]byte("\x00\x04abab\x00\x04abba\x00\x02ab\x03\x01a\x01\x02ab\x05\x00\x02\x02ab\x05\x00"))

	alphabet := []byte{0, 'a', 'b', 0xff}

	f.Fuzz(func(t *testing.T, prog []byte) {
		tr := New()
		m := model{}
		nf := &Notifier{}
		txn := tr.Txn(nf)
		tm := m.clone()
		var probes [][]byte
		var held []heldIter
		next := 0

		checkHeld := func() {
			for _, h := range held {
				if got := drainFwd(h.fwd); !sameInts(got, h.want) {
					t.Fatalf("held iterator %s: got %v want %v", h.desc, got, h.want)
				}
			}
			held = held[:0]
		}

		for len(prog) >= 2 {
			op, n := prog[0]%7, int(prog[1]%6)
			prog = prog[2:]
			if n > len(prog) {
				n = len(prog)
			}
			key := make([]byte, n)
			for i := range key {
				key[i] = alphabet[prog[i]%4]
			}
			prog = prog[n:]
			if len(probes) < 24 {
				probes = append(probes, key)
			}

			switch op {
			case 0: // insert
				next++
				old, existed := txn.Insert(key, next)
				want, had := tm[string(key)]
				if existed != had || (had && old.(int) != want) {
					t.Fatalf("Insert(%q) = %v,%v want %v,%v", key, old, existed, want, had)
				}
				tm[string(key)] = next
			case 1: // delete
				old, existed := txn.Delete(key)
				want, had := tm[string(key)]
				if existed != had || (had && old.(int) != want) {
					t.Fatalf("Delete(%q) = %v,%v want %v,%v", key, old, existed, want, had)
				}
				delete(tm, string(key))
			case 2: // delete prefix
				matched := false
				for k := range tm {
					if strings.HasPrefix(k, string(key)) {
						delete(tm, k)
						matched = true
					}
				}
				if got := txn.DeletePrefix(key); got != matched && len(key) > 0 {
					t.Fatalf("DeletePrefix(%q) = %v want %v", key, got, matched)
				}
			case 3: // iterator over uncommitted state
				txn.Freeze()
				it := &Iterator{}
				it.SeekLowerBound(txn.Tree(), key)
				ks := string(key)
				held = append(held, heldIter{fwd: it, desc: "lb " + ks,
					want: tm.values(func(s string) bool { return s >= ks }, false)})
			case 4: // read-your-writes
				got, ok := txn.Tree().Get(key)
				want, had := tm[string(key)]
				if ok != had || (had && got.(int) != want) {
					t.Fatalf("in-txn Get(%q) = %v,%v want %v,%v", key, got, ok, want, had)
				}
			case 5: // commit
				checkHeld()
				before := tr
				tr = txn.Commit()
				m = tm
				nf.Notify()
				checkSeals(t, before, tr)
				checkTree(t, tr, m, probes)
				txn = tr.Txn(nf)
				tm = m.clone()
			case 6: // abort
				checkHeld()
				nf.Reset()
				checkTree(t, tr, m, probes)
				txn = tr.Txn(nf)
				tm = m.clone()
			}
		}

		checkHeld()
		checkTree(t, txn.Tree(), tm, append(probes, nil))
	})
}
