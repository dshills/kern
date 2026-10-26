package native

import (
	"strings"
	"testing"
)

func TestHostTriple(t *testing.T) {
	tr := HostTriple()
	if !strings.Contains(tr, "-") || (!strings.HasPrefix(tr, "aarch64") && !strings.HasPrefix(tr, "x86_64")) {
		t.Fatalf("unexpected host triple %q", tr)
	}
}

func TestWithProgramName(t *testing.T) {
	in := "declare void @kern_start()\ndefine i32 @main() {\nentry:\n  ret i32 0\n}\n"
	out := withProgramName(in, `a"b\c.kern`, false)
	for _, want := range []string{
		`@kern.progname = private constant [11 x i8] c"a\22b\5Cc.kern\00"`,
		"declare void @kern_set_file(ptr)",
		"call void @kern_set_file(ptr @kern.progname)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}
