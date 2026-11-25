package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

// ---------------------------------------------------------------------------
// Lightweight Terragrunt parsing (regex-based, no full HCL evaluation).
// ---------------------------------------------------------------------------

var (
	dependencyBlockRe     = regexp.MustCompile(`dependency\s+"[^"]+"\s*\{[^}]*\}`)
	configPathRe          = regexp.MustCompile(`config_path\s*=\s*"([^"]+)"`)
	moduleSourceRe        = regexp.MustCompile(`source\s*=\s*"\$\{get_repo_root\(\)\}/([^"]+)"`)
	fileExprRe            = regexp.MustCompile(`file\("([^"]+)"\)`)
	findInParentRe        = regexp.MustCompile(`find_in_parent_folders\("([^"]+)"\)`)
	whenModifiedExcludeRe = regexp.MustCompile(`atlantis_when_modified_exclude\s*=\s*\[([^\]]*)\]`)
	stringLiteralRe       = regexp.MustCompile(`"([^"]+)"`)
	localRefRe            = regexp.MustCompile(`local\.[a-zA-Z_][a-zA-Z0-9_]*`)
)

// skipDirNames are directory names excluded from the Terragrunt unit walk.
var skipDirNames = map[string]bool{
	".git":              true,
	".terragrunt-cache": true,
	".terraform":        true,
	"node_modules":      true,
}

// stripHclComments removes HCL comments — `#` and `//` line comments and
// `/* ... */` block comments — outside double-quoted strings. It is used only
// for lightweight discovery, so it approximates HCL lexing without a full parse.
func stripHclComments(content string) string {
	var b strings.Builder
	b.Grow(len(content))

	inString := false
	inBlock := false

	for i := 0; i < len(content); i++ {
		c := content[i]

		if inBlock {
			if c == '*' && i+1 < len(content) && content[i+1] == '/' {
				inBlock = false
				i++
			}
			continue
		}

		if inString {
			b.WriteByte(c)
			if c == '"' && (i == 0 || content[i-1] != '\\') {
				inString = false
			}
			continue
		}

		switch {
		case c == '"':
			inString = true
			b.WriteByte(c)
		case c == '#':
			// Line comment: skip to the end of the line, preserving the newline.
			for i < len(content) && content[i] != '\n' {
				i++
			}
			if i < len(content) {
				b.WriteByte('\n')
			}
		case c == '/' && i+1 < len(content) && content[i+1] == '/':
			for i < len(content) && content[i] != '\n' {
				i++
			}
			if i < len(content) {
				b.WriteByte('\n')
			}
		case c == '/' && i+1 < len(content) && content[i+1] == '*':
			inBlock = true
			i++
		default:
			b.WriteByte(c)
		}
	}

	return b.String()
}

// readUnitContent returns the terragrunt.hcl content of a unit dir.
func readUnitContent(repoRoot, unitDir string) (string, bool) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", false
	}
	path := filepath.Join(filepath.Clean(root), filepath.FromSlash(unitDir), terragruntUnitFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// repoRel returns a posix repo-relative path from an absolute path ("" if outside).
func repoRel(repoRoot, abs string) string {
	rel, err := filepath.Rel(filepath.Clean(repoRoot), filepath.Clean(abs))
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// iterTerragruntUnitDirs lists all repo-relative Terragrunt unit dirs.
func iterTerragruntUnitDirs(repoRoot string) []string {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil
	}
	root = filepath.Clean(root)
	dirs := []string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == terragruntUnitFile {
			if rel := repoRel(root, filepath.Dir(path)); rel != "" {
				dirs = append(dirs, rel)
			}
		}
		return nil
	})
	return dirs
}

// dependencyTargetDirs returns the repo-relative unit dirs referenced by
// `dependency { config_path }` blocks in a unit (in-repo only).
func dependencyTargetDirs(repoRoot, unitDir string) []string {
	content, ok := readUnitContent(repoRoot, unitDir)
	if !ok {
		return nil
	}
	stripped := stripHclComments(content)
	targets := map[string]struct{}{}
	for _, block := range dependencyBlockRe.FindAllString(stripped, -1) {
		m := configPathRe.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		relCfg := m[1]
		root, _ := filepath.Abs(repoRoot)
		resolved := filepath.Clean(filepath.Join(filepath.Clean(root), filepath.FromSlash(unitDir), filepath.FromSlash(relCfg)))
		rel := repoRel(root, resolved)
		if rel == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(resolved, terragruntUnitFile)); err == nil && !info.IsDir() {
			targets[rel] = struct{}{}
		}
	}
	return sortedKeys(targets)
}

