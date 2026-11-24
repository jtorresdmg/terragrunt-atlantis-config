package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseNameStatus(t *testing.T) {
	output := "" +
		"M\tsrc/app/terragrunt.hcl\n" +
		"A\t_modules/widget/main.tf\n" +
		"D\tsrc/old/terragrunt.hcl\n" +
		"R100\tsrc/old\tsrc/new\n"

	entries := parseNameStatus(output)
	assert.Equal(t, []nameStatusEntry{
		{status: "M", oldPath: "src/app/terragrunt.hcl", newPath: "src/app/terragrunt.hcl"},
		{status: "A", oldPath: "_modules/widget/main.tf", newPath: "_modules/widget/main.tf"},
		{status: "D", oldPath: "src/old/terragrunt.hcl", newPath: "src/old/terragrunt.hcl"},
		{status: "R100", oldPath: "src/old", newPath: "src/new"},
	}, entries)
}

func TestGlobToRegexp(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.hcl", "main.hcl", true},
		{"*.hcl", "sub/main.hcl", false},
		{"**/*.hcl", "sub/main.hcl", true},
		{"**/*.hcl", "main.hcl", true},
		{"*.tf*", "main.tf", true},
		{"*.tf*", "main.tfvars", true},
		{"_modules/widget/*.tf*", "_modules/widget/main.tf", true},
		{"_modules/widget/*.tf*", "_modules/widget/sub/main.tf", false},
	}

	for _, tt := range tests {
		matched, err := regexp.MatchString(globToRegexp(tt.pattern), tt.path)
		assert.NoError(t, err, "%q", tt.pattern)
		assert.Equal(t, tt.want, matched, "%q vs %q", tt.pattern, tt.path)
	}
}

func TestMatchesWhenModified(t *testing.T) {
	projectDir := "github/ib-data-platform/custom-properties"
	patterns := []string{
		"*.hcl",
		"*.tf*",
		"../../../_modules/github/custom-properties/*.tf*",
	}

	assert.True(t, matchesWhenModified(projectDir, patterns, "github/ib-data-platform/custom-properties/main.tf"))
	assert.True(t, matchesWhenModified(projectDir, patterns, "_modules/github/custom-properties/main.tf"))
	assert.False(t, matchesWhenModified(projectDir, patterns, "github/ib-data-platform/other/main.tf"))
}

func TestMatchesWhenModifiedNegation(t *testing.T) {
	patterns := []string{"*.hcl", "!ignored.hcl"}
	assert.False(t, matchesWhenModified("app", patterns, "ignored.hcl"))
}

func TestFindUnitDirForFile(t *testing.T) {
	root := t.TempDir()
	unitDir := filepath.Join(root, "github", "ib-data-platform", "custom-properties")
	requireNoErr(t, os.MkdirAll(unitDir, 0o755))
	requireNoErr(t, os.WriteFile(filepath.Join(unitDir, "terragrunt.hcl"), []byte(""), 0o644))
	requireNoErr(t, os.WriteFile(filepath.Join(unitDir, "main.tf"), []byte(""), 0o644))

	moduleDir := filepath.Join(root, "_modules", "github", "custom-properties")
	requireNoErr(t, os.MkdirAll(moduleDir, 0o755))
	requireNoErr(t, os.WriteFile(filepath.Join(moduleDir, "main.tf"), []byte(""), 0o644))

	assert.Equal(t,
		"github/ib-data-platform/custom-properties",
		findUnitDirForFile(root, "github/ib-data-platform/custom-properties/main.tf"),
	)
	assert.Equal(t,
		"github/ib-data-platform/custom-properties",
		findUnitDirForFile(root, "github/ib-data-platform/custom-properties/terragrunt.hcl"),
	)
	assert.Equal(t,
		"",
		findUnitDirForFile(root, "_modules/github/custom-properties/main.tf"),
	)
}

