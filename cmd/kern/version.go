package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"

	"github.com/dshills/kern/compiler/diag"
)

// version is the release version, set only by the release build with
// -ldflags "-X main.version=<v>" (scripts/release.sh).
var version string

// buildIdentity is what `kern version` reports about the binary
// (specs/release-v0.1/SPEC.md R1).
type buildIdentity struct {
	Version string
	Commit  string
}

// resolveIdentity is R1's resolution, a pure function of the linked version
// and the build information: the linked version, else the main module's
// version unless it is empty or "(devel)" (as `go install ...@v0.1.0`
// stamps it), else "devel"; the commit is vcs.revision cut to 12
// characters, with "-dirty" when vcs.modified is true, else empty.
func resolveIdentity(linked string, bi *debug.BuildInfo, ok bool) buildIdentity {
	id := buildIdentity{Version: "devel"}
	if !ok || bi == nil {
		bi = &debug.BuildInfo{}
	}
	switch {
	case linked != "":
		id.Version = linked
	case bi.Main.Version != "" && bi.Main.Version != "(devel)":
		id.Version = bi.Main.Version
	}
	modified := false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			id.Commit = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if len(id.Commit) > 12 {
		id.Commit = id.Commit[:12]
	}
	if id.Commit != "" && modified {
		id.Commit += "-dirty"
	}
	return id
}

// currentIdentity resolves this binary's identity. Only the two version
// entry points call it, at most once per process.
func currentIdentity() buildIdentity {
	bi, ok := debug.ReadBuildInfo()
	return resolveIdentity(version, bi, ok)
}

// versionResult is the `result` of a `kern version` response.
type versionResult struct {
	Version        string   `json:"version"`
	Commit         string   `json:"commit"`
	Go             string   `json:"go"`
	OS             string   `json:"os"`
	Arch           string   `json:"arch"`
	SchemaVersions []string `json:"schema_versions"`
}

// versionCommand is `kern version [--human]` (spec R2): one JSON response,
// or with --human one line. Any other argument is CLI_INVALID_FLAGS.
func versionCommand(args []string, id buildIdentity, stdout io.Writer) int {
	human := false
	for _, a := range args {
		if a != "--human" {
			if len(a) > 1 && a[0] == '-' {
				return fail(stdout, "CLI_INVALID_FLAGS", "unknown flag %s", a)
			}
			return fail(stdout, "CLI_INVALID_FLAGS", "version takes no files (got %s)", a)
		}
		human = true
	}
	if human {
		commit := ""
		if id.Commit != "" {
			commit = id.Commit + ", "
		}
		_, _ = fmt.Fprintf(stdout, "kern %s (%s%s, %s/%s)\n", id.Version, commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	}
	res := versionResult{
		Version:        id.Version,
		Commit:         id.Commit,
		Go:             runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		SchemaVersions: append([]string(nil), diag.Accepted...),
	}
	_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: true, Result: res, Diagnostics: []diag.Diagnostic{}})
	return 0
}

// dashVersion is `kern --version`: exactly `kern version --human`, and an
// error when anything follows it.
func dashVersion(rest []string, stdout io.Writer) int {
	if len(rest) > 0 {
		return fail(stdout, "CLI_INVALID_FLAGS", "--version takes no further arguments (got %s)", rest[0])
	}
	return versionCommand([]string{"--human"}, currentIdentity(), stdout)
}
