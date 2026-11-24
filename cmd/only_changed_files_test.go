package cmd

import (
	"os"
	"path/filepath"
	"regexp"
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

	// A file in the project dir matches the bare globs.
	assert.True(t, matchesWhenModified(projectDir, patterns, "github/ib-data-platform/custom-properties/main.tf"))
	// A module file matches the resolved module glob.
	assert.True(t, matchesWhenModified(projectDir, patterns, "_modules/github/custom-properties/main.tf"))
	// An unrelated file does not match.
	assert.False(t, matchesWhenModified(projectDir, patterns, "github/ib-data-platform/other/main.tf"))
}

func TestMatchesWhenModifiedNegation(t *testing.T) {
	patterns := []string{"*.hcl", "!ignored.hcl"}
	assert.False(t, matchesWhenModified("app", patterns, "ignored.hcl"))
}

func TestDependencyDirs(t *testing.T) {
	projectDirs := map[string]struct{}{
		"github/ib-data-platform/custom-properties":          {},
		"github/ib-data-platform/rulesets/corporate/finance": {},
	}

	deps := dependencyDirs(
		"github/ib-data-platform/rulesets/corporate/finance",
		[]string{
			"*.hcl",
			"../../../custom-properties/terragrunt.hcl",
			"../../../../../_modules/github/custom-properties/*.tf*",
		},
		projectDirs,
	)
	assert.Equal(t, []string{"github/ib-data-platform/custom-properties"}, deps)
}

func TestExpandClosure(t *testing.T) {
	projects := []AtlantisProject{
		{Dir: "custom-properties", Autoplan: AutoplanConfig{WhenModified: []string{"*.hcl", "*.tf*"}}},
		{Dir: "rulesets/finance", Autoplan: AutoplanConfig{WhenModified: []string{"*.hcl", "../../custom-properties/terragrunt.hcl"}}},
		{Dir: "unrelated", Autoplan: AutoplanConfig{WhenModified: []string{"*.hcl"}}},
	}

	closure := expandClosure(projects, map[string]struct{}{"custom-properties": {}})

	assert.Contains(t, closure, "custom-properties")
	assert.Contains(t, closure, "rulesets/finance")
	assert.NotContains(t, closure, "unrelated")
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

func requireNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
