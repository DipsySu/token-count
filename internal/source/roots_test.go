package source

import (
	"errors"
	"io/fs"
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
	list := func(roots ...string) []string {
		t.Helper()
		files, unread := listFiles(roots, match)
		if len(unread) != 0 {
			t.Errorf("roots %v: unreadable %+v", roots, unread)
		}
		return files
	}

	if got := list(root); len(got) != 2 {
		t.Fatalf("single root found %d files", len(got))
	}
	// The same root twice, and a root nested inside another, both collapse.
	if got := list(root, root); len(got) != 2 {
		t.Errorf("repeated root found %d files, want 2", len(got))
	}
	if got := list(root, nested); len(got) != 2 {
		t.Errorf("nested roots found %d files, want 2", len(got))
	}
	// A missing root is skipped rather than failing the whole scan, and is
	// not reported: there is nothing in it to miss.
	if got := list(filepath.Join(root, "nope"), root); len(got) != 2 {
		t.Errorf("missing root broke the scan: %d files", len(got))
	}
}

// A directory that cannot be listed is reported once, however many of the
// roots reach it, and the walk carries on past it.
func TestListFilesReportsUnreadableDirectoryOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads directories regardless of their permissions")
	}
	root := t.TempDir()
	nested := filepath.Join(root, "a")
	for _, dir := range []string{"b", "c"} {
		if err := os.MkdirAll(filepath.Join(nested, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, dir, "s.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(nested, "b")
	lock(t, locked)

	files, unread := listFiles([]string{root, nested, root}, suffix(".jsonl"))
	if len(files) != 1 || files[0] != filepath.Join(nested, "c", "s.jsonl") {
		t.Errorf("found %v, want only the readable file", files)
	}
	if len(unread) != 1 || unread[0].path != locked || !errors.Is(unread[0].err, fs.ErrPermission) {
		t.Errorf("unreadable %+v, want %s once", unread, locked)
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

// A root that is itself a symlink, as a dotfiles manager or a moved home
// directory leaves one, is walked through to its target. WalkDir does not
// follow a symlinked root, so it used to yield no files at all and no issue.
// The files are named under the root as given, and a root reached both
// through the link and directly is read once.
func TestListFilesFollowsSymlinkedRoot(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(filepath.Join(real, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "p", "s.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here:", err)
	}

	files, unread := listFiles([]string{link}, suffix(".jsonl"))
	if want := filepath.Join(link, "p", "s.jsonl"); len(files) != 1 || files[0] != want || len(unread) != 0 {
		t.Errorf("found %v (unreadable %+v), want %s", files, unread, want)
	}
	files, _ = listFiles([]string{link, real}, suffix(".jsonl"))
	if len(files) != 1 {
		t.Errorf("found %v through the link and directly, want the file once", files)
	}
}
