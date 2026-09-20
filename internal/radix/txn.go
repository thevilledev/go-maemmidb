// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package radix

import "sync/atomic"

// epochCounter hands out ownership epochs. It is global on purpose: trees of
// a database and of its snapshots share nodes while having independent
// writers, so an epoch must never be issued twice, to anyone.
var epochCounter atomic.Uint64

// Notifier collects the objects that write transactions replace, so that their
// watchers can be notified once the transaction is committed and visible.
// One Notifier may be shared by all the transactions that commit together.
// It is not safe for concurrent use.
type Notifier struct {
	nodes    []*node
	leaves   []*leaf
	subtrees []*node
}

// Notify seals every recorded object: watch channels that were handed out are
// closed, and watchers that arrive late (readers still holding an older tree)
// are notified immediately. Call it after the new tree has been published so
// that woken watchers observe the new state. The Notifier is reset for reuse.
func (nf *Notifier) Notify() {
	for i, n := range nf.nodes {
		n.watch.seal()
		nf.nodes[i] = nil
	}
	for i, l := range nf.leaves {
		l.watch.seal()
		nf.leaves[i] = nil
	}
	for i, n := range nf.subtrees {
		sealSubtree(n)
		nf.subtrees[i] = nil
	}
	nf.nodes, nf.leaves, nf.subtrees = nf.nodes[:0], nf.leaves[:0], nf.subtrees[:0]
}

// Reset forgets everything recorded without notifying anyone (abort).
func (nf *Notifier) Reset() {
	clear(nf.nodes)
	clear(nf.leaves)
	clear(nf.subtrees)
	nf.nodes, nf.leaves, nf.subtrees = nf.nodes[:0], nf.leaves[:0], nf.subtrees[:0]
}

// Pending reports how many objects are recorded. Used by tests.
func (nf *Notifier) Pending() int {
	return len(nf.nodes) + len(nf.leaves) + len(nf.subtrees)
}

func sealSubtree(n *node) {
	n.watch.seal()
	if n.leaf != nil {
		n.leaf.watch.seal()
	}
	for _, k := range n.kidList() {
		sealSubtree(k)
	}
}

// Txn is a write transaction on a tree. It is a small value meant to be
// embedded; it must not be copied once used and is not safe for concurrent
// use.
//
// Ownership rule: a node whose epoch equals the transaction's current epoch
// was created by this transaction since its last Freeze, has never been
// visible to anyone else, and is mutated in place. Every other node is copied
// before it is changed and the original is recorded for notification.
type Txn struct {
	root  *node
	epoch uint64 // 0: no epoch drawn since the last Freeze
	nf    *Notifier
}

// Txn starts a write transaction. If nf is non-nil, replaced objects are
// recorded in it; pass nil for trees whose watchers must never be notified.
func (t Tree) Txn(nf *Notifier) Txn {
	return Txn{root: t.root, nf: nf}
}

// Started reports whether t was created by Tree.Txn, as opposed to being a
// zero Txn. It lets callers keep transactions in preallocated arrays.
func (t *Txn) Started() bool {
	return t.root != nil
}

// Tree returns the transaction's current state. If the result (or anything
// derived from it: an iterator, a watch channel) outlives the next write,
// call Freeze first.
func (t *Txn) Tree() Tree {
	return Tree{root: t.root}
}

// Freeze makes every node created so far immutable, in O(1): the transaction
// simply abandons its epoch and draws a fresh one on its next write. It must
// be called before an iterator or a watch channel is handed out over a tree
// with uncommitted writes, and before such a tree is shared with another
// goroutine. This upholds the invariant the lazy watch protocol rests on: a
// node with a materialised watch slot is never mutated again.
func (t *Txn) Freeze() {
	t.epoch = 0
}

// Commit returns the resulting tree. Notification is separate (see Notifier)
// so the caller can publish the tree first.
func (t *Txn) Commit() Tree {
	t.epoch = 0
	return Tree{root: t.root}
}

func (t *Txn) begin() {
	if t.epoch == 0 {
		t.epoch = epochCounter.Add(1)
	}
}

func (t *Txn) owns(n *node) bool {
	return n.epoch&^leafOwnedBit == t.epoch
}

func (t *Txn) dropNode(n *node) {
	if t.nf != nil {
		t.nf.nodes = append(t.nf.nodes, n)
	}
}

func (t *Txn) dropLeaf(l *leaf) {
	if t.nf != nil {
		t.nf.leaves = append(t.nf.leaves, l)
	}
}

func (t *Txn) dropSubtree(n *node) {
	if t.nf != nil {
		t.nf.subtrees = append(t.nf.subtrees, n)
	}
}

