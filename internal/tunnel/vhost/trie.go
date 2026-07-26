package vhost

import "fmt"

// node is one label level of the routing trie.
//
// Children are walked from the rightmost label inward, so the root's children
// are TLDs. Each node carries at most one wildcard child, which consumes
// exactly one label — that single constraint is what makes *.aaa.com decline
// to match x.bb.aaa.com.
type node struct {
	children map[string]*node
	wildcard *node
	route    *Route
}

func newNode() *node {
	return &node{children: make(map[string]*node)}
}

// insert adds a route under the given reversed labels.
func (n *node) insert(labels []string, route *Route) error {
	current := n

	for _, label := range labels {
		if label == Wildcard {
			if current.wildcard == nil {
				current.wildcard = newNode()
			}
			current = current.wildcard
			continue
		}
		child, ok := current.children[label]
		if !ok {
			child = newNode()
			current.children[label] = child
		}
		current = child
	}

	if current.route != nil {
		return fmt.Errorf("vhost: %q is already routed to tunnel %q on client %q",
			route.Pattern, current.route.ProxyName, current.route.RunID)
	}
	current.route = route
	return nil
}

// lookup walks the reversed host labels and returns the best route.
//
// Exact children are tried before the wildcard child at every level, and the
// walk only succeeds when the labels are exhausted at a node holding a route.
// Those two rules together give the whole priority order for free: exact beats
// wildcard, deeper wildcards beat shallower ones, and a wildcard can never
// swallow more than its one label.
func (n *node) lookup(labels []string) *Route {
	if len(labels) == 0 {
		return n.route
	}

	head, rest := labels[0], labels[1:]

	if child, ok := n.children[head]; ok {
		if route := child.lookup(rest); route != nil {
			return route
		}
	}
	if n.wildcard != nil {
		if route := n.wildcard.lookup(rest); route != nil {
			return route
		}
	}
	return nil
}

// walk visits every route in the trie.
func (n *node) walk(fn func(*Route)) {
	if n.route != nil {
		fn(n.route)
	}
	for _, child := range n.children {
		child.walk(fn)
	}
	if n.wildcard != nil {
		n.wildcard.walk(fn)
	}
}
