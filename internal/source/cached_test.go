package source

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"token-count/internal/cache"
	"token-count/internal/model"
)

// lineParser emits one record per line, which is all the cache tests need
// from a parser.
func lineParser(path string) ([]model.Usage, error) {
	var out []model.Usage
	n := 0
	err := forEachJSONL(path, func([]byte) {
		n++
		u, _ := model.Make(model.MakeOpts{
			Agent: "codex",
			Time:  time.Date(2026, 8, 25, 12, 0, 0, 0, time.Local),
			Input: 10,
			Dedup: seqKey("codex", "test", n),
		})
		out = append(out, u)
	})
	return out, err
}

// cached runs parseCached and fails the test if it reports an error.
func cached(t *testing.T, c *cache.Cache, path string, parse parser) []model.Usage {
	t.Helper()
	recs, err := parseCached(c, path, parse)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return recs
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// An agent appends to its log while a pass is parsing it. The records read
// before the append must not be cached against the grown file, or every later
// pass serves the stale result until the file changes again.
func TestParseCachedSkipsFileChangedMidParse(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, filepath.Join(dir, "rollout.jsonl"), `{"n":1}`)
	c := cache.New(filepath.Join(dir, "index.gob"), true)

	appending := func(p string) ([]model.Usage, error) {
		recs, err := lineParser(p)
		appendLine(t, p, `{"n":2}`) // written after the parser read the file
		return recs, err
	}
	before, err := cache.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cached(t, c, path, appending); len(got) != 1 {
		t.Fatalf("first pass parsed %d records, want 1", len(got))
	}
	now, err := cache.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is cached for the file, under either state.
	for _, st := range []cache.FileState{now, before} {
		if got, ok := c.Get(path, st); ok {
			t.Fatalf("state %+v is served %d records read while the file changed", st, len(got))
		}
	}
	if got := cached(t, c, path, lineParser); len(got) != 2 {
		t.Fatalf("second pass got %d records, want 2: the first pass cached a stale result", len(got))
	}
}

// A file that has not changed is parsed once and then served from the cache,
// in this process and the next one.
func TestParseCachedServesUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, filepath.Join(dir, "rollout.jsonl"), `{"n":1}`)
	index := filepath.Join(dir, "index.gob")
	calls := 0
	counting := func(p string) ([]model.Usage, error) {
		calls++
		return lineParser(p)
	}

	c := cache.New(index, true)
	for pass := 1; pass <= 2; pass++ {
		if got := cached(t, c, path, counting); len(got) != 1 {
			t.Fatalf("pass %d got %d records, want 1", pass, len(got))
		}
	}
	if calls != 1 {
		t.Fatalf("parsed %d times, want once", calls)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if got := cached(t, cache.New(index, true), path, counting); len(got) != 1 || calls != 1 {
		t.Fatalf("reloaded cache: %d records after %d parses, want 1 after 1", len(got), calls)
	}

	// Once the file grows it is parsed again.
	appendLine(t, path, `{"n":2}`)
	if got := cached(t, c, path, counting); len(got) != 2 || calls != 2 {
		t.Fatalf("grown file: %d records after %d parses, want 2 after 2", len(got), calls)
	}
}
