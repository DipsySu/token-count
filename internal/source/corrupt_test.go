package source

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"token-count/internal/cache"
)

// A crash can leave a log with a stretch of NUL bytes where data was due to
// be written: a line of nothing but NULs, or NULs glued in front of the next
// record. The line held usage that is now missing, so it is an issue like an
// overlong line; it used to be skipped as silently as a half-written final
// line, and the totals claimed to be complete.
func TestCollectReportsNULLine(t *testing.T) {
	dir := mkdir(t, isolate(t)["CODEX_HOME"], "sessions", "2026", "08", "25")
	a, b := codexTok(100, 0, 10), codexTok(200, 0, 20)
	path := writeLines(t, filepath.Join(dir, "rollout-2026-08-25T09-00-00-s-1.jsonl"),
		codexMeta("s-1", "s-1"), codexTurn,
		tokenCount(1, a, a),
		strings.Repeat("\x00", 3000), // line 4
		`{"broken`,                   // line 5
		tokenCount(3, b, codexTok(300, 0, 30)),
	)
	c := cache.New(filepath.Join(t.TempDir(), "index.gob"), true)

	recs, stats := CollectStats([]string{"codex"}, c, nil)
	if total, calls := sumOf(recs); total != 330 || calls != 2 {
		t.Fatalf("got %d tokens over %d calls, want both sides of the corrupt lines: 330 over 2", total, calls)
	}
	if len(stats.Issues) != 1 {
		t.Fatalf("issues %+v, want one", stats.Issues)
	}
	if is := stats.Issues[0]; is.Agent != "codex" || is.Path != path || is.Err != "2 corrupt lines were not counted, the first at line 4" {
		t.Fatalf("issue %+v", is)
	}
	st, err := cache.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(path, st); ok {
		t.Fatal("the partly read file was cached")
	}
}

// NULs glued in front of a whole record are what is left of a crash before
// it, and the record itself is intact: it counts, and nothing is missing.
func TestNULPrefixedRecordCounts(t *testing.T) {
	a, b := codexTok(100, 0, 10), codexTok(200, 0, 20)
	path := writeLines(t, filepath.Join(t.TempDir(), "rollout.jsonl"),
		codexMeta("s-1", "s-1"), codexTurn,
		tokenCount(1, a, a),
		strings.Repeat("\x00", 500)+tokenCount(3, b, codexTok(300, 0, 30)),
	)
	recs, err := parseCodex(path)
	if err != nil {
		t.Fatalf("error %v, want none", err)
	}
	if total, calls := sumOf(recs); total != 330 || calls != 2 {
		t.Fatalf("got %d tokens over %d calls, want 330 over 2", total, calls)
	}
}

// The final line of a log still being written can be garbage of any kind,
// NULs included: it is not an issue, and it will be whole on the next pass.
// Blank lines after it do not make it any less the last.
func TestGarbageFinalLineIsNotAnIssue(t *testing.T) {
	a := codexTok(100, 0, 10)
	for _, last := range []string{`{"timestamp":"2026-08-25T01:00:09.000Z","ty`, strings.Repeat("\x00", 64), "\x00\x00{\"ty", "garbage\n\n  \n"} {
		path := writeLines(t, filepath.Join(t.TempDir(), "rollout.jsonl"),
			codexMeta("s-1", "s-1"), codexTurn, tokenCount(1, a, a), last)
		recs, err := parseCodex(path)
		if len(recs) != 1 || err != nil {
			t.Errorf("last line %q: got %d records and %v, want 1 and no error", last, len(recs), err)
		}
	}
}

// A line that is well-formed JSON of a shape the parser does not expect is
// not ours to count, and not corrupt: reporting it would warn on every run
// about a log nothing is wrong with.
func TestUnexpectedShapeIsNotCorrupt(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "claude.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	odd := `{"type":"assistant","timestamp":12}` + "\n" + `[1,2]` + "\n" + `"text"` + "\n"
	if err := os.WriteFile(path, append([]byte(odd), data...), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := parseClaude(path)
	if err != nil || len(recs) != 1 {
		t.Fatalf("got %d records and %v, want 1 and no error", len(recs), err)
	}
}

// Overlong and corrupt lines in one file are both reported.
func TestForEachJSONLReportsOverlongAndCorruptLines(t *testing.T) {
	lowerLineLimit(t, 64)
	path := writeLines(t, filepath.Join(t.TempDir(), "log.jsonl"),
		`{"n":1}`, strings.Repeat("x", 100), "\x00\x00\x00", `{"n":4}`)
	var got []string
	err := forEachJSONL(path, func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if strings.Join(got, " ") != `{"n":1} {"n":4}` {
		t.Fatalf("read %q", got)
	}
	var long *longLinesError
	var bad *corruptLinesError
	if !errors.As(err, &long) || !errors.As(err, &bad) || long.first != 2 || bad.first != 3 || bad.count != 1 {
		t.Fatalf("error %v, want line 2 overlong and line 3 corrupt", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "line 3 is corrupt") || strings.Contains(msg, "\n") {
		t.Fatalf("message %q should name line 3 on one line", msg)
	}
}
