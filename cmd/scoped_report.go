package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// ANSI color codes for the scoped report (only emitted on a TTY).
const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiDim     = "\033[2m"
	ansiCyan    = "\033[36m"
	ansiGreen   = "\033[32m"
	ansiMagenta = "\033[35m"
	ansiYellow  = "\033[33m"
	ansiOrange  = "\033[38;5;208m"
	ansiGray    = "\033[90m"
)

// useColor reports whether the scoped report should emit ANSI colors.
func useColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	info, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// color wraps text in an ANSI color when colors are enabled.
func color(text, code string) string {
	if !useColor() {
		return text
	}
	return code + text + ansiReset
}

// displayWidth returns the number of terminal columns a string occupies
// (each rune is one column for the characters used in the report).
func displayWidth(s string) int {
	return utf8.RuneCountInString(s)
}

// padRight pads s with spaces until it occupies the given display width.
func padRight(s string, width int) string {
	if w := displayWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// dividerLine returns a line of box-drawing dashes of the given width.
func dividerLine(width int) string {
	return strings.Repeat("─", width)
}

// dividerLabel centers a label between box-drawing dashes to the given width.
func dividerLabel(label string, width int) string {
	label = " " + label + " "
	if displayWidth(label) >= width {
		return strings.Repeat("─", width)
	}
	left := (width - displayWidth(label)) / 2
	right := width - displayWidth(label) - left
	return strings.Repeat("─", left) + label + strings.Repeat("─", right)
}

// maxRenderedProjects is the maximum number of projects whose YAML is dumped
// into the report. Above this the full YAML is omitted and a download command
// is shown instead.
const maxRenderedProjects = 50

// maxListedDirs is the maximum number of directory entries rendered in a
// single list (consumer units / dependency graph) before they are collapsed
// into prefix groups.
const maxListedDirs = 50

// serviceAccountNamespace returns the Kubernetes namespace this process runs
// in (empty when not running inside a pod).
func serviceAccountNamespace() string {
	b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// inPod reports whether this process runs inside a Kubernetes pod, in which
// case a kubectl exec download command makes sense.
func inPod() bool {
	return strings.TrimSpace(os.Getenv("HOSTNAME")) != "" && serviceAccountNamespace() != ""
}

// reportFileName is the file the full (untruncated) scoped report is written to.
const reportFileName = "tac_report.log"

// reportPath returns the path (relative to the working directory) where the
// full report is written, next to the generated atlantis.yaml.
func reportPath() string {
	if outputPath != "" {
		return filepath.Join(filepath.Dir(outputPath), reportFileName)
	}
	return reportFileName
}

// atlantisConfigPath returns the path of the generated atlantis.yaml (the
// --output path, defaulting to "atlantis.yaml").
func atlantisConfigPath() string {
	if outputPath != "" {
		return outputPath
	}
	return "atlantis.yaml"
}

// downloadCommand builds the kubectl command an operator can run (with VPN +
// kubeconfig access) to fetch a file from this pod.
func downloadCommand(filename string) string {
	pod := strings.TrimSpace(os.Getenv("HOSTNAME"))
	namespace := serviceAccountNamespace()
	path := filename
	if cwd, err := os.Getwd(); err == nil {
		path = filepath.Join(cwd, filename)
	}
	if pod == "" || namespace == "" {
		// Not running inside a pod; point at the local file instead.
		return "cat " + path
	}
	return "kubectl -n " + namespace + " exec " + pod + " -- cat " + path
}

// allNoDeps reports whether every directory at or under prefix has no
// depends_on entries (used to annotate collapsed dependency-graph groups).
func allNoDeps(prefix string, dirs map[string]struct{}, dependsOn map[string][]string) bool {
	for dir := range dirs {
		if dir == prefix || strings.HasPrefix(dir, prefix+"/") {
			if len(dependsOn[dir]) > 0 {
				return false
			}
		}
	}
	return true
}

// buildScopedReport renders the human-readable PR-scoped generation summary
// followed by the generated atlantis.yaml (syntax-highlighted). When full is
// true no list is collapsed and the full YAML is included (used for the
// tac_report.log file); otherwise long lists are grouped and the YAML omitted
// to keep the terminal log short.
func buildScopedReport(d *scopedDiscovery, config *AtlantisConfig, tgVersions terragruntVersionInfo, elapsed time.Duration, full bool) string {
	var b strings.Builder

	title := "Scoped generation: PR-scoped atlantis.yaml (discover → expand → render)"
	reportWidth := displayWidth(title)

	b.WriteString(color(dividerLine(reportWidth), ansiDim))
	b.WriteString("\n")
	b.WriteString(color("Scoped generation: ", ansiBold))
	b.WriteString(color("PR-scoped atlantis.yaml (discover → expand → render)", ansiCyan))
	b.WriteString("\n\n")

	// ── Terragrunt versions ──
	b.WriteString(color("Terragrunt versions", ansiCyan+ansiBold))
	b.WriteString("\n")
	embedded := tgVersions.embedded
	if !tgVersions.embeddedOK {
		embedded = "unknown"
	}
	system := tgVersions.system
	if !tgVersions.systemOK {
		system = "not found"
	}
	b.WriteString("  - TAC (build): " + color(embedded, ansiCyan) + "\n")
	b.WriteString("  - Runtime:     " + color(system, ansiCyan) + "\n")
	switch {
	case tgVersions.mismatch():
		b.WriteString("  " + color("⚠️ mismatch — runtime "+system+" is in a different 0.X family than TAC's "+embedded+"; consider aligning them", ansiYellow) + "\n")
	case tgVersions.embeddedOK && tgVersions.systemOK:
		b.WriteString("  " + color("✓ same 0.X family", ansiGreen) + "\n")
	}

	b.WriteString("\n")

	// ── Discover ──
	b.WriteString(color("▸ Discover — changes in the PR", ansiOrange+ansiBold))
	b.WriteString("\n")
	b.WriteString("  Changed files (" + itoa(len(d.changedFiles)) + "):\n")
	for _, f := range d.changedFiles {
		line := "    - " + color(f, ansiGray)
		if kind := changedFileKind(f); kind != "" {
			line += "  " + color("["+kind+"]", ansiDim)
		}
		b.WriteString(line + "\n")
	}

	if len(d.directDirs) == 0 {
		b.WriteString("  Direct units (0)\n    (none — no changed file lives under a unit dir)\n")
	} else {
		b.WriteString("  Direct units (" + itoa(len(d.directDirs)) + "):\n")
		for _, dir := range d.directDirs {
			b.WriteString("    - " + color(dir, ansiCyan) + "\n")
		}
	}

	if len(d.consumerDirs) == 0 {
		b.WriteString("  Consumer units via when_modified (0)\n")
	} else {
		b.WriteString("  Consumer units via when_modified (" + itoa(len(d.consumerDirs)) + "):\n")
		consumerLimit := maxListedDirs
		if full {
			consumerLimit = len(d.consumerDirs)
		}
		for _, e := range collapseDirs(d.consumerDirs, consumerLimit) {
			if e.grouped {
				b.WriteString("    - " + color(e.prefix+"/*", ansiCyan) + color("  ("+itoa(e.count)+" units)", ansiDim) + "\n")
			} else {
				b.WriteString("    - " + color(e.prefix, ansiCyan) + "\n")
			}
		}
	}

	if len(d.excludes) == 0 {
		b.WriteString("  atlantis_when_modified_exclude: (not declared)\n")
	} else {
		b.WriteString("  atlantis_when_modified_exclude (applied):\n")
		for _, ex := range d.excludes {
			b.WriteString("    - " + color(ex.glob, ansiGray) + "  " + color("["+ex.source+"]", ansiDim) + "\n")
		}
	}

	if len(d.extraDeps) == 0 {
		b.WriteString("  extra_atlantis_dependencies: (not declared)\n")
	} else {
		b.WriteString("  extra_atlantis_dependencies:\n")
		for _, dep := range d.extraDeps {
			label := "literal"
			switch dep.kind {
			case "negation":
				label = "literal · negation"
			case "dynamic":
				label = "dynamic · not resolved"
			}
			b.WriteString("    - " + color(dep.value, ansiGray) + "  " + color("["+label+"]", ansiDim) + "\n")
		}
	}

	// ── Expand ──
	closureCount := len(d.dirs) - len(d.directDirs) - len(d.consumerDirs)
	if closureCount < 0 {
		closureCount = 0
	}
	b.WriteString("\n")
	b.WriteString(color("▸ Expand — Terragrunt dependency graph", ansiOrange+ansiBold))
	b.WriteString("\n")
	b.WriteString(
		"  Units in scope: " + itoa(len(d.dirs)) +
			" (" + itoa(len(d.directDirs)) + " direct + " +
			itoa(len(d.consumerDirs)) + " consumers + " +
			itoa(closureCount) + " closure)\n",
	)

	b.WriteString("\n  Dependency graph (dir → depends_on):\n")
	graphLimit := maxListedDirs
	if full {
		graphLimit = len(d.dirs)
	}
	for _, e := range collapseDirs(sortedKeys(d.dirs), graphLimit) {
		if e.grouped {
			suffix := "  (" + itoa(e.count) + " units)"
			if allNoDeps(e.prefix, d.dirs, d.dependsOn) {
				suffix = "  (no deps, " + itoa(e.count) + " units)"
			}
			b.WriteString("    " + color(e.prefix+"/*", ansiCyan) + color(suffix, ansiDim) + "\n")
			continue
		}
		dir := e.prefix
		deps := d.dependsOn[dir]
		if len(deps) == 0 {
			b.WriteString("    " + color(dir, ansiCyan) + "  " + color("(no deps)", ansiDim) + "\n")
			continue
		}
		b.WriteString("    " + color(dir, ansiCyan) + "\n")
		for _, dep := range deps {
			b.WriteString("      └─ " + color(dep, ansiGray) + "\n")
		}
	}

	// ── Generated YAML ──
	b.WriteString("\n")
	b.WriteString(color(dividerLabel("generated atlantis.yaml ("+itoa(len(config.Projects))+" projects)", reportWidth), ansiBold))
	b.WriteString("\n\n")
	if !full && len(config.Projects) > maxRenderedProjects {
		b.WriteString(color("⚠️ "+itoa(len(config.Projects))+" projects generated — atlantis.yaml omitted from the log (limit: "+itoa(maxRenderedProjects)+").", ansiYellow))
		b.WriteString("\n")
		b.WriteString(color("   Download the full atlantis.yaml with the commands in the Summary below.", ansiDim))
		b.WriteString("\n")
	} else {
		b.WriteString(highlightYaml(renderReportYaml(config)))
	}

	// ── Summary ──
	b.WriteString("\n")
	b.WriteString(color(dividerLabel("Summary", reportWidth), ansiBold))
	b.WriteString("\n")
	b.WriteString("  " + color("📦 Projects generated: ", ansiBold) + color(itoa(len(config.Projects)), ansiCyan) + "\n")
	b.WriteString("  " + color("🎯 Units in scope: ", ansiBold) + color(itoa(len(d.directDirs))+" direct + "+itoa(len(d.consumerDirs))+" consumers + "+itoa(closureCount)+" closure", ansiCyan) + "\n")
	b.WriteString("  " + color("⏱ Elapsed: ", ansiBold) + color(elapsed.Round(time.Millisecond).String(), ansiMagenta) + "\n")
	b.WriteString("\n")
	if inPod() {
		b.WriteString("  Download (requires VPN + kubectl):\n")
		reportCmd := downloadCommand(reportPath())
		yamlCmd := downloadCommand(atlantisConfigPath())
		cmdWidth := displayWidth(reportCmd)
		if w := displayWidth(yamlCmd); w > cmdWidth {
			cmdWidth = w
		}
		b.WriteString("      " + color(padRight(reportCmd, cmdWidth), ansiCyan) + "  # full report (" + reportFileName + ")\n")
		b.WriteString("      " + color(padRight(yamlCmd, cmdWidth), ansiCyan) + "  # atlantis.yaml\n")
	} else {
		b.WriteString("  Files written to:\n")
		b.WriteString("      " + color(reportPath(), ansiCyan) + "  # full report (" + reportFileName + ")\n")
		b.WriteString("      " + color(atlantisConfigPath(), ansiCyan) + "  # atlantis.yaml\n")
	}

	return b.String()
}

// changedFileKind returns a short label for a changed file used in the report.
func changedFileKind(path string) string {
	if strings.HasPrefix(path, "_modules/") {
		return "module"
	}
	if strings.HasSuffix(path, "root.hcl") {
		return "shared"
	}
	return ""
}

// renderReportYaml builds the generated atlantis.yaml with a stable, readable
// field order for the report only (the file keeps the original marshaling).
func renderReportYaml(config *AtlantisConfig) string {
	var b strings.Builder
	b.WriteString("version: " + itoa(config.Version) + "\n")
	b.WriteString("automerge: " + boolToStr(config.AutoMerge) + "\n")
	b.WriteString("parallel_plan: " + boolToStr(config.ParallelPlan) + "\n")
	b.WriteString("parallel_apply: " + boolToStr(config.ParallelApply) + "\n")
	b.WriteString("projects:\n")
	for _, p := range config.Projects {
		b.WriteString("- name: " + p.Name + "\n")
		b.WriteString("  dir: " + p.Dir + "\n")
		if p.Workflow != "" {
			b.WriteString("  workflow: " + p.Workflow + "\n")
		}
		if p.Workspace != "" {
			b.WriteString("  workspace: " + p.Workspace + "\n")
		}
		b.WriteString("  autoplan:\n")
		b.WriteString("    enabled: " + boolToStr(p.Autoplan.Enabled) + "\n")
		b.WriteString("    when_modified:\n")
		for _, wm := range p.Autoplan.WhenModified {
			b.WriteString("    - '" + wm + "'\n")
		}
		if p.TerraformVersion != "" {
			b.WriteString("  terraform_version: " + p.TerraformVersion + "\n")
		}
		if p.ApplyRequirements != nil {
			b.WriteString("  apply_requirements:\n")
			for _, req := range *p.ApplyRequirements {
				b.WriteString("  - " + req + "\n")
			}
		}
		if len(p.DependsOn) > 0 {
			b.WriteString("  depends_on:\n")
			for _, dep := range p.DependsOn {
				b.WriteString("  - " + dep + "\n")
			}
		}
		if p.ExecutionOrderGroup != nil {
			b.WriteString("  execution_order_group: " + itoa(*p.ExecutionOrderGroup) + "\n")
		}
	}
	return b.String()
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// highlightYaml applies lightweight syntax highlighting to the generated YAML.
func highlightYaml(yaml string) string {
	if !useColor() {
		return yaml
	}
	lines := strings.Split(yaml, "\n")
	for i, line := range lines {
		lines[i] = highlightYamlLine(line)
	}
	return strings.Join(lines, "\n")
}

var (
	yamlKeyLineRe = regexp.MustCompile(`^(\s*)(- )?([a-zA-Z_][a-zA-Z0-9_]*)(:)(.*)$`)
	yamlStringRe  = regexp.MustCompile(`'[^']*'`)
	yamlBoolRe    = regexp.MustCompile(`\b(true|false)\b`)
	yamlNumberRe  = regexp.MustCompile(`\b\d+\b`)
)

func highlightYamlLine(line string) string {
	m := yamlKeyLineRe.FindStringSubmatch(line)
	if m == nil {
		return highlightYamlValue(line)
	}
	key := m[1] + m[2] + color(m[3], ansiCyan) + m[4]
	return key + highlightYamlValue(m[5])
}

func highlightYamlValue(value string) string {
	value = yamlStringRe.ReplaceAllStringFunc(value, func(s string) string {
		return color(s, ansiGreen)
	})
	value = yamlBoolRe.ReplaceAllStringFunc(value, func(s string) string {
		return color(s, ansiMagenta)
	})
	value = yamlNumberRe.ReplaceAllStringFunc(value, func(s string) string {
		return color(s, ansiYellow)
	})
	return value
}

// itoa converts an int to a string (avoids a strconv import for a few calls).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
