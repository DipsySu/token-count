package source

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"token-count/internal/cache"
	"token-count/internal/model"
)

// lowerLineLimit makes lines longer than n bytes overlong for the rest of the
// test, so that the limit can be exercised without writing 64 MiB.
func lowerLineLimit(t *testing.T, n int) {
	t.Helper()
	old := maxLineBytes
	maxLineBytes = n
	t.Cleanup(func() { maxLineBytes = old })
}

func readAll(t *testing.T, path string) ([]string, error) {
	t.Helper()
	var got []string
	err := forEachJSONL(path, func(line []byte) { got = append(got, string(line)) })
	return got, err
}

// One oversized line used to stop the scan for good, silently dropping every
// record after it. It is now skipped, the rest of the file is read, and the
// skip is reported.
func TestForEachJSONLSkipsOverlongLine(t *testing.T) {
	lowerLineLimit(t, 64)
	long := `{"text":"` + strings.Repeat("x", 100) + `"}`
	path := writeLines(t, filepath.Join(t.TempDir(), "log.jsonl"),
		`{"n":1}`, long, `{"n":3}`, long, `{"n":5}`)

	got, err := readAll(t, path)
	if strings.Join(got, " ") != `{"n":1} {"n":3} {"n":5}` {
		t.Fatalf("read %q, want every line but the long ones", got)
	}
	var skipped *longLinesError
	if !errors.As(err, &skipped) || skipped.count != 2 || skipped.first != 2 {
		t.Fatalf("error %v, want two skipped lines starting at line 2", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "line 2") || !strings.Contains(msg, "64 bytes") {
		t.Fatalf("message %q should name the first line and the limit", msg)
	}
}

// Lines longer than the reader's own buffer arrive in pieces: one under the
// limit must be reassembled intact, and one over it drained to its end without
// disturbing the next, including at the end of the file.
func TestForEachJSONLHandlesLinesLongerThanItsBuffer(t *testing.T) {
	// The reader's buffer is 64 KiB: the kept line takes two pieces, and the
	// dropped one passes the limit with three more still to drain.
	lowerLineLimit(t, 100<<10)
	kept := `{"text":"` + strings.Repeat("k", 80<<10) + `"}`
	dropped := `{"text":"` + strings.Repeat("d", 300<<10) + `"}`
	path := filepath.Join(t.TempDir(), "log.jsonl")
	data := kept + "\n" + dropped + "\n" + `{"n":3}` + "\n" + dropped // no final newline
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readAll(t, path)
	if len(got) != 2 || got[0] != kept || got[1] != `{"n":3}` {
		t.Fatalf("read %d lines, want the 80 KiB line intact and line 3", len(got))
	}
	var skipped *longLinesError
	if !errors.As(err, &skipped) || skipped.count != 2 || skipped.first != 2 {
		t.Fatalf("error %v, want lines 2 and 4 skipped", err)
	}
}

func TestForEachJSONLKeepsLinesUpToTheLimit(t *testing.T) {
	lowerLineLimit(t, 10)
	path := writeLines(t, filepath.Join(t.TempDir(), "log.jsonl"), "0123456789", "0123456789X")
	got, err := readAll(t, path)
	if len(got) != 1 || got[0] != "0123456789" {
		t.Fatalf("read %q, want only the line of exactly 10 bytes", got)
	}
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error %v, want line 2 reported", err)
	}
}

