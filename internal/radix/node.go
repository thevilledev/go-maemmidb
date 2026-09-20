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
//     label-ordered child array; a child's position is the population count of
//     the bitmap below its label (the rank trick of Roaring bitmaps and HAMTs).
//     Copies are exactly sized: 8 bytes per child.
//
// The node itself is declared twice, in node_unsafe.go and node_safe.go, with
// the same fields in a different arrangement; everything else reaches a node's
// children through the accessors the two files define.
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
		return n.kid(idx)
	}
	return nil
}

// addKid inserts c at position idx. The node must be owned by the caller and
// have room for one more child (see Txn.own).
func (n *node) addKid(idx int, c *node) {
	label := c.prefix[0]
	count := n.kidCount()
	n.setKidCount(count + 1)
	kids := n.kidList()
	copy(kids[idx+1:], kids[idx:count])
	kids[idx] = c
	n.bitmap[label>>6] |= uint64(1) << (label & 63)
}

// delKid removes the child at position idx. The node must be owned.
func (n *node) delKid(idx int) {
	kids := n.kidList()
	label := kids[idx].prefix[0]
	last := len(kids) - 1
	copy(kids[idx:], kids[idx+1:])
	kids[last] = nil
	n.setKidCount(last)
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
		if n.childless() {
			return nil // only an empty root
		}
		n = n.kid(0)
	}
}

// maxNode returns the node holding the greatest key at or below n, or nil.
func (n *node) maxNode() *node {
	for count := n.kidCount(); count > 0; count = n.kidCount() {
		n = n.kid(count - 1)
	}
	if n.leaf == nil {
		return nil // only an empty root
	}
	return n
}
