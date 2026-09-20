// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package bitmap

import "math/bits"

// The set operations build their results from untouched operand subtrees
// wherever they can: a result node is only allocated where the two operands
// actually differ. Results are immutable (their new nodes carry epoch 0, which
// no Writer ever owns).

// And returns the intersection of a and b.
func And(a, b Bitmap) Bitmap {
	if a.root == nil || b.root == nil {
		return Bitmap{}
	}
	// Ids beyond the lower tree's span cannot be common: only child 0 of the
	// taller tree matters, all the way down to the lower one's height.
	for a.height() > b.height() {
		if a.root.present&1 == 0 {
			return Bitmap{}
		}
		a.root = a.root.kids[0]
	}
	for b.height() > a.height() {
		if b.root.present&1 == 0 {
			return Bitmap{}
		}
		b.root = b.root.kids[0]
	}
	return canonical(and(a.root, b.root, a.height()))
}

// Or returns the union of a and b.
func Or(a, b Bitmap) Bitmap {
	switch {
	case a.root == nil:
		return b
	case b.root == nil:
		return a
	}
	a, b = level(a, b)
	return canonical(or(a.root, b.root, a.height()))
}

// AndNot returns the ids of a that are not in b.
func AndNot(a, b Bitmap) Bitmap {
	if a.root == nil || b.root == nil {
		return a
	}
	for b.height() > a.height() {
		// What b holds beyond a's span is irrelevant.
		if b.root.present&1 == 0 {
			return a
		}
		b.root = b.root.kids[0]
	}
	a, b = level(a, b)
	return canonical(andNot(a.root, b.root, a.height()))
}

// level lifts the lower of two non-empty trees to the height of the taller.
func level(a, b Bitmap) (Bitmap, Bitmap) {
	for a.height() < b.height() {
		a.root = &node{count: a.root.count, height: a.height() + 1, present: 1, kids: []*node{a.root}}
	}
	for b.height() < a.height() {
		b.root = &node{count: b.root.count, height: b.height() + 1, present: 1, kids: []*node{b.root}}
	}
	return a, b
}

// canonical drops the directory levels that only lead to child 0, so that a
// set has the same height however it was arrived at.
func canonical(root *node) Bitmap {
	if root == nil {
		return Bitmap{}
	}
	for root.height > 0 && root.present == 1 {
		root = root.kids[0]
	}
	return Bitmap{root: root}
}

func popcount(w [4]uint64) uint32 {
	return uint32(bits.OnesCount64(w[0]) + bits.OnesCount64(w[1]) + bits.OnesCount64(w[2]) + bits.OnesCount64(w[3]))
}

// builder assembles the children of a result directory node and notices when
// the result is, child for child, one of the operands.
type builder struct {
	present uint64
	count   uint32
	kids    []*node
	buf     [8]*node
}

func (r *builder) add(slot uint, child *node) {
	if r.kids == nil {
		r.kids = r.buf[:0]
	}
	r.kids = append(r.kids, child)
	r.present |= 1 << slot
	r.count += child.count
}

// is reports whether the assembled node equals n.
func (r *builder) is(n *node) bool {
	if r.present != n.present || r.count != n.count {
		return false
	}
	for i, k := range r.kids {
		if k != n.kids[i] {
			return false
		}
	}
	return true
}

func (r *builder) node(height uint8) *node {
	if r.present == 0 {
		return nil
	}
	return &node{count: r.count, height: height, present: r.present, kids: append([]*node(nil), r.kids...)}
}

func and(a, b *node, height uint8) *node {
	if a == b {
		return a
	}
	if height == 0 {
		w := [4]uint64{a.words[0] & b.words[0], a.words[1] & b.words[1], a.words[2] & b.words[2], a.words[3] & b.words[3]}
		switch {
		case w == [4]uint64{}:
			return nil
		case w == a.words:
			return a
		case w == b.words:
			return b
		}
		return &node{count: popcount(w), words: w}
	}

	var r builder
	for common := a.present & b.present; common != 0; common &= common - 1 {
		slot := uint(bits.TrailingZeros64(common))
		ak := a.kids[bits.OnesCount64(a.present&(1<<slot-1))]
		bk := b.kids[bits.OnesCount64(b.present&(1<<slot-1))]
		if child := and(ak, bk, height-1); child != nil {
			r.add(slot, child)
		}
	}
	switch {
	case r.is(a):
		return a
	case r.is(b):
		return b
	}
	return r.node(height)
}

func or(a, b *node, height uint8) *node {
	if a == b {
		return a
	}
	if height == 0 {
		w := [4]uint64{a.words[0] | b.words[0], a.words[1] | b.words[1], a.words[2] | b.words[2], a.words[3] | b.words[3]}
		switch {
		case w == a.words:
			return a
		case w == b.words:
			return b
		}
		return &node{count: popcount(w), words: w}
	}

	var r builder
	for union := a.present | b.present; union != 0; union &= union - 1 {
		slot := uint(bits.TrailingZeros64(union))
		bit := uint64(1) << slot
		switch {
		case b.present&bit == 0:
			r.add(slot, a.kids[bits.OnesCount64(a.present&(bit-1))])
		case a.present&bit == 0:
			r.add(slot, b.kids[bits.OnesCount64(b.present&(bit-1))])
		default:
			r.add(slot, or(a.kids[bits.OnesCount64(a.present&(bit-1))], b.kids[bits.OnesCount64(b.present&(bit-1))], height-1))
		}
	}
	switch {
	case r.is(a):
		return a
	case r.is(b):
		return b
	}
	return r.node(height)
}

func andNot(a, b *node, height uint8) *node {
	if a == b {
		return nil
	}
	if height == 0 {
		w := [4]uint64{a.words[0] &^ b.words[0], a.words[1] &^ b.words[1], a.words[2] &^ b.words[2], a.words[3] &^ b.words[3]}
		switch {
		case w == [4]uint64{}:
			return nil
		case w == a.words:
			return a
		}
		return &node{count: popcount(w), words: w}
	}

	var r builder
	for rest := a.present; rest != 0; rest &= rest - 1 {
		slot := uint(bits.TrailingZeros64(rest))
		bit := uint64(1) << slot
		ak := a.kids[bits.OnesCount64(a.present&(bit-1))]
		if b.present&bit == 0 {
			r.add(slot, ak)
		} else if child := andNot(ak, b.kids[bits.OnesCount64(b.present&(bit-1))], height-1); child != nil {
			r.add(slot, child)
		}
	}
	if r.is(a) {
		return a
	}
	return r.node(height)
}
