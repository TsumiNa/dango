package cmd

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersionUsesExplicitBuildVersion(t *testing.T) {
	t.Parallel()

	got := resolveVersion("v1.2.3", nil, "devel.abcdef123456")
	if got != "v1.2.3" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "v1.2.3")
	}
}

func TestVersionFromBuildInfoUsesModuleVersion(t *testing.T) {
	t.Parallel()

	buildInfo := &debug.BuildInfo{}
	buildInfo.Main.Version = "v1.2.3"

	got := versionFromBuildInfo(buildInfo)
	if got != "v1.2.3" {
		t.Fatalf("versionFromBuildInfo() = %q, want %q", got, "v1.2.3")
	}
}

func TestVersionFromBuildInfoUsesVCSMetadata(t *testing.T) {
	t.Parallel()

	buildInfo := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "1234567890abcdef"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	got := versionFromBuildInfo(buildInfo)
	if got != "devel.1234567890ab.dirty" {
		t.Fatalf("versionFromBuildInfo() = %q, want %q", got, "devel.1234567890ab.dirty")
	}
}

func TestResolveVersionFallsBackToGit(t *testing.T) {
	t.Parallel()

	got := resolveVersion("", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "devel.abcdef123456")
	if got != "devel.abcdef123456" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "devel.abcdef123456")
	}
}

func TestResolveVersionReturnsUnknownWithoutSources(t *testing.T) {
	t.Parallel()

	got := resolveVersion("", nil, "")
	if got != unknownVersion {
		t.Fatalf("resolveVersion() = %q, want %q", got, unknownVersion)
	}
}
