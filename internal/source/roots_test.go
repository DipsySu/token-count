package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvRootsSeparators(t *testing.T) {
	sep := string(os.PathListSeparator)
	cases := []struct {
		raw  string
		want int
	}{
		{"", 0},
		{"   ", 0},
		{"/a", 1},
		{"/a,/b", 2},
		{"/a, /b , /c", 3},
		{"/a" + sep + "/b", 2},
		{"/a,/b" + sep + "/c", 3},
		{"/a,,/b", 2}, // empty entries are skipped
		{"/a,/a", 1},  // the same root twice is walked once
		{"/a,/a/", 1}, // ...including a trailing separator
	}
	for _, c := range cases {
		t.Setenv("TCOUNT_TEST_ROOTS", c.raw)
		if got := envRoots("TCOUNT_TEST_ROOTS"); len(got) != c.want {
			t.Errorf("envRoots(%q) = %v, want %d entries", c.raw, got, c.want)
		}
	}
}

func TestEnvRootsExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	t.Setenv("TCOUNT_TEST_ROOTS", "~/x,~/y")
	got := envRoots("TCOUNT_TEST_ROOTS")
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	for _, g := range got {
		if !strings.HasPrefix(g, home) {
			t.Errorf("%q was not expanded", g)
		}
	}
}

// Claude Code itself reads two locations, so both are scanned unless the
// environment says otherwise.
func TestClaudeRootsDefaultsAndOverride(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	def := claudeRoots()
	if len(def) != 2 {
		t.Fatalf("expected the home and XDG locations, got %v", def)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "/one,/two,/three")
	if got := claudeRoots(); len(got) != 3 {
		t.Fatalf("override gave %v", got)
	}
}

// Overlapping or nested roots must not hand the same file to the parser twice.
func TestListFilesDeduplicatesOverlappingRoots(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"one.jsonl", "two.jsonl"} {
		if err := os.WriteFile(filepath.Join(nested, n), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	match := suffix(".jsonl")

	if got := listFiles([]string{root}, match); len(got) != 2 {
		t.Fatalf("single root found %d files", len(got))
	}
	// The same root twice, and a root nested inside another, both collapse.
	if got := listFiles([]string{root, root}, match); len(got) != 2 {
		t.Errorf("repeated root found %d files, want 2", len(got))
	}
	if got := listFiles([]string{root, nested}, match); len(got) != 2 {
		t.Errorf("nested roots found %d files, want 2", len(got))
	}
	// A missing root is skipped rather than failing the whole scan.
	if got := listFiles([]string{filepath.Join(root, "nope"), root}, match); len(got) != 2 {
		t.Errorf("missing root broke the scan: %d files", len(got))
	}
}

// Every configured root shows up in the source listing, so a multi-root setup
// is visible rather than reported as one directory.
func TestInfosListsEveryRoot(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/tcount-a,/tmp/tcount-b")
	n := 0
	for _, info := range Infos() {
		if info.Agent == "claude" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("claude reported %d roots, want 2", n)
	}
}