// copyNode returns an owned copy of n with room for extra more children. The
// child slice is always a fresh array: an owned node grows its slice in place,
// which must never be visible through the original.
func (t *Txn) copyNode(n *node, extra int) *node {
	count := n.kidCount()
	if count+extra == 0 {
		// Childless stays childless: the copy gets its own inline segment.
		c := newLeafNode(n.prefix, "")
		c.epoch, c.val, c.leaf = t.epoch, n.val, n.leaf
		return c
	}
	c := newNode(count + extra)
	c.epoch, c.val, c.leaf, c.prefix, c.bitmap = t.epoch, n.val, n.leaf, n.prefix, n.bitmap
	if n.kidCap() == 0 {
		// n may hold its segment inline; sharing it would keep n alive.
		c.prefix = cloneSegment(n.prefix)
	}
	c.setKidCount(count)
	copy(c.kidList(), n.kidList())
	return c
}

// leafNode returns a new owned node holding only a value.
func (t *Txn) leafNode(prefix []byte, v interface{}) *node {
	n := newLeafNode(bytesToString(prefix), "")
	n.epoch, n.val, n.leaf = t.epoch|leafOwnedBit, v, &leaf{}
	return n
}

// own returns a node the transaction may mutate in place: n itself if it is
// already owned, otherwise a copy that replaces n under parent (or as root).
// parent must be owned.
func (t *Txn) own(parent *node, pidx int, n *node, extra int) *node {
	var c *node
	if t.owns(n) {
		if n.kidCount()+extra <= n.kidCap() {
			return n
		}
		// An owned node that has outgrown its inline child array moves to
		// a bigger size class (with headroom, so bulk loads amortise). It
		// was never visible to anyone, so nothing needs to be recorded.
		c = t.copyNode(n, n.kidCount()+extra)
		c.epoch = n.epoch
	} else {
		c = t.copyNode(n, extra)
		t.dropNode(n)
	}
	if parent == nil {
		t.root = c
	} else {
		parent.setKid(pidx, c)
	}
	return c
}

// link replaces the node at parent.kids[pidx] (or the root).
func (t *Txn) link(parent *node, pidx int, n *node) {
	if parent == nil {
		t.root = n
	} else {
		parent.setKid(pidx, n)
	}
}

// setLeaf stores v in the owned node n.
func (t *Txn) setLeaf(n *node, v interface{}) (interface{}, bool) {
	old, existed := n.val, n.leaf != nil
	n.val = v
	if existed && n.epoch&leafOwnedBit != 0 {
		// The leaf was created by this transaction in this epoch: nobody
		// can have seen or watched it, so it can stand for the new value.
		return old, true
	}
	if existed {
		t.dropLeaf(n.leaf)
	}
	n.leaf = &leaf{}
	n.epoch |= leafOwnedBit
	return old, existed
}

// clearLeaf removes the leaf of the owned node n.
func (t *Txn) clearLeaf(n *node) {
	if n.epoch&leafOwnedBit == 0 {
		t.dropLeaf(n.leaf)
	}
	n.val, n.leaf = nil, nil
	n.epoch &^= leafOwnedBit
}

// Insert stores v under k and returns the previous value, if any. The tree
// keeps no reference to k.
func (t *Txn) Insert(k []byte, v interface{}) (interface{}, bool) {
	t.begin()

	var parent *node
	pidx := 0
	n := t.root
	search := k
	for {
		if len(search) == 0 {
			// The key ends at n.
			n = t.own(parent, pidx, n, 0)
			return t.setLeaf(n, v)
		}

		idx, ok := n.rank(search[0])
		if !ok {
			// No edge for the next byte: hang a new leaf node below n.
			n = t.own(parent, pidx, n, 1)
			n.addKid(idx, t.leafNode(search, v))
			return nil, false
		}

		child := n.kid(idx)
		common := commonPrefixLen(search, child.prefix)
		n = t.own(parent, pidx, n, 0)
		if common == len(child.prefix) {
			parent, pidx = n, idx
			n = child
			search = search[common:]
			continue
		}

		// The key diverges inside child's path segment: split it. The
		// child keeps its leaf object (and that leaf's watchers); only
		// its prefix is trimmed.
		trimmed := child
		if !t.owns(child) {
			trimmed = t.copyNode(child, 0)
			t.dropNode(child)
		}
		split := newNode(2)
		split.epoch, split.prefix = t.epoch, child.prefix[:common]
		if child.kidCap() == 0 {
			// The child may hold its segment inline: do not share it.
			split.prefix = cloneSegment(split.prefix)
		}
		trimmed.prefix = trimmed.prefix[common:]
		n.setKid(idx, split) // same label as before: bitmap unchanged

		rest := search[common:]
		if len(rest) == 0 {
			split.val, split.leaf = v, &leaf{}
			split.epoch |= leafOwnedBit
			split.setKidCount(1)
			split.setKid(0, trimmed)
		} else {
			added := t.leafNode(rest, v)
			split.setKidCount(2)
			if added.prefix[0] < trimmed.prefix[0] {
				split.setKid(0, added)
				split.setKid(1, trimmed)
			} else {
				split.setKid(0, trimmed)
				split.setKid(1, added)
			}
			split.bitmap[added.prefix[0]>>6] |= uint64(1) << (added.prefix[0] & 63)
		}
		split.bitmap[trimmed.prefix[0]>>6] |= uint64(1) << (trimmed.prefix[0] & 63)
		return nil, false
	}
}

