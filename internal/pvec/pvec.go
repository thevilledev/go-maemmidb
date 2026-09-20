// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

// Package pvec implements the row table behind go-maemmidb's bitmap indexes:
// a persistent (immutable, structurally shared) vector that maps a row id to
// the row's object.
//
// It is a trie over the id: leaves of 16 values under directory nodes of 32
// children, grown only as tall as the largest id requires. Row ids are dense,
// so nodes are plain arrays. A write copies one leaf and the two or three
// directory nodes above it; like the radix tree's transactions, a Writer
// mutates in place what it created itself since its last Freeze.
package pvec

import "sync/atomic"

const (
	leafBits  = 4
	leafSize  = 1 << leafBits
	leafMask  = leafSize - 1
	innerBits = 5
	innerSize = 1 << innerBits
	innerMask = innerSize - 1

	maxHeight = (32 - leafBits + innerBits - 1) / innerBits
)

var epochCounter atomic.Uint64

// node is a leaf (height 0) or a directory node. It is allocated together with
// the array its slice points into, see newLeaf and newInner.
type node struct {
	epoch uint64
	used  uint32        // non-nil values at or below this node
	vals  []interface{} // leaf
	kids  []*node       // directory node; nil children are empty ranges
}

type leafNode struct {
	node
	arr [leafSize]interface{}
}

type innerNode struct {
	node
	arr [innerSize]*node
}

func newLeaf(epoch uint64) *node {
	x := &leafNode{}
	x.epoch, x.vals = epoch, x.arr[:]
	return &x.node
}

func newInner(epoch uint64) *node {
	x := &innerNode{}
	x.epoch, x.kids = epoch, x.arr[:]
	return &x.node
}

// Vector is an immutable vector of values indexed by uint32; unset indexes
// hold nil. The zero value is the empty vector.
type Vector struct {
	root   *node
	height uint8
}

func span(height uint8) uint {
	return leafBits + innerBits*uint(height)
}

// Len returns the number of non-nil values.
func (v Vector) Len() int {
	if v.root == nil {
		return 0
	}
	return int(v.root.used)
}

// Same reports whether two vectors are the same object: no write separates
// them.
func (v Vector) Same(o Vector) bool { return v.root == o.root }

// Get returns the value at index i, or nil.
func (v Vector) Get(i uint32) interface{} {
	if leaf := v.leaf(i); leaf != nil {
		return leaf.vals[i&leafMask]
	}
	return nil
}

// leaf returns the leaf holding index i, or nil.
func (v Vector) leaf(i uint32) *node {
	n := v.root
	if n == nil || (span(v.height) < 32 && i>>span(v.height) != 0) {
		return nil
	}
	for h := v.height; h > 0 && n != nil; h-- {
		n = n.kids[(i>>span(h-1))&innerMask]
	}
	return n
}

// Cursor reads a vector at ascending (or otherwise clustered) indexes without
// descending from the root for each: it remembers the last leaf.
type Cursor struct {
	v    Vector
	leaf *node
	base uint32
	ok   bool
}

// Cursor returns a cursor over the vector.
func (v Vector) Cursor() Cursor { return Cursor{v: v} }

// Get returns the value at index i, or nil.
func (c *Cursor) Get(i uint32) interface{} {
	if base := i &^ leafMask; !c.ok || base != c.base {
		c.leaf, c.base, c.ok = c.v.leaf(i), base, true
	}
	if c.leaf == nil {
		return nil
	}
	return c.leaf.vals[i&leafMask]
}

// Writer changes a vector. It is not safe for concurrent use. A node whose
// epoch equals the writer's was created by it since its last Freeze and is
// mutated in place; every other node is copied first.
type Writer struct {
	epoch uint64
}

// Freeze makes every node the writer created so far immutable, in O(1). Call
// it before a Vector the writer produced is handed to anyone who may still be
// looking at it after the writer's next change.
func (w *Writer) Freeze() { w.epoch = 0 }

func (w *Writer) own(n *node, leaf bool) *node {
	if w.epoch == 0 {
		w.epoch = epochCounter.Add(1)
	}
	switch {
	case n == nil && leaf:
		return newLeaf(w.epoch)
	case n == nil:
		return newInner(w.epoch)
	case n.epoch == w.epoch:
		return n
	case leaf:
		c := newLeaf(w.epoch)
		c.used = n.used
		copy(c.vals, n.vals)
		return c
	}
	c := newInner(w.epoch)
	c.used = n.used
	copy(c.kids, n.kids)
	return c
}

// Set returns the vector with the value at index i replaced; nil unsets it.
func (w *Writer) Set(v Vector, i uint32, val interface{}) Vector {
	if val == nil {
		if v.Get(i) == nil {
			return v
		}
	} else {
		if v.root == nil {
			for span(v.height) < 32 && i>>span(v.height) != 0 {
				v.height++
			}
		}
		// Grow until the vector covers i. The old root becomes child 0.
		for span(v.height) < 32 && i>>span(v.height) != 0 {
			root := w.own(nil, false)
			root.kids[0], root.used = v.root, v.root.used
			v.root, v.height = root, v.height+1
		}
	}
	v.root = w.set(v.root, v.height, i, val)
	if v.root == nil {
		return Vector{}
	}
	return v
}

func (w *Writer) set(n *node, height uint8, i uint32, val interface{}) *node {
	if height == 0 {
		n = w.own(n, true)
		slot := i & leafMask
		switch was := n.vals[slot] != nil; {
		case was && val == nil:
			n.used--
		case !was && val != nil:
			n.used++
		}
		n.vals[slot] = val
		if n.used == 0 {
			return nil
		}
		return n
	}

	n = w.own(n, false)
	slot := (i >> span(height-1)) & innerMask
	before := uint32(0)
	if n.kids[slot] != nil {
		before = n.kids[slot].used
	}
	child := w.set(n.kids[slot], height-1, i, val)
	n.kids[slot] = child
	after := uint32(0)
	if child != nil {
		after = child.used
	}
	n.used += after - before
	if n.used == 0 {
		return nil
	}
	return n
}
