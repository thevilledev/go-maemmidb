// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package radix

// Tree is an immutable radix tree. It is a one-pointer value: copy it freely.
// A Tree obtained from a committed transaction never changes, so any number of
// goroutines may read it without coordination.
type Tree struct {
	root *node
}

// New returns an empty tree. Every tree gets its own root node: a watch on
// "the whole tree" is a watch on that node, so two trees must never share it.
func New() Tree {
	return Tree{root: &node{}}
}

// Watch identifies a point in the tree that can be watched. Its channel is
// materialised on the first call to Chan, so a Watch that is never consulted
// costs nothing. The zero Watch has a nil channel, which blocks forever.
type Watch struct {
	s *slot
}

// Chan returns the channel that is closed when a committed transaction
// replaces the watched object: for a key, when it is updated or deleted; for a
// node, when anything in its subtree changes.
func (w Watch) Chan() <-chan struct{} {
	if w.s == nil {
		return nil
	}
	return w.s.channel()
}

// Get returns the value stored under k.
func (t Tree) Get(k []byte) (interface{}, bool) {
	n := t.root
	search := k
	for len(search) > 0 {
		n = n.child(search[0])
		if n == nil || !n.hasPrefix(search) {
			return nil, false
		}
		search = search[len(n.prefix):]
	}
	if n.leaf == nil {
		return nil, false
	}
	return n.val, true
}

// GetWatch is Get plus a watch. On a hit the watch covers exactly that key.
// On a miss it covers the deepest node on the search path -- including a child
// whose prefix diverges from the key -- which is the node an insert of k would
// have to replace, so the watch fires when k is created.
func (t Tree) GetWatch(k []byte) (Watch, interface{}, bool) {
	n := t.root
	w := &n.watch
	search := k
	for len(search) > 0 {
		c := n.child(search[0])
		if c == nil {
			return Watch{w}, nil, false
		}
		n = c
		w = &n.watch
		if !n.hasPrefix(search) {
			return Watch{w}, nil, false
		}
		search = search[len(n.prefix):]
	}
	if n.leaf == nil {
		return Watch{w}, nil, false
	}
	return Watch{&n.leaf.watch}, n.val, true
}

// LongestPrefix returns the value of the longest stored key that is a prefix
// of k.
func (t Tree) LongestPrefix(k []byte) (interface{}, bool) {
	var last *node
	n := t.root
	search := k
	for {
		if n.leaf != nil {
			last = n
		}
		if len(search) == 0 {
			break
		}
		n = n.child(search[0])
		if n == nil || !n.hasPrefix(search) {
			break
		}
		search = search[len(n.prefix):]
	}
	if last == nil {
		return nil, false
	}
	return last.val, true
}

// seekPrefix finds the node whose subtree holds exactly the keys starting with
// prefix (nil if there are none) and the finest-grained watch slot for that
// prefix: the deepest node reached, whether or not the prefix was found.
func (t Tree) seekPrefix(prefix []byte) (*node, *slot) {
	n := t.root
	w := &n.watch
	search := prefix
	for len(search) > 0 {
		c := n.child(search[0])
		if c == nil {
			return nil, w
		}
		n = c
		w = &n.watch
		if n.hasPrefix(search) {
			search = search[len(n.prefix):]
			continue
		}
		if len(search) < len(n.prefix) && n.prefix[:len(search)] == string(search) {
			// The prefix ends inside this node's path segment.
			return n, w
		}
		return nil, w
	}
	return n, w
}

// FirstPrefix returns the value of the smallest key starting with prefix,
// without allocating an iterator.
func (t Tree) FirstPrefix(prefix []byte) (Watch, interface{}, bool) {
	n, w := t.seekPrefix(prefix)
	if n == nil {
		return Watch{w}, nil, false
	}
	m := n.minNode()
	if m == nil {
		return Watch{w}, nil, false
	}
	return Watch{w}, m.val, true
}

// LastPrefix returns the value of the greatest key starting with prefix.
func (t Tree) LastPrefix(prefix []byte) (Watch, interface{}, bool) {
	n, w := t.seekPrefix(prefix)
	if n == nil {
		return Watch{w}, nil, false
	}
	m := n.maxNode()
	if m == nil {
		return Watch{w}, nil, false
	}
	return Watch{w}, m.val, true
}

// Len counts the keys in the tree. It walks the whole tree; it exists for
// tests and diagnostics, not for hot paths.
func (t Tree) Len() int {
	return countLeaves(t.root)
}

func countLeaves(n *node) int {
	c := 0
	if n.leaf != nil {
		c = 1
	}
	for _, k := range n.kids {
		c += countLeaves(k)
	}
	return c
}
