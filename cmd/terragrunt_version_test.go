package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSemver(t *testing.T) {
	v, ok := parseSemver("v0.77.7")
	assert.True(t, ok)
	assert.Equal(t, semver{major: 0, minor: 77, patch: 7}, v)
}

func TestParseSemverInvalid(t *testing.T) {
	_, ok := parseSemver("not a version")
	assert.False(t, ok)
}

func TestParseVersionString(t *testing.T) {
	got, ok := parseVersionString("terragrunt version v0.77.7\n")
	assert.True(t, ok)
	assert.Equal(t, "0.77.7", got)
}

func TestSemverFamily(t *testing.T) {
	tests := []struct {
		version semver
		want    string
	}{
		{semver{major: 0, minor: 77, patch: 7}, "0.7"},
		{semver{major: 0, minor: 78, patch: 1}, "0.7"},
		{semver{major: 0, minor: 99, patch: 0}, "0.9"},
		{semver{major: 1, minor: 2, patch: 3}, "1.0"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.version.family(), "%v", tt.version)
	}
}

func TestTerragruntVersionInfoMismatch(t *testing.T) {
	tests := []struct {
		name string
		info terragruntVersionInfo
		want bool
	}{
		{
			name: "same family (0.7) no mismatch",
			info: terragruntVersionInfo{embedded: "0.72.5", embeddedOK: true, system: "0.78.1", systemOK: true},
			want: false,
		},
		{
			name: "different family (0.7 vs 0.9) mismatch",
			info: terragruntVersionInfo{embedded: "0.72.5", embeddedOK: true, system: "0.99.0", systemOK: true},
			want: true,
		},
		{
			name: "unknown system never mismatches",
			info: terragruntVersionInfo{embedded: "0.72.5", embeddedOK: true, system: "", systemOK: false},
			want: false,
		},
		{
			name: "unknown embedded never mismatches",
			info: terragruntVersionInfo{embedded: "", embeddedOK: false, system: "0.99.0", systemOK: true},
			want: false,
		},
		{
			name: "same exact version no mismatch",
			info: terragruntVersionInfo{embedded: "0.77.7", embeddedOK: true, system: "0.77.7", systemOK: true},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.info.mismatch())
		})
	}
}
