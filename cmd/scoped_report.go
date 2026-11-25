package cmd

import (
	"os"
	"regexp"
	"strings"
	"time"
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

// buildScopedReport renders the human-readable PR-scoped generation summary
// followed by the generated atlantis.yaml (syntax-highlighted).
func buildScopedReport(d *scopedDiscovery, config *AtlantisConfig, elapsed time.Duration) string {
	var b strings.Builder

	b.WriteString(color("Scoped generation: ", ansiBold))
	b.WriteString(color("PR-scoped atlantis.yaml (discover → expand → render)", ansiCyan))
	b.WriteString("\n\n")

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
		for _, dir := range d.consumerDirs {
			b.WriteString("    - " + color(dir, ansiCyan) + "\n")
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
	for _, dir := range sortedKeys(d.dirs) {
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
	b.WriteString(color("── generated atlantis.yaml ("+itoa(len(config.Projects))+" projects) ──", ansiBold))
	b.WriteString("\n\n")
	b.WriteString(highlightYaml(renderReportYaml(config)))

	// ── Elapsed ──
	b.WriteString("\n")
	b.WriteString(color("⏱ Elapsed: "+elapsed.Round(time.Millisecond).String(), ansiBold))
	b.WriteString("\n")

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
