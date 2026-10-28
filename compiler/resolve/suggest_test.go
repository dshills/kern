package resolve

import "testing"

func TestNearest(t *testing.T) {
	cases := []struct {
		name  string
		cands []string
		want  string
	}{
		{"totl", []string{"total", "print_u64"}, "total"},
		{"lenn", []string{"len", "push", "pop"}, "len"},
		{"nothing", []string{"total", "len"}, ""},
		{"ab", []string{"abc", "abd"}, "abc"}, // ties go to the lexically first
		{"abcdef", []string{"abcxyz"}, ""},    // three edits is too far
		{"abcdefg", []string{"abcdxfg"}, "abcdxfg"},
		{"x", []string{"xyzw"}, ""}, // length alone rules it out
	}
	for _, c := range cases {
		if got := nearest(c.name, c.cands); got != c.want {
			t.Errorf("nearest(%q, %v) = %q, want %q", c.name, c.cands, got, c.want)
		}
	}
	if d := editDistance([]rune("kitten"), []rune("sitting"), 10); d != 3 {
		t.Errorf("editDistance = %d, want 3", d)
	}
	if d := editDistance([]rune("kitten"), []rune("sitting"), 1); d != 2 {
		t.Errorf("bounded editDistance = %d, want limit+1 = 2", d)
	}
}