// An agent writes the newline after the record, so the last line of an active
// log can lack one. It still counts, and blank lines and padding are ignored.
func TestForEachJSONLParsesFinalLineWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte("\n  {\"n\":1}  \r\n\n{\"n\":2}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readAll(t, path)
	if err != nil || strings.Join(got, " ") != `{"n":1} {"n":2}` {
		t.Fatalf("read %q, %v", got, err)
	}

	a := codexTok(100, 0, 10)
	log := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := strings.Join([]string{codexMeta("s-1", "s-1"), codexTurn, tokenCount(1, a, a)}, "\n")
	if err := os.WriteFile(log, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if recs := parseFile(t, parseCodex, log); len(recs) != 1 {
		t.Fatalf("parsed %d records, want the unterminated last line", len(recs))
	}
}

// An agent that is still writing leaves its last line half done. That line
// is skipped quietly, as malformed lines always were: it is not an issue, and
// it will be whole on the next pass.
func TestHalfWrittenLastLineIsNotAnIssue(t *testing.T) {
	a := codexTok(100, 0, 10)
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := strings.Join([]string{codexMeta("s-1", "s-1"), codexTurn, tokenCount(1, a, a), `{"timestamp":"2026-08-25T01:00:09.000Z","type":"event_msg","payload":{"type":"tok`}, "\n")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := parseCodex(path)
	if len(recs) != 1 || err != nil {
		t.Fatalf("got %d records and %v, want 1 and no error", len(recs), err)
	}
}

func TestForEachJSONLReturnsReadErrors(t *testing.T) {
	dir := t.TempDir()
	called := false
	// Opening a directory succeeds; reading it fails.
	err := forEachJSONL(dir, func([]byte) { called = true })
	if err == nil || called {
		t.Fatalf("reading a directory gave %v (called %v), want a read error", err, called)
	}
	err = forEachJSONL(filepath.Join(dir, "missing.jsonl"), func([]byte) { called = true })
	if !errors.Is(err, fs.ErrNotExist) || called {
		t.Fatalf("missing file gave %v", err)
	}
}

// Gemini and OpenCode write one JSON document per file. One that cannot be
// read, or is malformed or cut short mid-write, is reported, and so is an
// empty one: it holds no document at all, which is what a file caught between
// being truncated and rewritten looks like. One of another shape holds nothing
// of ours to count and is not reported.
func TestWholeFileParsersReportBrokenFiles(t *testing.T) {
	gemini := `{"cwd":"/home/u/proj","messages":[{"timestamp":"2026-08-25T01:00:00.000Z","model":"gemini-3-pro","tokens":{"input":100,"output":10,"cached":40,"thoughts":5}}]}`
	opencode := `{"id":"msg_1","role":"assistant","modelID":"m-1","path":{"cwd":"/home/u/proj"},"tokens":{"input":100,"output":10,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1787627800000,"completed":1787627805000}}`
	cases := []struct {
		name    string
		parse   parser
		content string
		recs    int
		wantErr bool
	}{
		{"gemini", parseGemini, gemini, 1, false},
		{"gemini cut short", parseGemini, gemini[:len(gemini)/2], 0, true},
		{"gemini malformed", parseGemini, `{"messages":[}`, 0, true},
		{"gemini other shape", parseGemini, `{"messages":"none"}`, 0, false},
		{"gemini empty", parseGemini, "", 0, true},
		{"gemini blank", parseGemini, " \n", 0, true},
		{"opencode", parseOpenCode, opencode, 1, false},
		{"opencode cut short", parseOpenCode, opencode[:len(opencode)-1], 0, true},
		{"opencode empty", parseOpenCode, "", 0, true},
		{"opencode other shape", parseOpenCode, `["not","a","message"]`, 0, false},
		{"opencode user message", parseOpenCode, `{"id":"msg_2","role":"user"}`, 0, false},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "session.json")
		if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		recs, err := c.parse(path)
		if len(recs) != c.recs || (err != nil) != c.wantErr {
			t.Errorf("%s: %d records, error %v; want %d records, error %v", c.name, len(recs), err, c.recs, c.wantErr)
		}
	}
	// A read error is reported too.
	for name, parse := range map[string]parser{"gemini": parseGemini, "opencode": parseOpenCode} {
		if _, err := parse(t.TempDir()); err == nil {
			t.Errorf("%s: reading a directory gave no error", name)
		}
	}
}

// A parse that fails part way keeps what it read, and the error, and nothing
// is cached: a later pass must read the file again rather than take the part
// for the whole.
func TestParseCachedNeverCachesAFailedParse(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, filepath.Join(dir, "rollout.jsonl"), `{"n":1}`)
	c := cache.New(filepath.Join(dir, "index.gob"), true)
	partial := func(p string) ([]model.Usage, error) {
		recs, _ := lineParser(p)
		return recs, errors.New("read failed")
	}
	recs, err := parseCached(c, path, partial)
	if len(recs) != 1 || err == nil {
		t.Fatalf("got %d records and %v, want the partial record and the error", len(recs), err)
	}
	st, statErr := cache.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if _, ok := c.Get(path, st); ok {
		t.Fatal("a failed parse was cached")
	}
}

