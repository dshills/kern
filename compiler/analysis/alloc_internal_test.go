package analysis

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// oldAllocating is the allocation table as it stood at b49589f, before
// specs/alloc-sites-std R1 corrected it.
var oldAllocating = map[string]bool{
	"Box.new": true, "Vec.new": true, "Vec.with_capacity": true, "Pool.new": true, "Arena.new": true,
	"Shared.new": true, "Mutex.new": true, "Condvar.new": true, "thread.scope": true,
	"Vec.push": false, "Pool.alloc": false, "Arena.alloc": false, "thread.spawn": false, "scope.spawn": false,
	"SystemAllocator.alloc": false, "SystemAllocator.realloc": false, "SystemAllocator.alloc_zeroed": false, "clone": false,
}

// oldSites is the table rule as it stood at b49589f, each site with the
// table key that produced it.
func oldSites(mod *ir.Module) map[string]string {
	keys := map[string]string{}
	for _, f := range mod.Funcs {
		if f.Library || f.Inlined {
			continue
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				c, ok := in.(*ir.Call)
				if !ok {
					continue
				}
				key := c.Intrinsic
				if key == "" {
					if isCloneCall(mod, c) {
						key = "clone"
					} else if lib := mod.Func(c.Callee); lib != nil && lib.Library {
						key = baseName(c.Callee)
					}
				}
				if _, known := oldAllocating[key]; !known {
					continue
				}
				if key == "clone" && (len(c.Dsts) != 1 || resolve.IsCopy(c.Dsts[0].Type)) {
					continue
				}
				k := siteKey(loc(f, b, c.Pos))
				if _, seen := keys[k]; !seen {
					keys[k] = key
				}
			}
		}
	}
	return keys
}

func siteKey(l diag.Loc) string { return fmt.Sprintf("%s|%d|%d", l.File, l.Span.Start, l.Span.End) }

// lowerProgram resolves, checks, lowers and optimizes one file; ok is false
// when the program does not resolve or check (it is then skipped), and a
// lowering or optimization failure of a checked program fails the test.
func lowerProgram(t *testing.T, name string, src []byte, level string) (*ir.Module, bool) {
	t.Helper()
	prog := resolve.ResolveFor([]*source.File{{Name: name, Bytes: src}}, level == "release")
	if len(prog.Diags) > 0 || len(check.Check(prog)) > 0 {
		return nil, false
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		t.Errorf("%s (%s): a checked program fails to lower: %s", name, level, ds[0].Message)
		return nil, false
	}
	if err := opt.Run(mod, opt.OptionsFor(level)); err != nil {
		t.Errorf("%s (%s): opt: %v", name, level, err)
		return nil, false
	}
	return mod, true
}

// TestAllocationSitesAgreeWithEffects sweeps the run fixtures in both opt
// levels (specs/alloc-sites-std §4 item 2): the old table sites, less those
// R1 corrects, survive with equal fields; every call from a user function
// to an allocating library function is a site (R3); and every allocating
// user function has a site of its own or another reason (R3's consequence).
func TestAllocationSitesAgreeWithEffects(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "tests", "run", "*.kern"))
	if len(files) == 0 {
		t.Fatal("no run fixtures")
	}
	checked := 0
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, level := range []string{"debug", "release"} {
			mod, ok := lowerProgram(t, filepath.Base(path), src, level)
			if !ok {
				continue
			}
			checked++
			res := Allocations(mod)
			sites := map[string]diag.AllocationSite{}
			for _, s := range res.Sites {
				sites[siteKey(s.Loc)] = s
			}
			// R1: old sites survive unless corrected
			for k, key := range oldSites(mod) {
				switch key {
				case "Vec.new", "Pool.new", "Arena.new":
					if _, ok := sites[k]; ok && sites[k].Label == diag.LabelProven {
						t.Errorf("%s (%s): %s at %s is still a proven site", path, level, key, k)
					}
					continue
				}
				s, ok := sites[k]
				if !ok {
					t.Errorf("%s (%s): the %s site at %s is gone", path, level, key, k)
					continue
				}
				wantLabel := diag.LabelUnknown
				if allocating[key] {
					wantLabel = diag.LabelProven
				}
				if s.Label != wantLabel || s.Allocator != "system" || (key == "Vec.with_capacity" && s.Size != nil) {
					t.Errorf("%s (%s): the %s site at %s changed: %+v", path, level, key, k, s)
				}
			}
			summaries, _ := effectSummaries(mod)
			for _, f := range mod.Funcs {
				if f.Library || f.Inlined {
					continue
				}
				own, other := false, false
				for _, b := range f.Blocks {
					for _, in := range b.Instrs {
						switch i := in.(type) {
						case *ir.Call:
							if _, ok := sites[siteKey(loc(f, b, i.Pos))]; ok {
								own = true
							}
							if i.Fn != nil || i.Intrinsic == "ptr_init" || i.Intrinsic == "ptr_init_ctx" {
								other = true
								continue
							}
							callee := mod.Func(i.Callee)
							if i.Intrinsic != "" || callee == nil {
								continue
							}
							e := summaries[callee.Name]
							if callee.Library && e != nil && e.allocates {
								if _, ok := sites[siteKey(loc(f, b, i.Pos))]; !ok {
									t.Errorf("%s (%s): %s calls allocating %s at %d with no site", path, level, f.Name, callee.Name, i.Pos.Start)
								}
							}
							if !callee.Library && (callee.Extern != "" || e != nil && e.allocates) {
								other = true
							}
						case *ir.Drop:
							if g := destructorOf(mod, i.Type); g != nil && summaries[g.Name] != nil && summaries[g.Name].allocates {
								other = true
							}
						}
					}
				}
				if e := summaries[f.Name]; e != nil && e.allocates && !own && !other {
					t.Errorf("%s (%s): %s allocates but has no site and no allocating callee", path, level, f.Name)
				}
			}
		}
	}
	if checked < 50 {
		t.Errorf("only %d fixture lowerings checked", checked)
	}
}

