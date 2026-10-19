package main

import (
	"strings"
	"testing"
)

// Aliases are installed as symlinks, so the binary has to work out what it was
// called by. Getting this wrong makes an alias second-class: you run
// `tokenSummary --help` and it tells you about `tcount`.
func TestInvokedName(t *testing.T) {
	cases := []struct{ argv0, want string }{
		{"tcount", "tcount"},
		{"./tcount", "tcount"},
		{"/home/u/.local/bin/tcount", "tcount"},
		{"/home/u/.local/bin/token-count", "token-count"},
		{"/home/u/.local/bin/tokenSummary", "tokenSummary"},
		{"tsum", "tsum"},
		{`C:\tools\tcount.exe`, `C:\tools\tcount`}, // only POSIX separators are split
		{"tcount.exe", "tcount"},
		// Nothing usable to show falls back to the default rather than
		// printing an empty or nonsense name.
		{"", defaultName},
		{".", defaultName},
		{"..", defaultName},
		{"/", defaultName},
	}
	for _, c := range cases {
		if got := invokedName(c.argv0); got != c.want {
			t.Errorf("invokedName(%q) = %q, want %q", c.argv0, got, c.want)
		}
	}
}

// The invoked name has to reach every place the tool names itself, or an alias
// looks half-applied.
func TestUsageUsesInvokedName(t *testing.T) {
	for _, name := range []string{"tcount", "tokenSummary", "tsum"} {
		u := usage(name)
		if !strings.HasPrefix(u, name+" — ") {
			t.Errorf("%s: usage does not open with the invoked name:\n%s", name, u[:40])
		}
		// The worked examples must be runnable as written.
		if !strings.Contains(u, name+" week projects") {
			t.Errorf("%s: usage examples still use another name", name)
		}
		if name != "tcount" && strings.Contains(u, "tcount") {
			t.Errorf("%s: usage leaked the default name", name)
		}
		if strings.Contains(u, "NAME") {
			t.Errorf("%s: usage template placeholder was not substituted", name)
		}
	}
}

func TestParsePeriodAndView(t *testing.T) {
	if _, ok := parsePeriod("week"); !ok {
		t.Error("week should parse as a span")
	}
	if _, ok := parsePeriod("projects"); ok {
		t.Error("projects is not a span")
	}
	if v, ok := parseView("project"); !ok || string(v) != "project" {
		t.Errorf("project -> %v %v", v, ok)
	}
	if v, ok := parseView("heatmap"); !ok || string(v) != "heat" {
		t.Errorf("heatmap should alias to heat, got %v", v)
	}
	if _, ok := parseView("nope"); ok {
		t.Error("nope is not a view")
	}
}
