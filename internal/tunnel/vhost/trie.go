package vhost

import "fmt"

type node struct {
	children map[string]*node
	wildcard *node
	route    *Route
}

func newNode() *node {
	return &node{children: make(map[string]*node)}
}

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
