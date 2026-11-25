package cmd

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

// terragruntVersionInfo holds the two Terragrunt versions that matter at
// generation time:
//   - embedded: the library version this binary was compiled against (used to
//     parse the terragrunt.hcl files and generate atlantis.yaml).
//   - system: the `terragrunt` binary on PATH (the one Atlantis runs at plan
//     time). It may be empty/unknown when the binary is not installed.
type terragruntVersionInfo struct {
	embedded   string
	embeddedOK bool
	system     string
	systemOK   bool
}

// embeddedTerragruntVersion returns the Terragrunt library version compiled
// into this binary, read from the Go build info (no hardcoded value).
func embeddedTerragruntVersion() (string, bool) {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, dep := range buildInfo.Deps {
		if dep.Path != "github.com/gruntwork-io/terragrunt" {
			continue
		}
		version := strings.TrimPrefix(dep.Version, "v")
		if version == "" || version == "(devel)" {
			return "", false
		}
		return version, true
	}
	return "", false
}

// systemTerragruntVersion returns the version of the `terragrunt` binary on
// PATH (the one Atlantis executes at plan time).
func systemTerragruntVersion() (string, bool) {
	path, err := exec.LookPath("terragrunt")
	if err != nil {
		return "", false
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", false
	}
	return parseVersionString(string(out))
}

// detectTerragruntVersions collects both Terragrunt versions.
func detectTerragruntVersions() terragruntVersionInfo {
	embedded, embeddedOK := embeddedTerragruntVersion()
	system, systemOK := systemTerragruntVersion()
	return terragruntVersionInfo{
		embedded:   embedded,
		embeddedOK: embeddedOK,
		system:     system,
		systemOK:   systemOK,
	}
}

// logTerragruntVersions logs both Terragrunt versions, warning when the
// runtime binary belongs to a different "0.X" family than the one TAC was
// built against.
func logTerragruntVersions(v terragruntVersionInfo) {
	embedded := v.embedded
	if !v.embeddedOK {
		embedded = "unknown"
	}
	system := v.system
	if !v.systemOK {
		system = "not found"
	}
	if v.mismatch() {
		log.Warnf(
			"Terragrunt version mismatch: TAC built against %s but runtime is %s (different 0.X family); consider aligning them",
			embedded, system,
		)
		return
	}
	log.Infof("Terragrunt versions: build=%s runtime=%s", embedded, system)
}

// mismatch reports whether both versions are known and belong to different
// "0.X" families (see semver.family). Unknown versions never count as a
// mismatch, since they cannot be compared.
func (i terragruntVersionInfo) mismatch() bool {
	if !i.embeddedOK || !i.systemOK {
		return false
	}
	embedded, okEmbedded := parseSemver(i.embedded)
	system, okSystem := parseSemver(i.system)
	if !okEmbedded || !okSystem {
		return false
	}
	return embedded.family() != system.family()
}

// semver is a minimal semantic-version representation.
type semver struct {
	major int
	minor int
	patch int
}

var versionRe = regexp.MustCompile(`v?([0-9]+)\.([0-9]+)\.([0-9]+)`)

// parseVersionString extracts the first "x.y.z" (optionally prefixed with "v")
// from a string such as `terragrunt version v0.77.7`.
func parseVersionString(s string) (string, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1] + "." + m[2] + "." + m[3], true
}

// parseSemver parses a version string into its numeric parts.
func parseSemver(s string) (semver, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return semver{}, false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return semver{major: major, minor: minor, patch: patch}, true
}

// family groups a version into its "0.X" series: the major plus the tens digit
// of the minor. 0.77 and 0.78 share family "0.7"; 0.99 is family "0.9".
func (v semver) family() string {
	return fmt.Sprintf("%d.%d", v.major, v.minor/10)
}
