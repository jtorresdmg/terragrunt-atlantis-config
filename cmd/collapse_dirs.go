package cmd

import (
	"path/filepath"
	"sort"
	"strings"
)

// collapsedDir is one rendered entry of a collapsed directory list: either a
// single directory or a prefix group (prefix/*) covering `count` directories.
type collapsedDir struct {
	prefix  string // full dir for a leaf, or the group prefix for a group
	count   int    // number of directories represented (1 for a leaf)
	grouped bool   // true when this represents prefix/*
}

// dirTrie is a prefix tree of directory paths.
type dirTrie struct {
	name     string
	terminal bool
	children map[string]*dirTrie
	leaves   int
}

func newDirTrie() *dirTrie {
	return &dirTrie{children: map[string]*dirTrie{}}
}

func (t *dirTrie) insert(segments []string) {
	node := t
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		child, ok := node.children[seg]
		if !ok {
			child = &dirTrie{name: seg, children: map[string]*dirTrie{}}
			node.children[seg] = child
		}
		node = child
	}
	node.terminal = true
}

func (t *dirTrie) computeLeaves() int {
	total := 0
	if t.terminal {
		total = 1
	}
	for _, child := range t.children {
		total += child.computeLeaves()
	}
	t.leaves = total
	return total
}

// dirGroup is a frontier node during collapse: a trie node plus its path.
type dirGroup struct {
	node   *dirTrie
	prefix []string
}

// collapseDirs abbreviates a list of repo-relative directories into prefix
// groups so the rendered list fits within `limit` lines. Starting from the
// top-level groups, the largest subtrees are expanded first until no further
// expansion fits within the limit (single-child chains are always expanded).
func collapseDirs(dirs []string, limit int) []collapsedDir {
	out := make([]collapsedDir, 0, len(dirs))
	if limit <= 0 || len(dirs) <= limit {
		for _, d := range dirs {
			out = append(out, collapsedDir{prefix: d, count: 1})
		}
		return out
	}

	root := newDirTrie()
	for _, d := range dirs {
		root.insert(strings.Split(filepath.ToSlash(d), "/"))
	}
	root.computeLeaves()

	frontier := make([]dirGroup, 0, len(root.children))
	for name, child := range root.children {
		frontier = append(frontier, dirGroup{node: child, prefix: []string{name}})
	}

	for {
		best := -1
		for i, g := range frontier {
			if len(g.node.children) == 0 {
				continue
			}
			if len(frontier)-1+len(g.node.children) > limit {
				continue
			}
			if best == -1 || g.node.leaves > frontier[best].node.leaves {
				best = i
			}
		}
		if best == -1 {
			break
		}

		g := frontier[best]
		children := make([]dirGroup, 0, len(g.node.children))
		for name, child := range g.node.children {
			prefix := make([]string, len(g.prefix), len(g.prefix)+1)
			copy(prefix, g.prefix)
			prefix = append(prefix, name)
			children = append(children, dirGroup{node: child, prefix: prefix})
		}
		frontier = append(frontier[:best], append(children, frontier[best+1:]...)...)
	}

	sort.Slice(frontier, func(i, j int) bool {
		return strings.Join(frontier[i].prefix, "/") < strings.Join(frontier[j].prefix, "/")
	})
	for _, g := range frontier {
		p := strings.Join(g.prefix, "/")
		if len(g.node.children) == 0 {
			out = append(out, collapsedDir{prefix: p, count: 1})
		} else {
			out = append(out, collapsedDir{prefix: p, count: g.node.leaves, grouped: true})
		}
	}
	return out
}
