package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
)

const terragruntUnitFile = "terragrunt.hcl"

// nameStatusEntry is a single line from `git diff --name-status`.
type nameStatusEntry struct {
	status  string
	oldPath string
	newPath string
}

// resolveBaseRef returns the git ref used to detect changed files.
// Precedence: --base-ref flag > origin/$BASE_BRANCH_NAME > origin/main.
func resolveBaseRef() string {
	if strings.TrimSpace(baseRef) != "" {
		return strings.TrimSpace(baseRef)
	}
	if branch := strings.TrimSpace(os.Getenv("BASE_BRANCH_NAME")); branch != "" {
		return "origin/" + branch
	}
	return "origin/main"
}

// gitDiffNameStatus returns the name-status entries between ref...HEAD.
func gitDiffNameStatus(repoDir, ref string) ([]nameStatusEntry, error) {
	cmd := exec.Command("git", "diff", "--name-status", ref+"...HEAD")
	cmd.Dir = filepath.Clean(repoDir)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseNameStatus(string(out)), nil
}

// parseNameStatus parses the output of `git diff --name-status`.
func parseNameStatus(output string) []nameStatusEntry {
	entries := []nameStatusEntry{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		if strings.HasPrefix(status, "R") && len(parts) >= 3 {
			entries = append(entries, nameStatusEntry{
				status: status, oldPath: parts[1], newPath: parts[2],
			})
			continue
		}
		entries = append(entries, nameStatusEntry{
			status: status, oldPath: parts[1], newPath: parts[1],
		})
	}
	return entries
}

// findUnitDirForFile walks up from a repo-relative file to the nearest
// terragrunt.hcl, returning the repo-relative unit dir ("" if none).
func findUnitDirForFile(gitRoot, repoRelativeFile string) string {
	root, err := filepath.Abs(gitRoot)
	if err != nil {
		return ""
	}
	root = filepath.Clean(root)
	abs, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(repoRelativeFile)))
	if err != nil {
		return ""
	}
	dir := abs
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		dir = filepath.Dir(abs)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, terragruntUnitFile)); err == nil {
			rel, relErr := filepath.Rel(root, dir)
			if relErr != nil {
				return ""
			}
			return filepath.ToSlash(rel)
		}
		if dir == root {
			return ""
		}
		dir = filepath.Dir(dir)
	}
}

// globToRegexp converts a path-aware glob to an anchored regular expression.
// `*` matches within a path segment, `**` matches across separators.
func globToRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(pattern[i:], "/**") && i+3 == len(pattern):
			b.WriteString("(?:/.*)?")
			i += 3
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pattern[i] == '*':
			b.WriteString("[^/]*")
			i++
		case pattern[i] == '?':
			b.WriteString("[^/]")
			i++
		case pattern[i] == '\\' && i+1 < len(pattern):
			b.WriteString(regexp.QuoteMeta(string(pattern[i+1])))
			i += 2
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}