// collectDependencyClosure returns the transitive forward dependency closure.
func collectDependencyClosure(repoRoot string, entryDirs []string) map[string]struct{} {
	seen := map[string]struct{}{}
	queue := append([]string{}, entryDirs...)
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		for _, dep := range dependencyTargetDirs(repoRoot, dir) {
			if _, ok := seen[dep]; !ok {
				queue = append(queue, dep)
			}
		}
	}
	return seen
}

// collectReverseDependents returns units that declare a dependency toward any
// of the target dirs (scanning all units).
func collectReverseDependents(repoRoot string, targetDirs map[string]struct{}) map[string]struct{} {
	consumers := map[string]struct{}{}
	for _, unitDir := range iterTerragruntUnitDirs(repoRoot) {
		for _, dep := range dependencyTargetDirs(repoRoot, unitDir) {
			if _, ok := targetDirs[dep]; ok {
				consumers[unitDir] = struct{}{}
				break
			}
		}
	}
	return consumers
}

// parseWhenModifiedExclude extracts the atlantis_when_modified_exclude globs
// from HCL content (lightweight regex; used only for consumer discovery).
func parseWhenModifiedExclude(content string) []string {
	m := whenModifiedExcludeRe.FindStringSubmatch(stripHclComments(content))
	if m == nil {
		return nil
	}
	globs := []string{}
	for _, s := range stringLiteralRe.FindAllStringSubmatch(m[1], -1) {
		globs = append(globs, s[1])
	}
	return globs
}

// scopedExtraDep is an entry of extra_atlantis_dependencies.
type scopedExtraDep struct {
	value string
	kind  string // "literal", "negation" or "dynamic"
}

// parseExtraAtlantisDependencies extracts the extra_atlantis_dependencies
// entries (literal strings, negations and dynamic references) from HCL content.
// Literals are used for discovery; dynamic references (local.*, concat, …) are
// reported but not resolved here.
func parseExtraAtlantisDependencies(content string) []scopedExtraDep {
	content = stripHclComments(content)
	idx := strings.Index(content, "extra_atlantis_dependencies")
	if idx == -1 {
		return nil
	}
	window := content[idx:]
	if len(window) > 600 {
		window = window[:600]
	}
	if end := strings.Index(window, "\n}"); end != -1 {
		window = window[:end]
	}

	deps := []scopedExtraDep{}
	for _, m := range stringLiteralRe.FindAllStringSubmatch(window, -1) {
		val := m[1]
		kind := "literal"
		if strings.HasPrefix(val, "!") {
			kind = "negation"
		}
		deps = append(deps, scopedExtraDep{value: val, kind: kind})
	}
	for _, m := range localRefRe.FindAllStringSubmatch(window, -1) {
		deps = append(deps, scopedExtraDep{value: m[0], kind: "dynamic"})
	}
	return deps
}

