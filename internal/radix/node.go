// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

// Package radix implements the storage engine of go-maemmidb: a persistent
// (immutable, structurally shared), path-compressed radix tree.
//
// It reproduces the observable semantics that go-memdb relies on from
// hashicorp/go-immutable-radix -- iteration order, prefix and lower-bound
// seeks, longest-prefix matching, and the exact granularity at which watch
// channels fire -- with a different internal design:
//
//   - Ownership epochs. A transaction may mutate a node in place iff the node
//     carries the transaction's current epoch; every other node is copied on
//     first write. There is no per-transaction cache to consult or overflow.
//   - Lazy watch channels. A node or leaf has an atomic watch slot that stays
//     nil until somebody actually watches it. Writers never allocate channels;
//     they record the objects they replace and seal them after commit.
//   - Rank-indexed children. A node keeps a 256-bit label bitmap and a dense,
//     label-ordered child slice; a child's position is the population count of
//     the bitmap below its label (the rank trick of Roaring bitmaps and HAMTs).
//     Copies are exactly sized: 8 bytes per child.
package radix

import (
	"math/bits"
	"strings"
	"sync/atomic"
)

// cell holds a materialised watch channel.
type cell struct {
	ch chan struct{}
}

// sealedCell marks a watch slot whose object has been replaced by a committed
// transaction. Its channel is closed, so a reader that loses the race against
// the seal is notified immediately -- which is correct, because the object it
// looked at is stale.
var sealedCell = func() *cell {
	ch := make(chan struct{})
	close(ch)
	return &cell{ch: ch}
}()

// slot is a lazily materialised watch channel. It only ever moves
// nil -> live cell -> sealedCell, or nil -> sealedCell.
type slot struct {
	p atomic.Pointer[cell]
}

// channel returns the slot's watch channel, creating it on first use.
func (s *slot) channel() <-chan struct{} {
	if c := s.p.Load(); c != nil {
		return c.ch
	}
	c := &cell{ch: make(chan struct{})}
	if s.p.CompareAndSwap(nil, c) {
		return c.ch
	}
	// Lost the race against another watcher or against a seal; either way
	// the winner's channel is the right one to hand out.
	return s.p.Load().ch
}

// seal closes the slot's channel, if one was ever handed out, and makes every
// later watcher of this (now stale) object fire immediately.
func (s *slot) seal() {
	if old := s.p.Swap(sealedCell); old != nil && old != sealedCell {
		close(old.ch)
	}
}

// sealed reports whether the slot has been sealed. Used by tests.
func (s *slot) sealed() bool { return s.p.Load() == sealedCell }

// leaf is the identity of one version of one key's value: the thing a watcher
// of that key watches. It is a separate object, shared by every copy of the
// node that holds the key, so that its watch slot survives node copies:
// splitting or merging a node must not notify watchers of an unchanged key --
// and a reader that arrives late, through an older copy of the node, must
// still be notified when the key finally does change.
//
// The value itself lives in the node (node.val), next to everything else a
// lookup or an iteration step needs: reads never touch the leaf object unless
// they ask for a watch.
type leaf struct {
	watch slot
}

// leafOwnedBit is stolen from node.epoch. It records that the node's current
// leaf was created in the node's own epoch, so it has never been visible to
// anyone else and need not be tracked for notification when replaced again.
const leafOwnedBit = uint64(1) << 63

// node is a radix tree node. Everything except the watch slot is immutable
// once the node is visible to anyone but the transaction that owns it.
//
// The fields a lookup touches come first. A node is allocated together with
// room for its children (see newNode), so descending one level costs one
// object -- and one allocation when a writer copies the node.
type node struct {
	// prefix is the compressed path segment leading to this node, including
	// the label byte its parent indexes it by. Empty only for the root.
	prefix string
	// kids holds the children in ascending label order; bitmap has one bit
	// per present label and kids[rank(label)] is the child for label.
	kids   []*node
	bitmap [4]uint64
	// val is the value of the key that ends exactly at this node; it is
	// meaningful iff leaf is non-nil.
	val   interface{}
	leaf  *leaf
	watch slot
	// epoch is the ownership stamp (plus leafOwnedBit).
	epoch uint64
}

// Size-classed nodes: a node header followed by an inline child array that
// node.kids points into. A *node obtained from one of these points at the
// start of the allocation, so the garbage collector keeps the array alive;
// nothing ever needs to know which class a node came from. The capacities
// line up with the allocator's size classes (112-byte header + 8 bytes/child).
type (
	node2 struct {
		node
		arr [2]*node
	}
	node4 struct {
		node
		arr [4]*node
	}
	node8 struct {
		node
		arr [8]*node
	}
	node16 struct {
		node
		arr [16]*node
	}
	node32 struct {
		node
		arr [32]*node
	}
	node64 struct {
		node
		arr [64]*node
	}
	node128 struct {
		node
		arr [128]*node
	}
	node256 struct {
		node
		arr [256]*node
	}
)

