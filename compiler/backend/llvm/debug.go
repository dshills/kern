package llvm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
)

// debugInfo accumulates DWARF metadata: one compile unit, one DIFile per
// source file, one DISubprogram per function and a DILocation per distinct
// (function, line, column) an instruction carries (PLAN.md Phase 5).
type debugInfo struct {
	sources map[string][]byte
	lines   map[string][]int // newline offsets per file, for binary search
	files   map[string]int   // file name -> metadata id
	subs    map[string]int   // function name -> DISubprogram id
	locs    map[string]int   // "sub:line:col" -> DILocation id
	nodes   []string         // metadata nodes in id order
	cuID    int
	typeID  int
}

func newDebugInfo(sources map[string][]byte) *debugInfo {
	d := &debugInfo{sources: sources, lines: map[string][]int{}, files: map[string]int{}, subs: map[string]int{}, locs: map[string]int{}}
	d.cuID = d.add("") // filled once the main file is known
	d.typeID = d.add("!DISubroutineType(types: !{null})")
	return d
}

func (d *debugInfo) add(node string) int {
	d.nodes = append(d.nodes, node)
	return len(d.nodes) - 1
}

func (d *debugInfo) file(name string) int {
	if id, ok := d.files[name]; ok {
		return id
	}
	dir, base := ".", name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		dir, base = name[:i], name[i+1:]
	}
	id := d.add(fmt.Sprintf("!DIFile(filename: \"%s\", directory: \"%s\")", mdEscape(base), mdEscape(dir)))
	d.files[name] = id
	if d.nodes[d.cuID] == "" {
		d.nodes[d.cuID] = fmt.Sprintf("distinct !DICompileUnit(language: DW_LANG_C, file: !%d, producer: \"kern\", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)", id)
	}
	return id
}

// lineCol maps a byte offset of a file to 1-based line and column, with
// the file's newline offsets computed once and searched by bisection.
func (d *debugInfo) lineCol(file string, off int) (int, int) {
	src := d.sources[file]
	if off > len(src) {
		off = len(src)
	}
	if off < 0 {
		off = 0
	}
	nl, ok := d.lines[file]
	if !ok {
		for i, c := range src {
			if c == '\n' {
				nl = append(nl, i)
			}
		}
		d.lines[file] = nl
	}
	// the number of newlines before off
	n := sort.SearchInts(nl, off)
	lineStart := 0
	if n > 0 {
		lineStart = nl[n-1] + 1
	}
	return n + 1, off - lineStart + 1
}

// mdEscape renders a string for an LLVM metadata string field: quotes,
// backslashes and control bytes as \XX.
func mdEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c < 0x7f && c != '"' && c != '\\' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "\\%02X", c)
		}
	}
	return b.String()
}

// subprogram returns the metadata reference of a function's DISubprogram.
func (d *debugInfo) subprogram(f *ir.Func) string {
	if id, ok := d.subs[f.Name]; ok {
		return fmt.Sprintf("!%d", id)
	}
	fid := d.file(f.File)
	line, _ := d.lineCol(f.File, f.Pos.Start)
	id := d.add(fmt.Sprintf("distinct !DISubprogram(name: \"%s\", linkageName: \"%s\", scope: !%d, file: !%d, line: %d, type: !%d, scopeLine: %d, spFlags: DISPFlagDefinition, unit: !%d)", mdEscape(bareName(f.Name)), mdEscape(f.Name), fid, fid, line, d.typeID, line, d.cuID))
	d.subs[f.Name] = id
	return fmt.Sprintf("!%d", id)
}

// location returns the DILocation reference for a position in f.
func (d *debugInfo) location(f *ir.Func, p ast.Pos) string {
	sub := d.subprogram(f)
	if p.Start == 0 && p.End == 0 {
		p = f.Pos
	}
	line, col := d.lineCol(f.File, p.Start)
	key := fmt.Sprintf("%s:%d:%d", sub, line, col)
	if id, ok := d.locs[key]; ok {
		return fmt.Sprintf("!%d", id)
	}
	id := d.add(fmt.Sprintf("!DILocation(line: %d, column: %d, scope: %s)", line, col, sub))
	d.locs[key] = id
	return fmt.Sprintf("!%d", id)
}

// metadata renders the module-level debug metadata.
func (d *debugInfo) metadata() string {
	var b strings.Builder
	if d.nodes[d.cuID] == "" {
		d.file("unknown.kern")
	}
	flags := d.add(`!{i32 2, !"Dwarf Version", i32 4}`)
	ver := d.add(`!{i32 2, !"Debug Info Version", i32 3}`)
	fmt.Fprintf(&b, "\n!llvm.dbg.cu = !{!%d}\n!llvm.module.flags = !{!%d, !%d}\n", d.cuID, flags, ver)
	for i, n := range d.nodes {
		fmt.Fprintf(&b, "!%d = %s\n", i, n)
	}
	return b.String()
}

// instrPos is the source position an instruction carries, or an empty
// position (the function's line is used).
func instrPos(in ir.Instr) ast.Pos {
	switch i := in.(type) {
	case *ir.Call:
		return i.Pos
	case *ir.CheckBounds:
		return i.Pos
	case *ir.CheckOverflow:
		return i.Pos
	case *ir.CheckConv:
		return i.Pos
	case *ir.Trap:
		return i.Pos
	case *ir.Load:
		return i.Pos
	case *ir.Drop:
		return i.Pos
	case *ir.AccessBegin:
		return i.Pos
	}
	return ast.Pos{}
}