// findParentRootHcl returns the content of the nearest parent root.hcl above
// the unit dir (the file find_in_parent_folders("root.hcl") resolves).
func findParentRootHcl(repoRoot, unitDir string) (string, bool) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", false
	}
	root = filepath.Clean(root)
	dir := filepath.Join(root, filepath.FromSlash(unitDir))
	for {
		candidate := filepath.Join(dir, "root.hcl")
		if b, err := os.ReadFile(candidate); err == nil {
			return string(b), true
		}
		if dir == root {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// inferWhenModified returns a lightweight approximation of a unit's
// autoplan.when_modified, used only to discover downstream consumers.
func inferWhenModified(repoRoot, unitDir string) []string {
	content, ok := readUnitContent(repoRoot, unitDir)
	if !ok {
		return []string{"*.hcl", "*.tf*"}
	}
	stripped := stripHclComments(content)
	root, _ := filepath.Abs(repoRoot)
	root = filepath.Clean(root)
	unitAbs := filepath.Join(root, filepath.FromSlash(unitDir))

	patterns := []string{"*.hcl", "*.tf*"}

	// file("...") expressions resolved relative to the unit dir.
	for _, m := range fileExprRe.FindAllStringSubmatch(stripped, -1) {
		expr := m[1]
		token := strings.TrimSpace(expr)
		prefix := "${get_terragrunt_dir()}/"
		if strings.HasPrefix(token, prefix) {
			token = token[len(prefix):]
		}
		if token == "" || strings.Contains(token, "..") {
			continue
		}
		candidate := filepath.Join(unitAbs, filepath.FromSlash(token))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			patterns = append(patterns, filepath.ToSlash(token))
		}
	}

	// find_in_parent_folders("...") resolved via parents.
	for _, m := range findInParentRe.FindAllStringSubmatch(stripped, -1) {
		filename := m[1]
		if filename == "" || strings.Contains(filename, "/") {
			continue
		}
		current := unitAbs
		for {
			if repoRel(root, current) == "" && current != root {
				break
			}
			candidate := filepath.Join(current, filename)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				rel, rerr := filepath.Rel(unitAbs, candidate)
				if rerr == nil {
					patterns = append(patterns, filepath.ToSlash(rel))
				}
				break
			}
			if current == root {
				break
			}
			current = filepath.Dir(current)
		}
	}

	// terraform source "${get_repo_root()}/..." -> module dir glob.
	for _, m := range moduleSourceRe.FindAllStringSubmatch(stripped, -1) {
		moduleRel := strings.Trim(m[1], "/")
		moduleAbs := filepath.Join(root, filepath.FromSlash(moduleRel))
		if info, err := os.Stat(moduleAbs); err == nil && info.IsDir() {
			rel := filepath.ToSlash(mustRel(unitAbs, moduleAbs))
			patterns = append(patterns, rel+"/*.tf*")
		}
	}

	// dependency { config_path } -> upstream terragrunt.hcl + its module globs.
	for _, depDir := range dependencyTargetDirs(repoRoot, unitDir) {
		depAbs := filepath.Join(root, filepath.FromSlash(depDir))
		relDir := filepath.ToSlash(mustRel(unitAbs, depAbs))
		if relDir != "." && relDir != "" {
			patterns = append(patterns, relDir+"/terragrunt.hcl")
		}
		if depContent, ok := readUnitContent(repoRoot, depDir); ok {
			for _, m := range moduleSourceRe.FindAllStringSubmatch(stripHclComments(depContent), -1) {
				depModuleRel := strings.Trim(m[1], "/")
				depModuleAbs := filepath.Join(root, filepath.FromSlash(depModuleRel))
				if info, err := os.Stat(depModuleAbs); err == nil && info.IsDir() {
					rel := filepath.ToSlash(mustRel(unitAbs, depModuleAbs))
					patterns = append(patterns, rel+"/*.tf*")
				}
			}
		}
	}

	// extra_atlantis_dependencies: add literal entries to when_modified so a
	// change to those files triggers the unit during consumer discovery.
	extraDeps := parseExtraAtlantisDependencies(content)
	rootContent, hasRoot := findParentRootHcl(repoRoot, unitDir)
	if hasRoot {
		extraDeps = append(extraDeps, parseExtraAtlantisDependencies(rootContent)...)
	}
	for _, dep := range extraDeps {
		if dep.kind == "literal" {
			patterns = append(patterns, dep.value)
		}
	}

	// Respect atlantis_when_modified_exclude (unit + parent root.hcl), the same
	// way the full generator does, so shared files like root.hcl don't fan out
	// to every unit during consumer discovery.
	exclude := parseWhenModifiedExclude(content)
	if hasRoot {
		exclude = append(exclude, parseWhenModifiedExclude(rootContent)...)
	}
	patterns = filterWhenModifiedExcludes(unitDir, patterns, uniqueStrings(exclude))

	return uniqueStrings(patterns)
}

// discoverUnitsByWhenModified returns units whose inferred when_modified
// matches at least one changed file (downstream consumers).
func discoverUnitsByWhenModified(repoRoot string, changedFiles []string) map[string]struct{} {
	matched := map[string]struct{}{}
	if len(changedFiles) == 0 {
		return matched
	}
	changed := make([]string, 0, len(changedFiles))
	for _, f := range changedFiles {
		f = filepath.ToSlash(strings.TrimSpace(f))
		f = strings.TrimPrefix(f, "/")
		if f != "" {
			changed = append(changed, f)
		}
	}
	if len(changed) == 0 {
		return matched
	}
	for _, unitDir := range iterTerragruntUnitDirs(repoRoot) {
		patterns := inferWhenModified(repoRoot, unitDir)
		for _, repoFile := range changed {
			if matchesWhenModified(unitDir, patterns, repoFile) {
				matched[unitDir] = struct{}{}
				break
			}
		}
	}
	return matched
}

// scopedExclude is a when_modified exclusion applied during discovery.
type scopedExclude struct {
	glob   string
	source string // "local" (atlantis_when_modified_exclude) or "flag" (--when-modified-exclude-paths)
}

// scopedDiscovery holds the result of the discover + expand phases.
type scopedDiscovery struct {
	dirs         map[string]struct{}
	changedFiles []string
	directDirs   []string
	consumerDirs []string
	baseRef      string
	dependsOn    map[string][]string // dir -> in-scope dirs it depends on
	excludes     []scopedExclude
	extraDeps    []scopedExtraDep
}

