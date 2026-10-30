package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/incremental"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

const navLib = `module lib
const Limit: u64 = 10u64
struct Point { X: u64; Y: u64 }
impl Point {
    func Sum(view self) -> u64 { return self.X + self.Y }
}
func Make(x: u64) -> Point { return Point { X: x, Y: 2u64 } }
`

const navMain = `module app
import "lib"
thread_local counter: u64 = 0u64
func main() -> i32 {
    p := lib.Make(3u64)
    total := p.Sum() + p.X + lib.Limit
    counter = total
    print_u64(total + counter)
    println()
    return 0i32
}
`

// pos is the UTF-16 position of the n-th occurrence of sub in text, plus
// delta bytes.
func pos(text, sub string, n, delta int) Position { return at(text, sub, n, delta) }

func (c *client) definition(uri string, p Position) (*Location, *rpcError) {
	c.t.Helper()
	var l *Location
	e := c.call("textDocument/definition", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p}, &l)
	return l, e
}

func (c *client) refs(uri string, p Position, decl bool) []Location {
	c.t.Helper()
	var ls []Location
	if e := c.call("textDocument/references", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p, "context": map[string]any{"includeDeclaration": decl}}, &ls); e != nil {
		c.t.Fatalf("references: %+v", e)
	}
	return ls
}

