package cache

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"
	"time"

	"token-count/internal/model"
)

func record(in int64) model.Usage {
	u, _ := model.Make(model.MakeOpts{
		Agent: "codex",
		Time:  time.Date(2026, 8, 25, 12, 0, 0, 0, time.Local),
		Input: in,
		Dedup: "codex|s|1",
	})
	return u
}

// Records are served only for the state they were parsed from: a file that
// has since grown, or been rewritten, is parsed again.
func TestGetRequiresTheStoredState(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "index.gob"), true)
	st := FileState{ModNano: 1_000, Size: 50}
	c.Put("/logs/a.jsonl", st, []model.Usage{record(10)})

	if got, ok := c.Get("/logs/a.jsonl", st); !ok || len(got) != 1 || got[0].Input != 10 {
		t.Fatalf("same state: got %v, %v", got, ok)
	}
	for _, now := range []FileState{
		{ModNano: 1_000, Size: 80}, // appended
		{ModNano: 2_000, Size: 50}, // rewritten in place
	} {
		if _, ok := c.Get("/logs/a.jsonl", now); ok {
			t.Errorf("state %+v was served the entry stored for %+v", now, st)
		}
	}
	if _, ok := c.Get("/logs/b.jsonl", st); ok {
		t.Error("another path was served the entry")
	}
}

func TestStatReadsSizeAndModTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := time.Date(2026, 8, 25, 12, 0, 0, 123, time.UTC)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	st, err := Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size != 3 || st.ModNano != mod.UnixNano() {
		t.Fatalf("got %+v, want size 3 and mtime %d", st, mod.UnixNano())
	}
	if _, err := Stat(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing file has no state")
	}
}

func TestEntriesSurviveSaveAndLoad(t *testing.T) {
	index := filepath.Join(t.TempDir(), "sub", "index.gob")
	st := FileState{ModNano: 1_000, Size: 50}
	c := New(index, true)
	c.Put("/logs/a.jsonl", st, []model.Usage{record(10), record(20)})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, ok := New(index, true).Get("/logs/a.jsonl", st)
	if !ok || len(got) != 2 || got[0].Input != 10 || got[1].Input != 20 || got[0].Dedup != "codex|s|1" {
		t.Fatalf("after reload: got %+v, %v", got, ok)
	}
}

// --no-cache must neither read nor write the index.
func TestDisabledCacheStoresNothing(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.gob")
	st := FileState{ModNano: 1_000, Size: 50}
	c := New(index, false)
	c.Put("/logs/a.jsonl", st, []model.Usage{record(10)})
	if _, ok := c.Get("/logs/a.jsonl", st); ok {
		t.Fatal("a disabled cache served an entry")
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatalf("a disabled cache wrote %s (stat error %v)", index, err)
	}
}

// An index written by another version holds records whose meaning has
// changed, so it is ignored even where its entries would still match.
func TestLoadIgnoresOtherVersions(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.gob")
	st := FileState{ModNano: 1_000, Size: 50}
	f, err := os.Create(index)
	if err != nil {
		t.Fatal(err)
	}
	old := file{Version: version - 1, Files: map[string]entry{
		"/logs/a.jsonl": {State: st, Records: []model.Usage{record(10)}},
	}}
	if err := gob.NewEncoder(f).Encode(old); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got, ok := New(index, true).Get("/logs/a.jsonl", st); ok {
		t.Fatalf("served %+v from a version %d index", got, version-1)
	}
}

// The file name carries the version, so binaries of different versions keep
// separate indexes rather than discarding each other's. v6 changed what a
// Codex or Claude log's entry holds.
func TestDefaultPathNamesTheVersion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	if got, want := DefaultPath(), filepath.Join(root, "token-count", "index-v7.gob"); got != want {
		t.Fatalf("DefaultPath() = %s, want %s", got, want)
	}
}