// discoverAffectedUnitDirs computes the units to generate for the PR.
func discoverAffectedUnitDirs(repoRoot string) (*scopedDiscovery, error) {
	ref := resolveBaseRef()
	entries, err := gitDiffNameStatus(repoRoot, ref)
	if err != nil {
		return nil, fmt.Errorf("--only-changed-files: cannot diff %q...HEAD: %w", ref, err)
	}

	affected := map[string]struct{}{}
	changedFiles := []string{}
	for _, entry := range entries {
		switch {
		case strings.HasPrefix(entry.status, "R"):
			if d := findUnitDirForFile(repoRoot, entry.oldPath); d != "" {
				affected[d] = struct{}{}
			}
			if d := findUnitDirForFile(repoRoot, entry.newPath); d != "" {
				affected[d] = struct{}{}
			}
		case entry.status == "D":
			if d := findUnitDirForFile(repoRoot, entry.newPath); d != "" {
				affected[d] = struct{}{}
			}
		default:
			changedFiles = append(changedFiles, entry.newPath)
			if d := findUnitDirForFile(repoRoot, entry.newPath); d != "" {
				affected[d] = struct{}{}
			}
		}
	}

	directDirs := sortedKeys(affected)
	for d := range discoverUnitsByWhenModified(repoRoot, changedFiles) {
		affected[d] = struct{}{}
	}
	consumerDirs := sortedKeys(difference(affected, directDirs))

	closure := collectDependencyClosure(repoRoot, sortedKeys(affected))
	for d := range collectReverseDependents(repoRoot, closure) {
		closure[d] = struct{}{}
	}

	dependsOn := map[string][]string{}
	for dir := range closure {
		for _, dep := range dependencyTargetDirs(repoRoot, dir) {
			if _, ok := closure[dep]; ok {
				dependsOn[dir] = append(dependsOn[dir], dep)
			}
		}
		sort.Strings(dependsOn[dir])
	}

	excludeSet := map[string]string{}
	for dir := range closure {
		if rootContent, ok := findParentRootHcl(repoRoot, dir); ok {
			for _, g := range parseWhenModifiedExclude(rootContent) {
				if _, seen := excludeSet[g]; !seen {
					excludeSet[g] = "local"
				}
			}
		}
	}
	for _, g := range whenModifiedExcludePaths {
		if _, seen := excludeSet[g]; !seen {
			excludeSet[g] = "flag"
		}
	}
	excludes := make([]scopedExclude, 0, len(excludeSet))
	for g, src := range excludeSet {
		excludes = append(excludes, scopedExclude{glob: g, source: src})
	}
	sort.Slice(excludes, func(i, j int) bool { return excludes[i].glob < excludes[j].glob })

	extraDepSet := map[string]string{}
	for dir := range closure {
		if rootContent, ok := findParentRootHcl(repoRoot, dir); ok {
			for _, dep := range parseExtraAtlantisDependencies(rootContent) {
				if _, seen := extraDepSet[dep.value]; !seen {
					extraDepSet[dep.value] = dep.kind
				}
			}
		}
		if unitContent, ok := readUnitContent(repoRoot, dir); ok {
			for _, dep := range parseExtraAtlantisDependencies(unitContent) {
				if _, seen := extraDepSet[dep.value]; !seen {
					extraDepSet[dep.value] = dep.kind
				}
			}
		}
	}
	extraDeps := make([]scopedExtraDep, 0, len(extraDepSet))
	for val, kind := range extraDepSet {
		extraDeps = append(extraDeps, scopedExtraDep{value: val, kind: kind})
	}
	sort.Slice(extraDeps, func(i, j int) bool { return extraDeps[i].value < extraDeps[j].value })

	return &scopedDiscovery{
		dirs:         closure,
		changedFiles: changedFiles,
		directDirs:   directDirs,
		consumerDirs: consumerDirs,
		baseRef:      ref,
		dependsOn:    dependsOn,
		excludes:     excludes,
		extraDeps:    extraDeps,
	}, nil
}

// difference returns the keys of a that are not in b.
func difference(a map[string]struct{}, b []string) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range a {
		out[k] = struct{}{}
	}
	for _, k := range b {
		delete(out, k)
	}
	return out
}

// isAffectedUnitDir reports whether an absolute terragrunt.hcl path belongs to
// the scoped set of unit dirs.
func (d *scopedDiscovery) contains(repoRoot, absTerragruntPath string) bool {
	rel := repoRel(repoRoot, filepath.Dir(absTerragruntPath))
	if rel == "" {
		return false
	}
	_, ok := d.dirs[rel]
	return ok
}

// sortedKeys returns the sorted keys of a set.
func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mustRel returns the relative path from base to target, or target on error.
func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}