// A file the listing found can be gone by the time it is read, as when an
// agent prunes old sessions. There is nothing to count, and nothing failed.
func TestVanishedFileIsNotAnIssue(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(filepath.Join(dir, "index.gob"), true)
	missing := filepath.Join(dir, "gone.jsonl")
	for name, parse := range map[string]parser{
		"claude": parseClaude, "codex": parseCodex, "grok": parseGrok,
		"gemini": parseGemini, "opencode": parseOpenCode,
	} {
		for _, cc := range []*cache.Cache{c, nil} {
			if recs, err := parseCached(cc, missing, parse); len(recs) != 0 || err != nil {
				t.Errorf("%s (cache %v): got %d records, %v", name, cc != nil, len(recs), err)
			}
		}
	}

	// Gone between the cache's look at it and the parse.
	path := writeLines(t, filepath.Join(dir, "rollout.jsonl"), `{"n":1}`)
	vanishing := func(p string) ([]model.Usage, error) {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		return parseCodex(p)
	}
	if recs, err := parseCached(c, path, vanishing); len(recs) != 0 || err != nil {
		t.Fatalf("got %d records, %v", len(recs), err)
	}
}

// isolate points every agent at an empty directory, so that a collection pass
// in a test never reads the real logs, and returns those directories by
// environment variable.
func isolate(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	dirs := map[string]string{}
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GROK_HOME", "GEMINI_DIR", "OPENCODE_DATA"} {
		dir := filepath.Join(root, key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
		dirs[key] = dir
	}
	return dirs
}

