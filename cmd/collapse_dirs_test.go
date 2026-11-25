package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCollapseDirsWithinLimit(t *testing.T) {
	dirs := []string{"a/b", "c/d", "e/f"}
	out := collapseDirs(dirs, 5)
	assert.Equal(t, []collapsedDir{
		{prefix: "a/b", count: 1},
		{prefix: "c/d", count: 1},
		{prefix: "e/f", count: 1},
	}, out)
}

func TestCollapseDirsCollapsesLargestFirst(t *testing.T) {
	dirs := []string{
		"a/1", "a/2", "a/3", "a/4",
		"b/1", "b/2",
		"c",
	}
	out := collapseDirs(dirs, 5)
	// 7 dirs > 5: collapse "a" (4 leaves) first → 4 lines remain.
	assert.Equal(t, []collapsedDir{
		{prefix: "a", count: 4, grouped: true},
		{prefix: "b/1", count: 1},
		{prefix: "b/2", count: 1},
		{prefix: "c", count: 1},
	}, out)
}

func TestCollapseDirsNestedPrefixes(t *testing.T) {
	dirs := []string{
		"github/ib-data-platform/repositories/operations/production/safety/ai/common",
		"github/ib-data-platform/repositories/operations/production/safety/ai/poc",
		"github/ib-data-platform/repositories/operations/production/safety/streaming/stream",
		"github/ib-data-platform/overview",
	}
	// 4 dirs, limit 2 → collapse the biggest subtree ("safety", 3 leaves) once
	// leaves 2 lines.
	out := collapseDirs(dirs, 2)
	assert.Equal(t, []collapsedDir{
		{prefix: "github/ib-data-platform/overview", count: 1},
		{prefix: "github/ib-data-platform/repositories/operations/production/safety", count: 3, grouped: true},
	}, out)
}

func TestCollapseDirsKeepsOrderSorted(t *testing.T) {
	dirs := []string{
		"z/1", "z/2", "z/3",
		"a/1", "a/2",
	}
	out := collapseDirs(dirs, 3)
	// 5 dirs > 3 → collapse "z" (3 leaves) → 3 lines, sorted by prefix.
	assert.Equal(t, []collapsedDir{
		{prefix: "a/1", count: 1},
		{prefix: "a/2", count: 1},
		{prefix: "z", count: 3, grouped: true},
	}, out)
}
