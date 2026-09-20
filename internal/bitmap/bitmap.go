// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

// Package bitmap implements the sets of row ids behind go-maemmidb's bitmap
// indexes: a persistent (immutable, structurally shared) compressed bitmap
// over uint32.
//
// The layout follows Roaring bitmaps -- a sparse directory of chunks, addressed
// by the high bits of the id, each child found by a population count over a
// presence bitmap -- with the proportions changed for a different cost model.
// A Roaring container holds 2^16 ids and may be 8 kB large, which is what a
// mutable bitmap wants and what a persistent one cannot afford: every write of
// a single-row transaction would copy it. Here a chunk is 256 ids (four words)
// and the directory is a 64-ary trie over it, grown only as tall as the largest
// id requires, so a write copies one 80-byte chunk and two or three small
// directory nodes, less than one write to a radix tree index. Dense sets cost
// under three bits per id; a sparse one costs a chunk per 256-id range it
// touches, so an index with nearly as many values as rows is better served by
// an ordinary index.
//
// A Bitmap value is immutable and safe for concurrent use. It is changed
// through a Writer, which -- like the radix tree's transactions -- mutates in
// place the nodes it created itself since its last Freeze and copies all
// others. Set algebra (And, Or, AndNot) never copies more than it must: a
// subtree that comes out equal to an operand's is that operand's.
package bitmap

import (
	"math/bits"
	"sync/atomic"
)

const (
	chunkBits = 8 // ids per chunk: 1 << chunkBits
	chunkMask = 1<<chunkBits - 1
	fanBits   = 6 // children per directory node: 1 << fanBits
	fanMask   = 1<<fanBits - 1

	// maxHeight is the number of directory levels that covers every uint32.
	maxHeight = (32 - chunkBits + fanBits - 1) / fanBits
)

// epochCounter hands out ownership epochs, see Writer.
var epochCounter atomic.Uint64

// node is a chunk (height 0) or a directory node (height > 0).
type node struct {
	epoch  uint64
	count  uint32 // ids at or below this node
	height uint8  // directory levels between this node and the chunks
	// A chunk: its 256 bits.
	words [4]uint64
	// A directory node: present has bit i set if child i exists, and
	// kids[popcount(present below i)] is that child.
	present uint64
	kids    []*node
}

// Bitmap is an immutable set of uint32. The zero value is the empty set. It is
// a one-pointer value, so storing it in an interface does not allocate.
type Bitmap struct {
	root *node
}

// height returns the directory levels above the chunks of a non-empty set.
func (b Bitmap) height() uint8 { return b.root.height }

// span returns how many low bits of an id a tree of the given height covers.
func span(height uint8) uint {
	return chunkBits + fanBits*uint(height)
}

// Len returns the number of ids in the set.
func (b Bitmap) Len() int {
	if b.root == nil {
		return 0
	}
	return int(b.root.count)
}

// Empty reports whether the set has no ids.
func (b Bitmap) Empty() bool { return b.root == nil }

// Same reports whether two sets are the same object: one was derived from the
// other without any change. It is a cheap, conservative test for equality.
func (b Bitmap) Same(o Bitmap) bool { return b.root == o.root }

// Has reports whether id is in the set.
func (b Bitmap) Has(id uint32) bool {
	n := b.root
	if n == nil || (span(n.height) < 32 && id>>span(n.height) != 0) {
		return false
	}
	for h := n.height; h > 0; h-- {
		slot := uint(id>>span(h-1)) & fanMask
		if n.present&(1<<slot) == 0 {
			return false
		}
		n = n.kids[bits.OnesCount64(n.present&(1<<slot-1))]
	}
	low := id & chunkMask
	return n.words[low>>6]&(1<<(low&63)) != 0
}

// Min returns the smallest id of the set.
func (b Bitmap) Min() (uint32, bool) {
	n := b.root
	if n == nil {
		return 0, false
	}
	var id uint32
	for h := n.height; h > 0; h-- {
		id |= uint32(bits.TrailingZeros64(n.present)) << span(h-1)
		n = n.kids[0]
	}
	for w, word := range n.words {
		if word != 0 {
			return id | uint32(w<<6+bits.TrailingZeros64(word)), true
		}
	}
	panic("bitmap: empty chunk in a tree")
}

// Writer changes a set. It is a small value meant to be embedded; it is not
// safe for concurrent use.
//
// Ownership rule: a node whose epoch equals the writer's was created by this
// writer since its last Freeze, has never been visible to anyone else, and is
// mutated in place. Every other node is copied before it is changed. One
// Writer may be used for any number of sets.
type Writer struct {
	epoch uint64
}

// Freeze makes every node the writer created so far immutable, in O(1). It
// must be called before a Bitmap the writer produced is handed to anyone who
// may still be looking at it after the writer's next change.
func (w *Writer) Freeze() { w.epoch = 0 }

func (w *Writer) begin() {
	if w.epoch == 0 {
		w.epoch = epochCounter.Add(1)
	}
}

// own returns a node the writer may mutate: n itself, or a copy with room for
// extra more children. Drawing the epoch first is what keeps the immutable
// nodes of epoch 0 (frozen by nobody: results of And, Or and AndNot) from ever
// looking owned.
func (w *Writer) own(n *node, extra int) *node {
	w.begin()
	if n.epoch == w.epoch {
		return n
	}
	c := &node{epoch: w.epoch, count: n.count, height: n.height, words: n.words, present: n.present}
	if len(n.kids)+extra > 0 {
		c.kids = make([]*node, len(n.kids), len(n.kids)+extra)
		copy(c.kids, n.kids)
	}
	return c
}

