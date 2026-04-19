package cmd

import (
	"fmt"
	"os/exec"
	"runtime/debug"
	"strings"
)

const unknownVersion = "devel.unknown"

// Version is the CLI version reported by the --version flag.
// It can be overridden at build time with:
// go build -ldflags "-X github.com/tsumina/dango/cmd.Version=v0.1.0"
var Version string

func getVersion() string {
	buildInfo, _ := debug.ReadBuildInfo()

	return resolveVersion(Version, buildInfo, versionFromGit())
}

func resolveVersion(buildVersion string, buildInfo *debug.BuildInfo, gitVersion string) string {
	if buildVersion != "" {
		return buildVersion
	}

	if version := versionFromBuildInfo(buildInfo); version != "" {
		return version
	}

	if gitVersion != "" {
		return gitVersion
	}

	return unknownVersion
}

func versionFromBuildInfo(buildInfo *debug.BuildInfo) string {
	if buildInfo == nil {
		return ""
	}

	if v := buildInfo.Main.Version; v != "" && v != "(devel)" {
		return v
	}

	var rev string
	var dirty bool

	for _, setting := range buildInfo.Settings {
		switch setting.Key {
		case "vcs.revision":
			rev = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}

	return formatVCSVersion(rev, dirty)
}

func versionFromGit() string {
	rev, err := gitOutput("rev-parse", "--short=12", "HEAD")
	if err != nil || rev == "" {
		return ""
	}

	status, err := gitOutput("status", "--porcelain")
	if err != nil {
		return formatVCSVersion(rev, false)
	}

	return formatVCSVersion(rev, status != "")
}

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

func formatVCSVersion(rev string, dirty bool) string {
	if rev == "" {
		return ""
	}

	if len(rev) > 12 {
		rev = rev[:12]
	}

	if dirty {
		return fmt.Sprintf("devel.%s.dirty", rev)
	}

	return fmt.Sprintf("devel.%s", rev)
}