// tableCalls has one call of every library entry of the allocation table,
// in a program TestAllocationTableEntriesAllocate lowers.
var tableCalls = map[string]string{
	"Box.new":           "b := Box[u64].new(1u64)",
	"Vec.push":          "v.push(1u64)",
	"Vec.with_capacity": "w := Vec[u64].with_capacity(4usize)",
	"Pool.alloc":        "pid := p.alloc(2u64)",
	"Arena.alloc":       "aid := a.alloc(3u64)",
	"clone":             "c := copy(v)",
}

// TestAllocationTableEntriesAllocate pins the table to the library
// (specs/alloc-sites-std R8): every library entry's callee has an
// allocating effect summary, so an entry for a constructor that allocates
// nothing (as Vec.new is) fails here.
func TestAllocationTableEntriesAllocate(t *testing.T) {
	var body []string
	for key, call := range tableCalls {
		body = append(body, "    // "+key, "    "+call)
	}
	src := "module m\nfunc main() -> i32 {\n    var v: Vec[u64] = Vec[u64].new()\n    p := Pool[u64].new()\n    a := Arena[u64].new()\n" + strings.Join(body, "\n") + "\n    return 0i32\n}\n"
	mod, ok := lowerProgram(t, "m.kern", []byte(src), "debug")
	if !ok {
		t.Fatalf("the table program does not check:\n%s", src)
	}
	summaries, _ := effectSummaries(mod)
	seen := map[string]bool{}
	for _, f := range mod.Funcs {
		if f.Library {
			continue
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				c, ok := in.(*ir.Call)
				if !ok || c.Intrinsic != "" {
					continue
				}
				callee := mod.Func(c.Callee)
				if callee == nil || !callee.Library {
					continue
				}
				key := baseName(c.Callee)
				if isCloneCall(mod, c) {
					key = "clone"
				}
				if _, entry := allocating[key]; !entry {
					continue
				}
				seen[key] = true
				if e := summaries[callee.Name]; e == nil || !e.allocates {
					t.Errorf("table entry %s: %s does not allocate", key, callee.Name)
				}
			}
		}
	}
	for key := range allocating {
		if fn := mod.Func(key); fn != nil && !fn.Library {
			continue
		}
		intrinsic := !strings.Contains(key, ".") || strings.HasPrefix(key, "Shared.") || strings.HasPrefix(key, "Mutex.") || strings.HasPrefix(key, "Condvar.") || strings.HasPrefix(key, "thread.") || strings.HasPrefix(key, "scope.") || strings.HasPrefix(key, "SystemAllocator.")
		if intrinsic && key != "clone" {
			continue
		}
		if !seen[key] {
			t.Errorf("table entry %s has no call in tableCalls: add one", key)
		}
	}
}
