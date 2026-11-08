package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
)

func TestResolveIdentity(t *testing.T) {
	rev := "782404bdd5947a1b2c3d4e5f60718293a4b5c6d7"
	vcs := func(modified string) []debug.BuildSetting {
		s := []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: rev}}
		if modified != "" {
			s = append(s, debug.BuildSetting{Key: "vcs.modified", Value: modified})
		}
		return s
	}
	cases := []struct {
		name   string
		linked string
		bi     *debug.BuildInfo
		ok     bool
		want   buildIdentity
	}{
		{"linked wins", "v0.1.0", &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}, Settings: vcs("false")}, true, buildIdentity{"v0.1.0", "782404bdd594"}},
		{"module version", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, true, buildIdentity{"v0.1.0", ""}},
		{"devel module", "", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs("true")}, true, buildIdentity{"devel", "782404bdd594-dirty"}},
		{"empty module", "", &debug.BuildInfo{}, true, buildIdentity{"devel", ""}},
		{"clean revision", "", &debug.BuildInfo{Settings: vcs("false")}, true, buildIdentity{"devel", "782404bdd594"}},
		{"revision without modified", "", &debug.BuildInfo{Settings: vcs("")}, true, buildIdentity{"devel", "782404bdd594"}},
		{"short revision", "", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}, {Key: "vcs.modified", Value: "true"}}}, true, buildIdentity{"devel", "abc-dirty"}},
		{"modified without revision", "", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}}, true, buildIdentity{"devel", ""}},
		{"no build info", "", nil, false, buildIdentity{"devel", ""}},
		{"no build info, linked", "v1.2.3", nil, false, buildIdentity{"v1.2.3", ""}},
	}
	for _, c := range cases {
		if got := resolveIdentity(c.linked, c.bi, c.ok); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// versionOut runs versionCommand with a fixed identity.
func versionOut(t *testing.T, args []string, id buildIdentity) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := versionCommand(args, id, &out)
	return code, out.String()
}

func TestVersionJSON(t *testing.T) {
	for _, id := range []buildIdentity{{"v0.1.0", "782404bdd594"}, {"devel", ""}} {
		code, out := versionOut(t, nil, id)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if strings.Count(strings.TrimSpace(out), "\n") != 0 {
			t.Fatalf("more than one line: %q", out)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			t.Fatal(err)
		}
		var res map[string]any
		if err := json.Unmarshal(raw["result"], &res); err != nil {
			t.Fatal(err)
		}
		keys := []string{"version", "commit", "go", "os", "arch", "schema_versions"}
		if len(res) != len(keys) {
			t.Fatalf("result keys %v, want exactly %v", res, keys)
		}
		for _, k := range keys {
			if _, ok := res[k]; !ok {
				t.Fatalf("result lacks %q: %v", k, res)
			}
		}
		if res["version"] != id.Version || res["commit"] != id.Commit || res["go"] != runtime.Version() || res["os"] != runtime.GOOS || res["arch"] != runtime.GOARCH {
			t.Fatalf("result %v for identity %+v", res, id)
		}
		var sv []string
		if err := json.Unmarshal(mustJSON(t, res["schema_versions"]), &sv); err != nil || !reflect.DeepEqual(sv, diag.Accepted) {
			t.Fatalf("schema_versions %v, want %v", res["schema_versions"], diag.Accepted)
		}
		var resp diag.Response
		if err := json.Unmarshal([]byte(out), &resp); err != nil || !resp.OK || resp.SchemaVersion != diag.SchemaVersion || resp.Diagnostics == nil || len(resp.Diagnostics) != 0 {
			t.Fatalf("envelope %+v (%v)", resp, err)
		}
		if !strings.Contains(out, `"diagnostics":[]`) {
			t.Fatalf("diagnostics is not an empty list: %s", out)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVersionHuman(t *testing.T) {
	tail := runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH + ")\n"
	for _, args := range [][]string{{"--human"}, {"--human", "--human"}} {
		code, out := versionOut(t, args, buildIdentity{"v0.1.0", "782404bdd594"})
		if want := "kern v0.1.0 (782404bdd594, " + tail; code != 0 || out != want {
			t.Fatalf("%v: exit %d %q, want %q", args, code, out, want)
		}
	}
	code, out := versionOut(t, []string{"--human"}, buildIdentity{"devel", ""})
	if want := "kern devel (" + tail; code != 0 || out != want {
		t.Fatalf("commit-less: exit %d %q, want %q", code, out, want)
	}
}

func TestVersionCLI(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, strings.NewReader(""), &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "kern ") || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("--version: exit %d %q", code, out.String())
	}
	// `kern --version` is exactly `kern version --human`
	var human bytes.Buffer
	if code := run([]string{"version", "--human"}, strings.NewReader(""), &human, &errb); code != 0 || human.String() != out.String() {
		t.Fatalf("version --human %q differs from --version %q", human.String(), out.String())
	}
	code, resp, _ := runWith(t, []string{"version"}, "")
	if code != 0 || !resp.OK {
		t.Fatalf("version: exit %d %+v", code, resp)
	}
	for _, args := range [][]string{
		{"version", "--json"},
		{"version", "file.kern"},
		{"version", "--human", "x"},
		{"--version", "extra"},
		{"--version", "--human"},
		{"--version", "--version"},
	} {
		code, resp, _ := runWith(t, args, "")
		if code != 2 || resp.OK || len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Code != "CLI_INVALID_FLAGS" {
			t.Fatalf("%v: exit %d %+v, want CLI_INVALID_FLAGS", args, code, resp)
		}
	}
}

func TestUsageListsVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, strings.NewReader(""), &out, &errb); code != 2 || !strings.Contains(errb.String(), "|version>") {
		t.Fatalf("usage: exit %d %q", code, errb.String())
	}
}

// TestLinkedVersion builds the command with the release script's linker
// flag and checks that the binary reports that version.
func TestLinkedVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the compiler")
	}
	bin := filepath.Join(t.TempDir(), "kern")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version=v0.0.0-test", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "version", "--human").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "kern v0.0.0-test (") {
		t.Fatalf("linked binary reports %q", out)
	}
}