// Set returns the set with id added, and whether it was absent before.
func (w *Writer) Set(b Bitmap, id uint32) (Bitmap, bool) {
	if b.root == nil {
		var height uint8
		for span(height) < 32 && id>>span(height) != 0 {
			height++
		}
		w.begin()
		return Bitmap{root: w.branch(height, id)}, true
	}
	// Grow the directory until it covers id. The old root becomes child 0.
	for h := b.height(); span(h) < 32 && id>>span(h) != 0; h++ {
		w.begin()
		b.root = &node{epoch: w.epoch, count: b.root.count, height: h + 1, present: 1, kids: []*node{b.root}}
	}
	root, added := w.set(b.root, b.height(), id)
	b.root = root
	return b, added
}

func (w *Writer) set(n *node, height uint8, id uint32) (*node, bool) {
	if height == 0 {
		low := id & chunkMask
		bit := uint64(1) << (low & 63)
		if n.words[low>>6]&bit != 0 {
			return n, false
		}
		n = w.own(n, 0)
		n.words[low>>6] |= bit
		n.count++
		return n, true
	}

	slot := uint(id>>span(height-1)) & fanMask
	idx := bits.OnesCount64(n.present & (1<<slot - 1))
	if n.present&(1<<slot) == 0 {
		// A new branch: a chain of single-child nodes down to a new chunk.
		n = w.own(n, 1) // draws the epoch that branch needs
		n.kids = append(n.kids, nil)
		copy(n.kids[idx+1:], n.kids[idx:])
		n.kids[idx] = w.branch(height-1, id)
		n.present |= 1 << slot
		n.count++
		return n, true
	}
	child, added := w.set(n.kids[idx], height-1, id)
	if !added {
		return n, false
	}
	n = w.own(n, 0)
	n.kids[idx] = child
	n.count++
	return n, true
}

// branch returns a new subtree of the given height holding only id. The
// writer's epoch must have been drawn.
func (w *Writer) branch(height uint8, id uint32) *node {
	if height == 0 {
		n := &node{epoch: w.epoch, count: 1}
		low := id & chunkMask
		n.words[low>>6] = 1 << (low & 63)
		return n
	}
	slot := uint(id>>span(height-1)) & fanMask
	return &node{epoch: w.epoch, count: 1, height: height, present: 1 << slot, kids: []*node{w.branch(height-1, id)}}
}

// Clear returns the set with id removed, and whether it was present.
func (w *Writer) Clear(b Bitmap, id uint32) (Bitmap, bool) {
	if !b.Has(id) {
		return b, false
	}
	b.root = w.clear(b.root, b.height(), id)
	if b.root == nil {
		return Bitmap{}, true
	}
	// Keep the tree as low as its ids allow: drop directory levels that only
	// lead to child 0.
	for b.root.height > 0 && b.root.present == 1 {
		b.root = b.root.kids[0]
	}
	return b, true
}

// clear removes id, which is known to be present, and returns nil if nothing
// is left.
func (w *Writer) clear(n *node, height uint8, id uint32) *node {
	if n.count == 1 {
		return nil
	}
	n = w.own(n, 0)
	n.count--
	if height == 0 {
		low := id & chunkMask
		n.words[low>>6] &^= 1 << (low & 63)
		return n
	}
	slot := uint(id>>span(height-1)) & fanMask
	idx := bits.OnesCount64(n.present & (1<<slot - 1))
	if child := w.clear(n.kids[idx], height-1, id); child != nil {
		n.kids[idx] = child
		return n
	}
	last := len(n.kids) - 1
	copy(n.kids[idx:], n.kids[idx+1:])
	n.kids[last] = nil
	n.kids = n.kids[:last]
	n.present &^= 1 << slot
	return n
}

// Iterator walks a set in ascending order. The zero value is exhausted.
type Iterator struct {
	depth int
	path  [maxHeight]frame
	chunk *node
	base  uint32 // id of the chunk's bit 0
	word  int
	bits  uint64 // what is left of chunk.words[word]
}

type frame struct {
	n       *node
	next    int    // index of the next child to visit
	present uint64 // presence bits of the children not visited yet
	base    uint32
}

// Iterate returns an iterator over the set.
func (b Bitmap) Iterate() Iterator {
	var it Iterator
	if b.root == nil {
		return it
	}
	if b.height() == 0 {
		it.chunk, it.bits = b.root, b.root.words[0]
		return it
	}
	it.path[0] = frame{n: b.root, present: b.root.present}
	it.depth = 1
	it.descend(b.height())
	return it
}

// descend moves from the top frame down to the next chunk. height is the
// height of the top frame's node.
func (it *Iterator) descend(height uint8) {
	for {
		f := &it.path[it.depth-1]
		slot := uint(bits.TrailingZeros64(f.present))
		f.present &= f.present - 1
		child := f.n.kids[f.next]
		f.next++
		base := f.base | uint32(slot)<<span(height-1)
		height--
		if height == 0 {
			it.chunk, it.base, it.word, it.bits = child, base, 0, child.words[0]
			return
		}
		it.path[it.depth] = frame{n: child, present: child.present, base: base}
		it.depth++
	}
}

// Next returns the next id.
func (it *Iterator) Next() (uint32, bool) {
	for {
		if it.bits != 0 {
			id := it.base | uint32(it.word<<6+bits.TrailingZeros64(it.bits))
			it.bits &= it.bits - 1
			return id, true
		}
		if it.chunk == nil {
			return 0, false
		}
		if it.word < 3 {
			it.word++
			it.bits = it.chunk.words[it.word]
			continue
		}
		// The chunk is done: back up to a node with children left.
		it.chunk = nil
		height := uint8(1)
		for it.depth > 0 && it.path[it.depth-1].present == 0 {
			it.depth--
			height++
		}
		if it.depth == 0 {
			return 0, false
		}
		it.descend(height)
	}
}