func writeUnit(t *testing.T, root, unitDir, content string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(unitDir))
	requireNoErr(t, os.MkdirAll(dir, 0o755))
	requireNoErr(t, os.WriteFile(filepath.Join(dir, "terragrunt.hcl"), []byte(content), 0o644))
}

func TestDependencyTargetDirs(t *testing.T) {
	root := t.TempDir()
	writeUnit(t, root, "dep", `terraform { source = "${get_repo_root()}/_modules/foo" }`)
	writeUnit(t, root, "depender", `
terraform { source = "${get_repo_root()}/_modules/foo" }
dependency "some_dep" { config_path = "../dep" }
`)

	assert.Equal(t, []string{"dep"}, dependencyTargetDirs(root, "depender"))
}

func TestCollectDependencyClosure(t *testing.T) {
	root := t.TempDir()
	writeUnit(t, root, "a", `terraform { source = "${get_repo_root()}/_modules/foo" }`)
	writeUnit(t, root, "b", `
terraform { source = "${get_repo_root()}/_modules/foo" }
dependency "x" { config_path = "../a" }
`)
	writeUnit(t, root, "c", `
terraform { source = "${get_repo_root()}/_modules/foo" }
dependency "x" { config_path = "../b" }
`)

	closure := collectDependencyClosure(root, []string{"c"})
	assert.Contains(t, closure, "c")
	assert.Contains(t, closure, "b")
	assert.Contains(t, closure, "a")
}

func TestCollectReverseDependents(t *testing.T) {
	root := t.TempDir()
	writeUnit(t, root, "a", `terraform { source = "${get_repo_root()}/_modules/foo" }`)
	writeUnit(t, root, "b", `
terraform { source = "${get_repo_root()}/_modules/foo" }
dependency "x" { config_path = "../a" }
`)

	consumers := collectReverseDependents(root, map[string]struct{}{"a": {}})
	assert.Contains(t, consumers, "b")
	assert.NotContains(t, consumers, "a")
}

func TestInferWhenModifiedModuleSource(t *testing.T) {
	root := t.TempDir()
	requireNoErr(t, os.MkdirAll(filepath.Join(root, "_modules", "foo"), 0o755))
	requireNoErr(t, os.WriteFile(filepath.Join(root, "_modules", "foo", "main.tf"), []byte(""), 0o644))
	writeUnit(t, root, "unit", `terraform { source = "${get_repo_root()}/_modules/foo" }`)

	patterns := inferWhenModified(root, "unit")
	assert.Contains(t, patterns, "../_modules/foo/*.tf*")
}

func TestDiscoverUnitsByWhenModified(t *testing.T) {
	root := t.TempDir()
	requireNoErr(t, os.MkdirAll(filepath.Join(root, "_modules", "foo"), 0o755))
	requireNoErr(t, os.WriteFile(filepath.Join(root, "_modules", "foo", "main.tf"), []byte(""), 0o644))
	writeUnit(t, root, "unit", `terraform { source = "${get_repo_root()}/_modules/foo" }`)

	consumers := discoverUnitsByWhenModified(root, []string{"_modules/foo/main.tf"})
	assert.Contains(t, consumers, "unit")
}

func TestInferWhenModifiedRespectsExclude(t *testing.T) {
	root := t.TempDir()
	requireNoErr(t, os.WriteFile(filepath.Join(root, "root.hcl"), []byte(`locals {
  atlantis_when_modified_exclude = ["root.hcl"]
}`), 0o644))
	writeUnit(t, root, "unit", `terraform { source = "${get_repo_root()}/_modules/foo" }
include "root" {
  path = find_in_parent_folders("root.hcl")
}`)

	patterns := inferWhenModified(root, "unit")
	for _, p := range patterns {
		if strings.HasSuffix(filepath.ToSlash(p), "root.hcl") {
			t.Fatalf("root.hcl pattern should be excluded by atlantis_when_modified_exclude, got %q", p)
		}
	}
}

func requireNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
