// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package radix

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fired(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func build(keys ...string) Tree {
	txn := New().Txn(nil)
	for i, k := range keys {
		txn.Insert([]byte(k), i)
	}
	return txn.Commit()
}

// commit applies fn in a tracked transaction, publishes and notifies.
func commit(tr Tree, fn func(txn *Txn)) Tree {
	nf := &Notifier{}
	txn := tr.Txn(nf)
	fn(&txn)
	out := txn.Commit()
	nf.Notify()
	return out
}

func TestWatchKey(t *testing.T) {
	tr := build("foo", "foobar", "fox", "zip")

	w, v, ok := tr.GetWatch([]byte("foobar"))
	if !ok || v.(int) != 1 {
		t.Fatalf("GetWatch = %v,%v", v, ok)
	}
	key := w.Chan()
	if w.Chan() != key {
		t.Fatal("the same watch must keep returning the same channel")
	}
	pw, _, _ := tr.FirstPrefix([]byte("fo"))
	prefix := pw.Chan()
	rw, _, _ := tr.FirstPrefix(nil)
	root := rw.Chan()
	ow, _, _ := tr.GetWatch([]byte("zip"))
	other := ow.Chan()

	// An unrelated insert touches only the root path.
	tr2 := commit(tr, func(txn *Txn) { txn.Insert([]byte("zap"), 9) })
	if fired(key) || fired(prefix) {
		t.Fatal("unrelated insert fired a key/prefix watch")
	}
	if !fired(root) {
		t.Fatal("root watch must fire on any write")
	}

	// A failed delete changes nothing and notifies nobody.
	w2, _, _ := tr2.FirstPrefix(nil)
	root2 := w2.Chan()
	nf := &Notifier{}
	txn := tr2.Txn(nf)
	if _, ok := txn.Delete([]byte("fooba")); ok {
		t.Fatal("deleted a missing key")
	}
	if _, ok := txn.Delete([]byte("nope")); ok {
		t.Fatal("deleted a missing key")
	}
	if nf.Pending() != 0 || txn.Commit().root != tr2.root {
		t.Fatal("a failed delete must not copy anything")
	}
	if fired(root2) {
		t.Fatal("a failed delete fired the root watch")
	}

	// Splitting the node that holds "foobar" must not fire the key watch:
	// the leaf object survives, only the node around it is replaced.
	tr3 := commit(tr2, func(txn *Txn) { txn.Insert([]byte("fooba"), 10) })
	if fired(key) {
		t.Fatal("a split fired the watch of an unchanged key")
	}
	if !fired(prefix) {
		t.Fatal("prefix watch must fire when a key is added below it")
	}

	// Removing the split point merges the node back: still not a change
	// of "foobar".
	tr4 := commit(tr3, func(txn *Txn) { txn.Delete([]byte("fooba")) })
	if fired(key) {
		t.Fatal("a merge fired the watch of an unchanged key")
	}

	// An update replaces the leaf.
	tr5 := commit(tr4, func(txn *Txn) { txn.Insert([]byte("foobar"), 11) })
	if !fired(key) {
		t.Fatal("update did not fire the key watch")
	}
	if fired(other) {
		t.Fatal("unrelated key watch fired")
	}

	// A watch taken on the stale tree is notified immediately.
	sw, _, _ := tr4.GetWatch([]byte("foobar"))
	if !fired(sw.Chan()) {
		t.Fatal("watch on a replaced leaf must already be closed")
	}
	// ...while the live tree hands out a fresh one.
	lw, v, _ := tr5.GetWatch([]byte("foobar"))
	if v.(int) != 11 || fired(lw.Chan()) {
		t.Fatal("live watch is closed")
	}

	// Delete fires it.
	commit(tr5, func(txn *Txn) { txn.Delete([]byte("foobar")) })
	if !fired(lw.Chan()) {
		t.Fatal("delete did not fire the key watch")
	}
}

// TestWatchCreation covers "watch for a key that does not exist yet": the
// watch returned on a miss must fire when that key is created.
func TestWatchCreation(t *testing.T) {
	tr := build("alpha", "alphabet", "beta")
	misses := []string{
		"al",        // ends inside a node's path segment
		"alpha1",    // no edge below an existing node
		"alphabets", // below a leaf node
		"alpine",    // diverges inside a path segment
		"gamma",     // no edge at the root
		"",          // the empty key
	}
	for _, k := range misses {
		w, _, ok := tr.GetWatch([]byte(k))
		if ok {
			t.Fatalf("%q unexpectedly present", k)
		}
		pw, _, _ := tr.FirstPrefix([]byte(k))
		ch, pch := w.Chan(), pw.Chan()
		commit(tr, func(txn *Txn) { txn.Insert([]byte(k), 1) })
		if !fired(ch) {
			t.Fatalf("creating %q did not fire the GetWatch channel", k)
		}
		if !fired(pch) {
			t.Fatalf("creating %q did not fire the prefix watch", k)
		}
	}
}

func TestWatchDeletePrefix(t *testing.T) {
	tr := build("a/1", "a/2", "a/2/x", "b/1")
	var chans []<-chan struct{}
	for _, k := range []string{"a/1", "a/2", "a/2/x"} {
		w, _, _ := tr.GetWatch([]byte(k))
		chans = append(chans, w.Chan())
	}
	pw, _, _ := tr.FirstPrefix([]byte("a/2"))
	chans = append(chans, pw.Chan())
	bw, _, _ := tr.GetWatch([]byte("b/1"))
	keep := bw.Chan()

	before := tr
	nf := &Notifier{}
	txn := tr.Txn(nf)
	if !txn.DeletePrefix([]byte("a/")) {
		t.Fatal("DeletePrefix found nothing")
	}
	if txn.DeletePrefix([]byte("zzz")) {
		t.Fatal("DeletePrefix matched a missing prefix")
	}
	tr = txn.Commit()
	nf.Notify()
	checkSeals(t, before, tr)
	for i, ch := range chans {
		if !fired(ch) {
			t.Fatalf("watch %d inside the deleted subtree did not fire", i)
		}
	}
	if fired(keep) {
		t.Fatal("watch outside the deleted subtree fired")
	}
	if tr.Len() != 1 {
		t.Fatalf("Len = %d, want 1", tr.Len())
	}
}

// TestWatchInsideTxn: a watch handed out over uncommitted state (after the
// mandatory Freeze) fires when the same transaction writes there again and
// commits, and never fires when the transaction is abandoned.
func TestWatchInsideTxn(t *testing.T) {
	tr := build("k1")
	nf := &Notifier{}
	txn := tr.Txn(nf)
	txn.Insert([]byte("k2"), 2)

	txn.Freeze()
	w, v, ok := txn.Tree().GetWatch([]byte("k2"))
	if !ok || v.(int) != 2 {
		t.Fatal("own write not visible")
	}
	ch := w.Chan()

	txn.Insert([]byte("k2"), 3)
	if fired(ch) {
		t.Fatal("watch fired before commit")
	}
	if got, _ := txn.Tree().Get([]byte("k2")); got.(int) != 3 {
		t.Fatal("second write lost")
	}
	txn.Commit()
	nf.Notify()
	if !fired(ch) {
		t.Fatal("in-txn watch did not fire at commit")
	}

	// Abort path.
	txn = tr.Txn(nf)
	txn.Insert([]byte("k3"), 1)
	txn.Freeze()
	w, _, _ = txn.Tree().GetWatch([]byte("k3"))
	ch = w.Chan()
	txn.Insert([]byte("k3"), 2)
	nf.Reset()
	if fired(ch) {
		t.Fatal("aborted transaction fired a watch")
	}
}

// TestUntrackedNeverNotifies: a transaction without a Notifier (a snapshot
// database) must not disturb watchers of the nodes it shares.
func TestUntrackedNeverNotifies(t *testing.T) {
	tr := build("a", "ab", "b")
	w, _, _ := tr.GetWatch([]byte("ab"))
	rw, _, _ := tr.FirstPrefix(nil)
	ch, root := w.Chan(), rw.Chan()

	txn := tr.Txn(nil)
	txn.Insert([]byte("ab"), 7)
	txn.Delete([]byte("a"))
	txn.DeletePrefix([]byte("b"))
	snap := txn.Commit()
	if fired(ch) || fired(root) {
		t.Fatal("untracked transaction fired a watch")
	}
	if v, _ := tr.Get([]byte("ab")); v.(int) != 1 {
		t.Fatal("original tree changed")
	}
	if v, _ := snap.Get([]byte("ab")); v.(int) != 7 {
		t.Fatal("snapshot write lost")
	}

	// The primary lineage still notifies them later.
	commit(tr, func(txn *Txn) { txn.Insert([]byte("ab"), 8) })
	if !fired(ch) || !fired(root) {
		t.Fatal("tracked transaction did not fire")
	}
}

// TestRepeatedUpdatesAreBounded: rewriting one key many times in a single
// transaction must not grow the notification list.
func TestRepeatedUpdatesAreBounded(t *testing.T) {
	tr := build("key", "other")
	nf := &Notifier{}
	txn := tr.Txn(nf)
	for i := 0; i < 10_000; i++ {
		txn.Insert([]byte("key"), i)
	}
	if nf.Pending() > 4 {
		t.Fatalf("%d objects recorded for repeated updates of one key", nf.Pending())
	}
}

// TestEmptyRootsAreDistinct: watching "everything" in one tree must not be
// affected by writes to another tree.
func TestEmptyRootsAreDistinct(t *testing.T) {
	a, b := New(), New()
	w, _, _ := a.FirstPrefix(nil)
	ch := w.Chan()
	commit(b, func(txn *Txn) { txn.Insert([]byte("x"), 1) })
	if fired(ch) {
		t.Fatal("trees share a root node")
	}
	commit(a, func(txn *Txn) { txn.Insert([]byte("x"), 1) })
	if !fired(ch) {
		t.Fatal("root watch did not fire")
	}
}

// TestNoLostWakeups hammers the lazy watch protocol: readers materialise
// watches on published trees while a writer keeps replacing every key. A
// watch that is never closed is a lost wakeup.
func TestNoLostWakeups(t *testing.T) {
	const keys = 64
	var published atomic.Pointer[Tree]
	txn := New().Txn(nil)
	for i := 0; i < keys; i++ {
		txn.Insert([]byte(fmt.Sprintf("key-%03d", i)), 0)
	}
	first := txn.Commit()
	published.Store(&first)

	stop := make(chan struct{})
	var wg, writer sync.WaitGroup

	writer.Add(1)
	go func() { // the single writer
		defer writer.Done()
		nf := &Notifier{}
		for gen := 1; ; gen++ {
			select {
			case <-stop:
				return
			default:
			}
			txn := published.Load().Txn(nf)
			for i := 0; i < keys; i++ {
				txn.Insert([]byte(fmt.Sprintf("key-%03d", i)), gen)
			}
			if gen%7 == 0 {
				txn.Delete([]byte("key-005"))
				txn.Insert([]byte("key-005"), gen)
			}
			next := txn.Commit()
			published.Store(&next) // publish first, then notify
			nf.Notify()
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	if testing.Short() {
		deadline = time.Now().Add(300 * time.Millisecond)
	}
	errs := make(chan error, 16)
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				tr := *published.Load()
				var w Watch
				switch i % 3 {
				case 0:
					w, _, _ = tr.GetWatch([]byte(fmt.Sprintf("key-%03d", (i+r)%keys)))
				case 1:
					w, _, _ = tr.FirstPrefix([]byte("key-0"))
				default:
					w, _, _ = tr.GetWatch([]byte("missing"))
				}
				select {
				case <-w.Chan():
				case <-time.After(5 * time.Second):
					select {
					case errs <- fmt.Errorf("reader %d: watch %d never fired", r, i%3):
					default:
					}
					return
				}
			}
		}(r)
	}

	// The writer keeps going until the last reader has been woken.
	wg.Wait()
	close(stop)
	writer.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}
