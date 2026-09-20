// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build memdb_safe || purego

package radix

// node is a radix tree node. Everything except the watch slot is immutable
// once the node is visible to anyone but the transaction that owns it.
//
// This is the declaration of the pure-safe build: the children are an ordinary
// slice, which points into the node's own allocation (see newNode). The default
// build does without the slice header, see node_unsafe.go.
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

func (n *node) kidCount() int         { return len(n.kids) }
func (n *node) kidCap() int           { return cap(n.kids) }
func (n *node) childless() bool       { return len(n.kids) == 0 }
func (n *node) kid(i int) *node       { return n.kids[i] }
func (n *node) setKid(i int, c *node) { n.kids[i] = c }

// kidList returns the children as a slice of the node's own storage.
func (n *node) kidList() []*node { return n.kids }

// setKidCount changes the number of children, within the node's capacity.
func (n *node) setKidCount(count int) { n.kids = n.kids[:count] }

// Size-classed nodes: a node header followed by an inline child array that
// node.kids points into. A *node obtained from one of these points at the
// start of the allocation, so the garbage collector keeps the array alive;
// nothing ever needs to know which class a node came from.
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