func mkdir(t *testing.T, elem ...string) string {
	t.Helper()
	dir := filepath.Join(elem...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func longCodexLine() string {
	return `{"timestamp":"2026-08-25T01:00:02.000Z","type":"response_item","payload":{"type":"function_call_output","output":"` +
		strings.Repeat("x", 4096) + `"}}`
}

func TestCollectReportsOverlongLine(t *testing.T) {
	lowerLineLimit(t, 1024)
	dir := mkdir(t, isolate(t)["CODEX_HOME"], "sessions", "2026", "08", "25")
	a, b := codexTok(100, 0, 10), codexTok(200, 0, 20)
	path := writeLines(t, filepath.Join(dir, "rollout-2026-08-25T09-00-00-s-1.jsonl"),
		codexMeta("s-1", "s-1"), codexTurn,
		tokenCount(1, a, a),
		longCodexLine(), // line 4
		tokenCount(3, b, codexTok(300, 0, 30)),
	)
	c := cache.New(filepath.Join(t.TempDir(), "index.gob"), true)

	recs, stats := CollectStats([]string{"codex"}, c, nil)
	if total, calls := sumOf(recs); total != 330 || calls != 2 {
		t.Fatalf("got %d tokens over %d calls, want both sides of the long line: 330 over 2", total, calls)
	}
	if len(stats.Issues) != 1 {
		t.Fatalf("issues %+v, want one", stats.Issues)
	}
	if is := stats.Issues[0]; is.Agent != "codex" || is.Path != path || !strings.Contains(is.Err, "line 4") {
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

func TestCollectReportsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of their permissions")
	}
	dir := mkdir(t, isolate(t)["CODEX_HOME"], "sessions", "2026", "08", "25")
	a := codexTok(100, 0, 10)
	writeLines(t, filepath.Join(dir, "rollout-2026-08-25T09-00-00-good.jsonl"),
		codexMeta("good", "good"), codexTurn, tokenCount(1, a, a))
	bad := writeLines(t, filepath.Join(dir, "rollout-2026-08-25T09-00-00-bad.jsonl"),
		codexMeta("bad", "bad"), codexTurn, tokenCount(1, a, a))
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	c := cache.New(filepath.Join(t.TempDir(), "index.gob"), true)

	recs, stats := CollectStats([]string{"codex"}, c, nil)
	if len(recs) != 1 || len(stats.Issues) != 1 {
		t.Fatalf("got %d records and issues %+v, want the readable file and one issue", len(recs), stats.Issues)
	}
	is := stats.Issues[0]
	if is.Agent != "codex" || is.Path != bad || !strings.Contains(is.Err, "permission denied") {
		t.Fatalf("issue %+v", is)
	}
	if strings.Contains(is.Err, bad) {
		t.Fatalf("the error %q repeats the path, which the issue already carries", is.Err)
	}

	// The failed read was not cached as an empty file: once readable, it counts.
	if err := os.Chmod(bad, 0o644); err != nil {
		t.Fatal(err)
	}
	recs, stats = CollectStats([]string{"codex"}, c, nil)
	if len(recs) != 2 || len(stats.Issues) != 0 {
		t.Fatalf("readable again: %d records, issues %+v", len(recs), stats.Issues)
	}
}

// lock takes every permission from a directory until the test ends, when it
// gives them back so that the temporary tree can be removed.
func lock(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// A directory that cannot be listed hides every session below it, so it is
// reported just as an unreadable file is, rather than leaving totals that look
// complete. That includes the agent's own directory: a sessions directory that
// cannot even be looked at is not one that does not exist.
func TestCollectReportsUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads directories regardless of their permissions")
	}
	home := isolate(t)["CODEX_HOME"]
	sessions := filepath.Join(home, "sessions")
	a := codexTok(100, 0, 10)
	for _, day := range []string{"24", "25"} {
		dir := mkdir(t, sessions, "2026", "08", day)
		writeLines(t, filepath.Join(dir, "rollout-2026-08-"+day+"T09-00-00-s-"+day+".jsonl"),
			codexMeta("s-"+day, "s-"+day), codexTurn, tokenCount(1, a, a))
	}
	locked := filepath.Join(sessions, "2026", "08", "25")
	lock(t, locked)

	recs, stats := CollectStats([]string{"codex"}, nil, nil)
	if len(recs) != 1 || len(stats.Issues) != 1 {
		t.Fatalf("got %d records and issues %+v, want the readable day and one issue", len(recs), stats.Issues)
	}
	if is := stats.Issues[0]; is.Agent != "codex" || is.Path != locked || !strings.Contains(is.Err, "permission denied") {
		t.Fatalf("issue %+v", is)
	}

	lock(t, home)
	recs, stats = CollectStats([]string{"codex"}, nil, nil)
	if len(recs) != 0 || len(stats.Issues) != 1 || stats.Issues[0].Path != sessions {
		t.Fatalf("with the agent's directory locked: %d records, issues %+v; want %s reported", len(recs), stats.Issues, sessions)
	}
}

// bytesAllocated reports how many bytes of heap fn allocates.
func bytesAllocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A line longer than the reader's buffer is assembled in one that doubles as
// it fills, as bufio.Scanner's did. Left to append, which grows a large slice
// by a quarter at a time, the copies discarded on the way came to about five
// times the line, or the limit for one that is dropped.
func TestForEachJSONLAssemblesLongLinesCheaply(t *testing.T) {
	// Just under 4 MiB: doubling from the reader's 64 KiB ends at 4 MiB,
	// having allocated twice the line in all.
	line := strings.Repeat("x", 4<<20-4<<10)
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var n int
	var err error
	if got := bytesAllocated(func() {
		err = forEachJSONL(path, func(l []byte) {
			if string(l) == line {
				n++
			}
		})
	}); err != nil || n != 1 || got > 3*uint64(len(line)) {
		t.Fatalf("read the line %d times (%v), allocating %d bytes; want once, under three times its %d", n, err, got, len(line))
	}

	// A dropped line costs no more than the limit does.
	limit := 1 << 20
	lowerLineLimit(t, limit)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 3*limit)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := bytesAllocated(func() { err = forEachJSONL(path, func([]byte) {}) }); err == nil || got > 3*uint64(limit) {
		t.Fatalf("dropping a line three times the limit allocated %d bytes (%v); want under three times the limit", got, err)
	}

	// Doubling stops at the limit rather than overshoot one that is not a
	// power of two, and what was assembled of a dropped line is let go
	// rather than held for the rest of the file.
	limit = 100 << 10
	lowerLineLimit(t, limit)
	lr := lineReader{r: bufio.NewReaderSize(strings.NewReader(
		strings.Repeat("x", limit)+"\n"+strings.Repeat("x", limit+1)+"\n"), 64<<10)}
	if l, tooLong, err := lr.next(); len(l) != limit || tooLong || err != nil || cap(lr.buf) > limit {
		t.Fatalf("line at the limit: %d bytes, too long %v, error %v, buffer of %d", len(l), tooLong, err, cap(lr.buf))
	}
	if _, tooLong, err := lr.next(); !tooLong || err != nil || cap(lr.buf) != 0 {
		t.Fatalf("after the dropped line: too long %v, error %v, %d bytes still held", tooLong, err, cap(lr.buf))
	}
}

