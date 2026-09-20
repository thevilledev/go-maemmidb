// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package radix

// frame is one level of an in-progress traversal: a node and the position of
// the next child to visit. The meaning of i differs per direction, see below.
// A node has at most 256 children, so the position fits 16 bits -- which keeps
// an iterator, the one object every Get allocates, in a smaller size class.
type frame struct {
	n *node
	i int16
}

// inlineFrames is the traversal depth an iterator handles without allocating.
// A frame is only needed per branching level, so this covers typical indexes;
// deeper paths spill to the heap.
const inlineFrames = 8

// stack is an explicit traversal stack with inline storage. It is addressed by
// depth rather than by a slice into itself, so the enclosing iterator stays a
// plain value that can be embedded and moved before first use. The inline
// frames are kept as two arrays: side by side, the padding after each 16-bit
// position would cost as much as the node pointers.
type stack struct {
	depth int
	nodes [inlineFrames]*node
	pos   [inlineFrames]int16
	spill []frame
}

func (s *stack) push(n *node, i int) {
	if s.depth < inlineFrames {
		s.nodes[s.depth], s.pos[s.depth] = n, int16(i)
	} else {
		s.spill = append(s.spill[:s.depth-inlineFrames], frame{n, int16(i)})
	}
	s.depth++
}

// top returns the node of the top frame and a pointer to its position.
func (s *stack) top() (*node, *int16) {
	if s.depth <= inlineFrames {
		return s.nodes[s.depth-1], &s.pos[s.depth-1]
	}
	f := &s.spill[s.depth-1-inlineFrames]
	return f.n, &f.i
}

func (s *stack) reset() {
	s.depth = 0
	s.spill = s.spill[:0]
}

// Iterator walks a tree in ascending key order. The zero value is an exhausted
// iterator; position it with one of the Seek methods.
//
// Forward frames: i < 0 means the node's own leaf has not been emitted yet
// (a key sorts before every key it is a prefix of); otherwise i is the index
// of the next child to descend into.
type Iterator struct {
	s stack
}

// SeekPrefixWatch positions the iterator on the keys of t that start with
// prefix and returns the finest-grained watch covering that prefix.
func (it *Iterator) SeekPrefixWatch(t Tree, prefix []byte) Watch {
	it.s.reset()
	n, w := t.seekPrefix(prefix)
	if n != nil {
		it.s.push(n, -1)
	}
	return Watch{w}
}

// SeekLowerBound positions the iterator on the smallest key >= key; iteration
// then continues to the end of the tree.
func (it *Iterator) SeekLowerBound(t Tree, key []byte) {
	it.s.reset()
	n := t.root
	search := key
	for {
		common := commonPrefixLen(search, n.prefix)
		if common < len(n.prefix) {
			// The key leaves this node's path segment. If it ends here, or
			// continues with a smaller byte, the whole subtree is greater
			// than the key; otherwise the whole subtree is smaller.
			if common == len(search) || n.prefix[common] > search[common] {
				it.s.push(n, -1)
			}
			return
		}
		search = search[common:]
		if len(search) == 0 {
			// The key ends exactly here: this node and everything below.
			it.s.push(n, -1)
			return
		}
		idx, ok := n.rank(search[0])
		if !ok {
			// No child for the next byte: continue with the first child
			// that has a greater label. This node's own key is smaller.
			it.s.push(n, idx)
			return
		}
		// Resume with the next sibling once the exact child is done.
		it.s.push(n, idx+1)
		n = n.kid(idx)
	}
}

// Next returns the next value in ascending key order.
func (it *Iterator) Next() (interface{}, bool) {
	s := &it.s
	for s.depth > 0 {
		n, i := s.top()
		if *i < 0 {
			*i = 0
			if n.leaf != nil {
				return n.val, true
			}
		}
		if int(*i) >= n.kidCount() {
			s.depth--
			continue
		}
		c := n.kid(int(*i))
		*i++
		if c.childless() {
			// A childless node always carries a value; skip the frame.
			return c.val, true
		}
		s.push(c, -1)
	}
	return nil, false
}

// ReverseIterator walks a tree in descending key order.
//
// Reverse frames: i >= 0 is the index of the next child to descend into,
// counting down; i < 0 means only the node's own leaf is left, which is
// emitted last.
type ReverseIterator struct {
	s stack
}

// SeekPrefixWatch positions the iterator on the keys of t that start with
// prefix, to be visited in descending order.
func (it *ReverseIterator) SeekPrefixWatch(t Tree, prefix []byte) Watch {
	it.s.reset()
	n, w := t.seekPrefix(prefix)
	if n != nil {
		it.s.push(n, n.kidCount()-1)
	}
	return Watch{w}
}

// SeekReverseLowerBound positions the iterator on the greatest key <= key;
// iteration then continues down to the start of the tree.
func (it *ReverseIterator) SeekReverseLowerBound(t Tree, key []byte) {
	it.s.reset()
	n := t.root
	search := key
	for {
		common := commonPrefixLen(search, n.prefix)
		if common < len(n.prefix) {
			// Mirror image of SeekLowerBound: the subtree qualifies as a
			// whole only if it is entirely smaller than the key.
			if common < len(search) && n.prefix[common] < search[common] {
				it.s.push(n, n.kidCount()-1)
			}
			return
		}
		search = search[common:]
		if len(search) == 0 {
			// The key ends exactly here. Children extend the key, so they
			// are greater; only this node's own leaf qualifies.
			it.s.push(n, -1)
			return
		}
		idx, ok := n.rank(search[0])
		// Children left of idx are smaller than the key, as is this node's
		// own leaf: they follow once the exact child (if any) is done.
		it.s.push(n, idx-1)
		if !ok {
			return
		}
		n = n.kid(idx)
	}
}

// Previous returns the next value in descending key order.
func (it *ReverseIterator) Previous() (interface{}, bool) {
	s := &it.s
	for s.depth > 0 {
		n, i := s.top()
		if *i < 0 {
			s.depth--
			if n.leaf != nil {
				return n.val, true
			}
			continue
		}
		c := n.kid(int(*i))
		*i--
		if c.childless() {
			return c.val, true
		}
		s.push(c, c.kidCount()-1)
	}
	return nil, false
}