// newNode returns a node with room for capacity children.
func newNode(capacity int) *node {
	switch {
	case capacity <= 0:
		return &node{}
	case capacity <= 2:
		x := &node2{}
		x.kids = x.arr[:0]
		return &x.node
	case capacity <= 4:
		x := &node4{}
		x.kids = x.arr[:0]
		return &x.node
	case capacity <= 8:
		x := &node8{}
		x.kids = x.arr[:0]
		return &x.node
	case capacity <= 16:
		x := &node16{}
		x.kids = x.arr[:0]
		return &x.node
	case capacity <= 32:
		x := &node32{}
		x.kids = x.arr[:0]
		return &x.node
	case capacity <= 64:
		x := &node64{}
		x.kids = x.arr[:0]
		return &x.node
	case capacity <= 128:
		x := &node128{}
		x.kids = x.arr[:0]
		return &x.node
	default:
		x := &node256{}
		x.kids = x.arr[:0]
		return &x.node
	}
}

// oneByte holds every one-byte string, so that the (very common) one-byte
// path segments need no allocation.
var oneByte = func() (t [256]string) {
	for i := range t {
		t[i] = string([]byte{byte(i)})
	}
	return t
}()

// cloneSegment returns a copy of s that shares no memory with it.
func cloneSegment(s string) string {
	if len(s) == 1 {
		return oneByte[s[0]]
	}
	return strings.Clone(s)
}

// rank returns the position of label among the node's children and whether a
// child with that label exists. When it does not, the position is where such
// a child would be inserted, i.e. the index of the first child with a greater
// label.
func (n *node) rank(label byte) (int, bool) {
	w := label >> 6
	bit := uint64(1) << (label & 63)
	word := n.bitmap[w]
	idx := bits.OnesCount64(word & (bit - 1))
	switch w {
	case 3:
		idx += bits.OnesCount64(n.bitmap[2])
		fallthrough
	case 2:
		idx += bits.OnesCount64(n.bitmap[1])
		fallthrough
	case 1:
		idx += bits.OnesCount64(n.bitmap[0])
	}
	return idx, word&bit != 0
}

// child returns the child for label, or nil.
func (n *node) child(label byte) *node {
	if idx, ok := n.rank(label); ok {
		return n.kids[idx]
	}
	return nil
}

// addKid inserts c at position idx. The node must be owned by the caller.
func (n *node) addKid(idx int, c *node) {
	label := c.prefix[0]
	n.kids = append(n.kids, nil)
	copy(n.kids[idx+1:], n.kids[idx:])
	n.kids[idx] = c
	n.bitmap[label>>6] |= uint64(1) << (label & 63)
}

// delKid removes the child at position idx. The node must be owned.
func (n *node) delKid(idx int) {
	label := n.kids[idx].prefix[0]
	last := len(n.kids) - 1
	copy(n.kids[idx:], n.kids[idx+1:])
	n.kids[last] = nil
	n.kids = n.kids[:last]
	n.bitmap[label>>6] &^= uint64(1) << (label & 63)
}

// hasPrefix reports whether search starts with the node's prefix. It must only
// be called on a child found under search[0]: the first byte is then known to
// match, and a one-byte segment need not be read from memory at all. The
// string conversion in the comparison does not allocate.
func (n *node) hasPrefix(search []byte) bool {
	l := len(n.prefix)
	if l == 1 {
		return true
	}
	return len(search) >= l && string(search[1:l]) == n.prefix[1:]
}

// commonPrefixLen returns the length of the longest common prefix.
func commonPrefixLen(a []byte, b string) int {
	max := len(a)
	if len(b) < max {
		max = len(b)
	}
	i := 0
	for i < max && a[i] == b[i] {
		i++
	}
	return i
}

// minNode returns the node holding the smallest key at or below n, or nil. A
// node's own key sorts before the keys of its children.
func (n *node) minNode() *node {
	for {
		if n.leaf != nil {
			return n
		}
		if len(n.kids) == 0 {
			return nil // only an empty root
		}
		n = n.kids[0]
	}
}

// maxNode returns the node holding the greatest key at or below n, or nil.
func (n *node) maxNode() *node {
	for len(n.kids) > 0 {
		n = n.kids[len(n.kids)-1]
	}
	if n.leaf == nil {
		return nil // only an empty root
	}
	return n
}