func TestCollectCleanFilesHaveNoIssues(t *testing.T) {
	env := isolate(t)
	dir := mkdir(t, env["CODEX_HOME"], "sessions", "2026", "08", "25")
	a := codexTok(100, 0, 10)
	writeLines(t, filepath.Join(dir, "rollout-2026-08-25T09-00-00-s-1.jsonl"),
		codexMeta("s-1", "s-1"), codexTurn, tokenCount(1, a, a))
	data, err := os.ReadFile(filepath.Join("testdata", "claude.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	project := mkdir(t, env["CLAUDE_CONFIG_DIR"], "projects", "-home-u-proj")
	if err := os.WriteFile(filepath.Join(project, "session.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Every agent, including those with no logs at all.
	recs, stats := CollectStats(model.Agents, cache.New("", false), nil)
	if len(recs) != 2 || len(stats.Issues) != 0 {
		t.Fatalf("got %d records and issues %+v, want 2 and none", len(recs), stats.Issues)
	}
}

func TestSortIssues(t *testing.T) {
	// Path order disagrees with agent order, so only sorting by agent first
	// gets this right.
	issues := []Issue{
		{Agent: "codex", Path: "/a/codex/2.jsonl"},
		{Agent: "claude", Path: "/z/claude/b.jsonl"},
		{Agent: "codex", Path: "/a/codex/1.jsonl"},
		{Agent: "claude", Path: "/z/claude/a.jsonl"},
	}
	sortIssues(issues)
	var got []string
	for _, is := range issues {
		got = append(got, is.Path)
	}
	if want := "/z/claude/a.jsonl /z/claude/b.jsonl /a/codex/1.jsonl /a/codex/2.jsonl"; strings.Join(got, " ") != want {
		t.Fatalf("order %v, want %s", got, want)
	}
}

// Files are parsed in parallel, so the order of the issues comes from sorting
// alone. The roots are named so that path order disagrees with agent order.
func TestCollectSortsIssues(t *testing.T) {
	lowerLineLimit(t, 1024)
	isolate(t)
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "z"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "a"))
	long := `{"type":"user","message":{"content":"` + strings.Repeat("x", 4096) + `"}}`
	project := mkdir(t, root, "z", "projects", "-home-u-proj")
	var want []Issue
	for _, name := range []string{"a.jsonl", "b.jsonl"} {
		want = append(want, Issue{Agent: "claude", Path: writeLines(t, filepath.Join(project, name), long)})
	}
	sessions := mkdir(t, root, "a", "sessions", "2026", "08", "25")
	want = append(want, Issue{Agent: "codex", Path: writeLines(t, filepath.Join(sessions, "rollout-c.jsonl"), longCodexLine())})

	for i := 0; i < 5; i++ {
		_, stats := CollectStats([]string{"codex", "claude"}, nil, nil)
		if len(stats.Issues) != len(want) {
			t.Fatalf("issues %+v, want %d", stats.Issues, len(want))
		}
		for j, is := range stats.Issues {
			if is.Agent != want[j].Agent || is.Path != want[j].Path {
				t.Fatalf("issue %d is %s %s, want %s %s", j, is.Agent, is.Path, want[j].Agent, want[j].Path)
			}
		}
	}
}

// The progress callback still hears from every agent once. Agents are
// collected in parallel, so it is called concurrently.
func TestCollectStillReportsProgress(t *testing.T) {
	isolate(t)
	var mu sync.Mutex
	seen := map[string]int{}
	CollectStats(model.Agents, nil, func(agent string, files, records int) {
		mu.Lock()
		seen[agent]++
		mu.Unlock()
	})
	for _, a := range model.Agents {
		if seen[a] != 1 {
			t.Fatalf("progress saw %v", seen)
		}
	}
}