type pathEntry struct {
	n   *node
	idx int // position, in n, of the next node on the path
}

// ownPath makes every node of a root-to-parent path owned, in place: on
// return path[i].n is the owned node of level i.
func (t *Txn) ownPath(path []pathEntry) {
	var parent *node
	pidx := 0
	for i := range path {
		parent = t.own(parent, pidx, path[i].n, 0)
		path[i].n, pidx = parent, path[i].idx
	}
}

// unlink removes the child at the end of an owned path from its parent and
// restores the tree's shape: a node left with no value and a single child is
// merged with that child. The root is exempt.
func (t *Txn) unlink(path []pathEntry) {
	last := len(path) - 1
	parent := path[last].n
	parent.delKid(path[last].idx)
	if last > 0 && parent.leaf == nil && parent.kidCount() == 1 {
		t.mergeChild(path[last-1].n, path[last-1].idx, parent)
	}
}

// Delete removes k and returns its value. Deleting a missing key leaves the
// tree -- and every watcher -- untouched.
func (t *Txn) Delete(k []byte) (interface{}, bool) {
	// Read-only descent first, so that a miss copies nothing.
	var buf [24]pathEntry
	path := buf[:0]
	n := t.root
	search := k
	for len(search) > 0 {
		idx, ok := n.rank(search[0])
		if !ok {
			return nil, false
		}
		c := n.kid(idx)
		if !c.hasPrefix(search) {
			return nil, false
		}
		path = append(path, pathEntry{n, idx})
		search = search[len(c.prefix):]
		n = c
	}
	if n.leaf == nil {
		return nil, false
	}
	old := n.val

	t.begin()
	t.ownPath(path)
	switch {
	case len(path) == 0:
		// The root is never removed or merged.
		t.clearLeaf(t.own(nil, 0, n, 0))
	case n.childless():
		// The node disappears altogether.
		if !t.owns(n) {
			t.dropNode(n)
			t.dropLeaf(n.leaf)
		} else if n.epoch&leafOwnedBit == 0 {
			t.dropLeaf(n.leaf)
		}
		t.unlink(path)
	default:
		parent, pidx := path[len(path)-1].n, path[len(path)-1].idx
		n = t.own(parent, pidx, n, 0)
		t.clearLeaf(n)
		if n.kidCount() == 1 {
			t.mergeChild(parent, pidx, n)
		}
	}
	return old, true
}

// mergeChild replaces the owned, leafless node n -- which sits at
// parent.kids[pidx] and has exactly one child -- with that child, whose path
// segment absorbs n's. The child keeps its leaf object, so the watchers of
// that key are not disturbed; the child node itself is replaced (and its
// watchers notified) unless this transaction owns it.
func (t *Txn) mergeChild(parent *node, pidx int, n *node) {
	c := n.kid(0)
	var m *node
	switch {
	case c.childless():
		// A childless result gets the joined segment inline.
		m = newLeafNode(n.prefix, c.prefix)
		m.val, m.leaf = c.val, c.leaf
		m.epoch = t.epoch
		if t.owns(c) {
			m.epoch |= c.epoch & leafOwnedBit
		}
	case t.owns(c):
		m = c
		m.prefix = n.prefix + c.prefix
	default:
		m = t.copyNode(c, 0)
		m.prefix = n.prefix + c.prefix
	}
	if !t.owns(c) {
		t.dropNode(c)
	}
	parent.setKid(pidx, m) // same label as n: the parent's bitmap is unchanged
}

// DeletePrefix removes every key starting with prefix in one subtree cut and
// reports whether a matching subtree existed.
func (t *Txn) DeletePrefix(prefix []byte) bool {
	var buf [24]pathEntry
	path := buf[:0]
	n := t.root
	search := prefix
	for len(search) > 0 {
		idx, ok := n.rank(search[0])
		if !ok {
			return false
		}
		c := n.kid(idx)
		switch {
		case c.hasPrefix(search):
			search = search[len(c.prefix):]
		case len(search) < len(c.prefix) && c.prefix[:len(search)] == string(search):
			search = nil
		default:
			return false
		}
		path = append(path, pathEntry{n, idx})
		n = c
	}

	t.begin()
	// Everything at and below n goes away; its watchers are found by
	// walking the subtree when (and only if) the transaction commits.
	t.dropSubtree(n)
	if len(path) == 0 {
		t.root = &node{epoch: t.epoch}
		return true
	}
	t.ownPath(path)
	t.unlink(path)
	return true
}