// TestDefinitionAndReferences (specs/editor-nav §4 item 1).
func TestDefinitionAndReferences(t *testing.T) {
	dir := writeTree(t, map[string]string{"main.kern": navMain, "lib/lib.kern": navLib})
	mainURI, libURI := URIOf(filepath.Join(dir, "main.kern")), URIOf(filepath.Join(dir, "lib", "lib.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(mainURI, navMain)
	type loc struct {
		uri  string
		text string // text beginning at the declaration's name
		n    int
		size int // the name's length; the length of text when 0
	}
	checkLoc := func(what string, l *Location, want loc) {
		t.Helper()
		src := navMain
		if want.uri == libURI {
			src = navLib
		}
		size := want.size
		if size == 0 {
			size = len(want.text)
		}
		exp := Range{Start: at(src, want.text, want.n, 0), End: at(src, want.text, want.n, size)}
		if l == nil || l.URI != want.uri || l.Range != exp {
			t.Errorf("%s: %+v, want %s %v", what, l, want.uri, exp)
		}
	}
	for _, k := range []struct {
		what string
		p    Position
		want loc
	}{
		{"function", pos(navMain, "Make(3u64)", 0, 1), loc{libURI, "Make", 0, 0}},
		{"method", pos(navMain, "Sum()", 0, 0), loc{libURI, "Sum", 0, 0}},
		{"field", pos(navMain, "p.X", 0, 2), loc{libURI, "X", 0, 0}},
		{"constant", pos(navMain, "lib.Limit", 0, 5), loc{libURI, "Limit", 0, 0}},
		{"global", pos(navMain, "counter = total", 0, 0), loc{mainURI, "counter", 0, 0}},
		{"local", pos(navMain, "total + counter", 0, 1), loc{mainURI, "total", 0, 0}},
		{"local p", pos(navMain, "p.Sum", 0, 0), loc{mainURI, "p := lib", 0, 1}},
		{"a declaration's own name", pos(navMain, "total :=", 0, 0), loc{mainURI, "total", 0, 0}},
	} {
		l, e := c.definition(mainURI, k.p)
		if e != nil {
			t.Fatalf("%s: %+v", k.what, e)
		}
		checkLoc(k.what, l, k.want)
	}
	for _, k := range []struct {
		what string
		p    Position
	}{{"whitespace", Position{Line: 3, Character: 0}}, {"a prelude function", pos(navMain, "print_u64", 0, 2)}} {
		if l, e := c.definition(mainURI, k.p); l != nil || e != nil {
			t.Errorf("%s: %+v %+v, want null", k.what, l, e)
		}
	}
	if l, e := c.definition("untitled:1", Position{}); l != nil || e != nil {
		t.Errorf("non-kern document: %+v %+v", l, e)
	}
	// references, across both files, in file then offset order
	render := func(ls []Location) string {
		var out []string
		for _, l := range ls {
			out = append(out, filepath.Base(l.URI)+":"+itoa(l.Range.Start.Line)+":"+itoa(l.Range.Start.Character))
		}
		return strings.Join(out, " ")
	}
	cases := []struct {
		what       string
		p          Position
		with, sans string
	}{
		{"function", pos(navMain, "Make(3u64)", 0, 0), "lib.kern:6:5 main.kern:4:13", "main.kern:4:13"},
		// the struct literal's X in Make is not a name: the resolver does not
		// record literal field names
		{"field", pos(navMain, "p.X", 0, 2), "lib.kern:2:15 lib.kern:4:45 main.kern:5:25", "lib.kern:4:45 main.kern:5:25"},
		{"method", pos(navMain, "p.Sum", 0, 2), "lib.kern:4:9 main.kern:5:15", "main.kern:5:15"},
		{"local", pos(navMain, "total :=", 0, 0), "main.kern:5:4 main.kern:6:14 main.kern:7:14", "main.kern:6:14 main.kern:7:14"},
	}
	for _, k := range cases {
		if got := render(c.refs(mainURI, k.p, true)); got != k.with {
			t.Errorf("%s with declaration: %s, want %s", k.what, got, k.with)
		}
		if got := render(c.refs(mainURI, k.p, false)); got != k.sans {
			t.Errorf("%s without declaration: %s, want %s", k.what, got, k.sans)
		}
	}
	if got := c.refs(mainURI, pos(navMain, "print_u64", 0, 0), true); len(got) != 0 {
		t.Errorf("references of a prelude function: %+v", got)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// TestNavigationPanic: a panicking resolver answers -32603 and the server
// continues (R4).
func TestNavigationPanic(t *testing.T) {
	dir := writeTree(t, map[string]string{"main.kern": navMain, "lib/lib.kern": navLib})
	uri := URIOf(filepath.Join(dir, "main.kern"))
	c := start(t, Options{Resolve: func([]*source.File) *resolve.Program { panic("resolver broke") }, Check: func([]*source.File) ([]diag.Diagnostic, error) { return nil, nil }})
	c.initialize(nil, true)
	c.open(uri, navMain)
	if _, e := c.definition(uri, Position{Line: 4, Character: 4}); e == nil || e.Code != codeInternalError {
		t.Errorf("definition: %+v", e)
	}
	if e := c.call("textDocument/references", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": Position{Line: 4, Character: 4}, "context": map[string]any{}}, nil); e == nil || e.Code != codeInternalError {
		t.Errorf("references: %+v", e)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

// TestTestFilesPublished: a test file's diagnostics are published when
// another document of its module is open (R7).
func TestTestFilesPublished(t *testing.T) {
	app := "module app\nfunc main() -> i32 { return 0i32 }\n"
	dir := writeTree(t, map[string]string{"app.kern": app, "app_test.kern": "module app\nfunc test_x(n: u64) { }\n"})
	appURI, testURI := URIOf(filepath.Join(dir, "app.kern")), URIOf(filepath.Join(dir, "app_test.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(appURI, app)
	p := c.publication(testURI)
	if codesOf(p.Diagnostics) != "TEST_SIGNATURE" {
		t.Errorf("test file publication %+v", p)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

type statLog struct {
	mu  sync.Mutex
	got map[string][]incremental.Stats
}

func (l *statLog) add(uri string, st incremental.Stats) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got[uri] = append(l.got[uri], st)
}

func (l *statLog) last(uri string) (incremental.Stats, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	xs := l.got[uri]
	if len(xs) == 0 {
		return incremental.Stats{}, 0
	}
	return xs[len(xs)-1], len(xs)
}

// fence waits for every pending check: a hover on a non-file document
// ends the current batch.
func (c *client) fence() []rpc {
	c.t.Helper()
	_, notes := c.reply(c.request("textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": "untitled:x"}, "position": Position{}}))
	return notes
}

// TestCachedChecks (R8, §4 item 4): three programs; changing one serves
// the other two from the cache; a program with a resolution error never
// hits; a changed file set misses.
func TestCachedChecks(t *testing.T) {
	mainSrc := "module app\nimport \"lib\"\nfunc main() -> i32 { return i32(lib.F()) }\n"
	libSrc := "module lib\nfunc F() -> u64 { return 0u64 }\n"
	utilSrc := "module util\nfunc G() -> u64 { return 1u64 }\n"
	dir := writeTree(t, map[string]string{"main.kern": mainSrc, "lib/lib.kern": libSrc, "util/util.kern": utilSrc, "bad/bad.kern": "module bad\nfunc H() -> u64 { return nope }\n"})
	mainURI, libURI, utilURI, badURI := URIOf(filepath.Join(dir, "main.kern")), URIOf(filepath.Join(dir, "lib", "lib.kern")), URIOf(filepath.Join(dir, "util", "util.kern")), URIOf(filepath.Join(dir, "bad", "bad.kern"))
	log := &statLog{got: map[string][]incremental.Stats{}}
	c := start(t, Options{CacheStats: log.add})
	c.initialize(nil, true)
	c.open(mainURI, mainSrc)
	c.open(libURI, libSrc)
	c.open(utilURI, utilSrc)
	c.open(badURI, "module bad\nfunc H() -> u64 { return nope }\n")
	waitChecks := func(uri string, n int) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, k := log.last(uri); k >= n || time.Now().After(deadline) {
				return
			}
			c.fence()
		}
	}
	for _, u := range []string{mainURI, libURI, utilURI, badURI} {
		waitChecks(u, 1)
	}
	_, nMain := log.last(mainURI)
	_, nLib := log.last(libURI)
	_, nUtil := log.last(utilURI)
	_, nBad := log.last(badURI)
	c.change(utilURI, 2, utilSrc+"// edited\n")
	waitChecks(utilURI, nUtil+1)
	waitChecks(mainURI, nMain+1)
	waitChecks(libURI, nLib+1)
	waitChecks(badURI, nBad+1)
	for _, k := range []struct {
		uri  string
		hits bool
	}{{mainURI, true}, {libURI, true}, {utilURI, false}, {badURI, false}} {
		st, _ := log.last(k.uri)
		full := st.Modules > 0 && st.Hits == st.Modules
		if full != k.hits {
			t.Errorf("%s: stats %+v, full hit %v, want %v", filepath.Base(k.uri), st, full, k.hits)
		}
	}
	// a new file in util's directory changes its program's file set
	if err := os.WriteFile(filepath.Join(dir, "util", "more.kern"), []byte("module util\nfunc K() -> u64 { return 2u64 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, nUtil = log.last(utilURI)
	_, nMain = log.last(mainURI)
	c.change(mainURI, 2, mainSrc)
	waitChecks(utilURI, nUtil+1)
	waitChecks(mainURI, nMain+1)
	if st, _ := log.last(utilURI); st.Hits == st.Modules {
		t.Errorf("util after a new file: %+v, want a miss", st)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

// TestCacheFallback: an unusable cache directory checks uncached with one
// warning over several checks, and publishes what a cached server does.
func TestCacheFallback(t *testing.T) {
	src := "module app\nfunc main() -> i32 { return nope }\n"
	dir := writeTree(t, map[string]string{"app.kern": src, "blocker": "a file, not a directory"})
	uri := URIOf(filepath.Join(dir, "app.kern"))
	published := func(opts Options) (PublishDiagnosticsParams, int) {
		c := start(t, opts)
		c.initialize(nil, true)
		c.open(uri, src)
		p := c.publication(uri)
		c.change(uri, 2, src+"\n")
		p2 := c.publication(uri)
		c.fence()
		_ = p
		c.stop()
		// every logMessage is also written to the server's log
		return p2, strings.Count(c.log.String(), "without the incremental cache")
	}
	cached, w0 := published(Options{})
	uncached, w1 := published(Options{CacheDir: filepath.Join(dir, "blocker", "sub")})
	if w0 != 0 || w1 != 1 {
		t.Errorf("warnings: cached %d, uncached %d; want 0 and 1", w0, w1)
	}
	if codesOf(cached.Diagnostics) != codesOf(uncached.Diagnostics) || len(cached.Diagnostics) == 0 || cached.Diagnostics[0].Range != uncached.Diagnostics[0].Range {
		t.Errorf("cached %+v, uncached %+v", cached.Diagnostics, uncached.Diagnostics)
	}
}

// TestNameSpan: the declared name is found after the keyword, as a whole
// word, outside a comment.
func TestNameSpan(t *testing.T) {
	src := []byte("func // Make is built here\n    Make(x: u64) -> u64 { return x }")
	sp := nameSpan(src, 0, len(src), "func", "Make")
	if string(src[sp.Start:sp.End]) != "Make" || sp.Start < 27 {
		t.Errorf("span %v (%q), want the declaration's Make on the second line", sp, src[sp.Start:sp.End])
	}
	src = []byte("func MakeAll[Make](m: Make) {}")
	if sp := nameSpan(src, 0, len(src), "func", "MakeAll"); sp != (diag.Span{Start: 5, End: 12}) {
		t.Errorf("span %v", sp)
	}
	src = []byte("struct S { Make: u64 }")
	if sp := nameSpan(src, 11, 21, "", "Make"); sp != (diag.Span{Start: 11, End: 15}) {
		t.Errorf("field span %v", sp)
	}
}

func TestInComment(t *testing.T) {
	for _, c := range []struct {
		src  string
		want bool
	}{
		{`x // name`, true},
		{`requires "a//b" } func name`, false},
		{`requires "a\"//b" } func name`, false},
		{`"s" // name`, true},
		{"line one // c\nname", false},
	} {
		at := strings.LastIndex(c.src, "name")
		if got := inComment([]byte(c.src), at); got != c.want {
			t.Errorf("inComment(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}