// matchesWhenModified reports whether a repo-relative file matches any
// when_modified pattern of a project (patterns are relative to the project dir).
func matchesWhenModified(projectDir string, whenModified []string, repoRelativeFile string) bool {
	repoRelativeFile = filepath.ToSlash(filepath.Clean(repoRelativeFile))
	for _, pattern := range whenModified {
		pattern = filepath.ToSlash(pattern)
		if pattern == "" || strings.HasPrefix(pattern, "!") {
			continue
		}
		repoPattern := repoRelativeWhenModifiedPattern(projectDir, pattern)
		matched, err := regexp.MatchString(globToRegexp(repoPattern), repoRelativeFile)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// dependencyDirs returns the project dirs this project depends on, derived
// from when_modified entries that resolve to another project's dir. This is
// the same derivation the `depends_on` computation uses.
func dependencyDirs(projectDir string, whenModified []string, projectDirs map[string]struct{}) []string {
	deps := []string{}
	for _, pattern := range whenModified {
		pattern = filepath.ToSlash(pattern)
		if pattern == "" || strings.HasPrefix(pattern, "!") {
			continue
		}
		joined := filepath.ToSlash(filepath.Clean(filepath.Join(projectDir, pattern)))
		depPath := filepath.ToSlash(filepath.Dir(joined))
		if depPath == projectDir {
			continue
		}
		if _, ok := projectDirs[depPath]; ok {
			deps = append(deps, depPath)
		}
	}
	return uniqueStrings(deps)
}

// expandClosure returns the affected dirs plus their transitive forward
// (dependencies) and reverse (dependents) closure.
func expandClosure(projects []AtlantisProject, affected map[string]struct{}) map[string]struct{} {
	projectDirs := make(map[string]struct{}, len(projects))
	for i := range projects {
		projectDirs[projects[i].Dir] = struct{}{}
	}

	forward := make(map[string][]string, len(projects))
	reverse := make(map[string][]string, len(projects))
	for i := range projects {
		deps := dependencyDirs(projects[i].Dir, projects[i].Autoplan.WhenModified, projectDirs)
		forward[projects[i].Dir] = deps
		for _, dep := range deps {
			reverse[dep] = append(reverse[dep], projects[i].Dir)
		}
	}

	closure := make(map[string]struct{}, len(affected))
	queue := make([]string, 0, len(affected))
	for dir := range affected {
		closure[dir] = struct{}{}
		queue = append(queue, dir)
	}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		for _, dep := range forward[dir] {
			if _, ok := closure[dep]; !ok {
				closure[dep] = struct{}{}
				queue = append(queue, dep)
			}
		}
		for _, dep := range reverse[dir] {
			if _, ok := closure[dep]; !ok {
				closure[dep] = struct{}{}
				queue = append(queue, dep)
			}
		}
	}
	return closure
}

// scopeProjects filters config.Projects to those affected by the PR's changed
// files plus their dependency closure.
func scopeProjects(config *AtlantisConfig) (*AtlantisConfig, error) {
	ref := resolveBaseRef()
	entries, err := gitDiffNameStatus(gitRoot, ref)
	if err != nil {
		return nil, fmt.Errorf("--only-changed-files: cannot diff %q...HEAD: %w", ref, err)
	}

	affected := map[string]struct{}{}
	changedFiles := []string{}
	for _, entry := range entries {
		switch {
		case strings.HasPrefix(entry.status, "R"):
			if old := findUnitDirForFile(gitRoot, entry.oldPath); old != "" {
				affected[old] = struct{}{}
			}
			if newDir := findUnitDirForFile(gitRoot, entry.newPath); newDir != "" {
				affected[newDir] = struct{}{}
			}
		case entry.status == "D":
			if dir := findUnitDirForFile(gitRoot, entry.newPath); dir != "" {
				affected[dir] = struct{}{}
			}
		default:
			changedFiles = append(changedFiles, entry.newPath)
			if dir := findUnitDirForFile(gitRoot, entry.newPath); dir != "" {
				affected[dir] = struct{}{}
			}
		}
	}

	// Consumer discovery: units whose when_modified matches a changed file.
	for i := range config.Projects {
		for _, file := range changedFiles {
			if matchesWhenModified(config.Projects[i].Dir, config.Projects[i].Autoplan.WhenModified, file) {
				affected[config.Projects[i].Dir] = struct{}{}
				break
			}
		}
	}

	closure := expandClosure(config.Projects, affected)

	filtered := AtlantisConfig{
		Version:       config.Version,
		AutoMerge:     config.AutoMerge,
		ParallelPlan:  config.ParallelPlan,
		ParallelApply: config.ParallelApply,
		Workflows:     config.Workflows,
	}
	includedNames := map[string]struct{}{}
	for i := range config.Projects {
		if _, ok := closure[config.Projects[i].Dir]; ok {
			filtered.Projects = append(filtered.Projects, config.Projects[i])
			if config.Projects[i].Name != "" {
				includedNames[config.Projects[i].Name] = struct{}{}
			}
		}
	}

	// Prune depends_on edges pointing to projects outside the scoped set.
	for i := range filtered.Projects {
		pruned := make([]string, 0, len(filtered.Projects[i].DependsOn))
		for _, name := range filtered.Projects[i].DependsOn {
			if _, ok := includedNames[name]; ok {
				pruned = append(pruned, name)
			}
		}
		filtered.Projects[i].DependsOn = pruned
	}

	log.Infof(
		"Scoped config to %d of %d projects (base ref %q, %d changed files)",
		len(filtered.Projects), len(config.Projects), ref, len(changedFiles),
	)
	return &filtered, nil
}
