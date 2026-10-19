package source

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"token-count/internal/model"
)

func usage(agent, dedup string, in int64) model.Usage {
	u, _ := model.Make(model.MakeOpts{
		Agent: agent,
		Time:  time.Date(2026, 8, 25, 12, 0, 0, 0, time.Local),
		Input: in,
		Dedup: dedup,
	})
	return u
}

// Claude Code replays earlier messages into every resumed or forked session
// log. Counting those repeats inflated its totals roughly threefold.
func TestDedupDropsReplayedRecords(t *testing.T) {
	recs := []model.Usage{
		usage("claude", "claude|msg_1|req_A", 100),
		usage("claude", "claude|msg_1|req_A", 100),
		usage("claude", "claude|msg_1|req_A", 100),
		usage("claude", "claude|msg_2|req_B", 7),
	}
	var stats Stats
	got := dedup(recs, &stats)
	if len(got) != 2 {
		t.Fatalf("kept %d records, want 2", len(got))
	}
	if stats.Dropped != 2 || stats.DroppedTokens != 200 {
		t.Fatalf("stats %+v", stats)
	}
	var total int64
	for _, r := range got {
		total += r.Total()
	}
	if total != 107 {
		t.Fatalf("total %d, want 107", total)
	}
}

// Records from agents that expose no id to match on must survive untouched;
// dropping them would silently lose real usage.
func TestDedupKeepsUnkeyedRecords(t *testing.T) {
	recs := []model.Usage{
		usage("codex", "", 10),
		usage("codex", "", 10),
		usage("codex", "", 10),
	}
	var stats Stats
	got := dedup(recs, &stats)
	if len(got) != 3 {
		t.Fatalf("kept %d, want 3", len(got))
	}
	if stats.Dropped != 0 {
		t.Fatalf("dropped %d", stats.Dropped)
	}
}

func TestDedupIsPerAgent(t *testing.T) {
	// Two agents could in principle mint the same raw id; the key is prefixed
	// so they never collide.
	recs := []model.Usage{
		usage("claude", "claude|x", 5),
		usage("opencode", "opencode|x", 5),
	}
	if got := dedup(recs, nil); len(got) != 2 {
		t.Fatalf("kept %d, want 2", len(got))
	}
}

func TestParseClaudeDeduplicatesAndKeepsProject(t *testing.T) {
	recs := parseClaude(filepath.Join("testdata", "claude_dupes.jsonl"))
	if len(recs) != 3 {
		t.Fatalf("parser should emit every line: got %d", len(recs))
	}
	for _, r := range recs {
		if r.Project != "/home/u/proj" {
			t.Fatalf("project not captured: %+v", r)
		}
		if r.Dedup == "" {
			t.Fatalf("missing dedup key: %+v", r)
		}
	}
	kept := dedup(recs, nil)
	if len(kept) != 2 {
		t.Fatalf("after dedup got %d, want 2", len(kept))
	}
	var total int64
	for _, r := range kept {
		total += r.Total()
	}
	if total != (2+100+10+20)+(1+50+0+5) {
		t.Fatalf("total %d", total)
	}
}

func TestClaudeKeyFallbacks(t *testing.T) {
	const p = "/home/u/.claude/projects/proj/sess.jsonl"
	if got := claudeKey("m", "r", p, 1); got != "claude|m|r" {
		t.Fatalf("%s", got)
	}
	if got := claudeKey("m", "", p, 1); got != "claude|m" {
		t.Fatalf("%s", got)
	}
	if got := claudeKey("", "r", p, 1); got != "claude|req|r" {
		t.Fatalf("%s", got)
	}

	// With no id at all the key comes from the location. It must still be a
	// key, or an old-format record double-counts when two roots hold it.
	noID := claudeKey("", "", p, 1)
	if noID == "" {
		t.Fatal("a record with no id still needs a key")
	}
	// The same record reached through another root keys identically...
	other := claudeKey("", "", "/mnt/backup/.claude/projects/proj/sess.jsonl", 1)
	if noID != other {
		t.Fatalf("copy keyed differently:\n  %s\n  %s", noID, other)
	}
	// ...while a different project, file or position does not.
	for _, diff := range []struct {
		path string
		seq  int
	}{
		{"/home/u/.claude/projects/OTHER/sess.jsonl", 1},
		{"/home/u/.claude/projects/proj/other.jsonl", 1},
		{p, 2},
	} {
		if got := claudeKey("", "", diff.path, diff.seq); got == noID {
			t.Fatalf("distinct record %v collided with %s", diff, noID)
		}
	}
	// A location-derived key must never be mistaken for an id-derived one.
	if noID == claudeKey("proj/sess.jsonl", "", p, 1) {
		t.Fatal("fallback key collided with the id namespace")
	}
}

// usageOut builds a record whose output count differs, to model the snapshots
// Claude Code writes while a response streams.
func usageOut(dedup string, cacheRead, out int64) model.Usage {
	u, _ := model.Make(model.MakeOpts{
		Agent:                   "claude",
		Time:                    time.Date(2026, 8, 25, 12, 0, 0, 0, time.Local),
		CacheRead:               cacheRead,
		Output:                  out,
		OutputIncludesReasoning: true,
		Dedup:                   dedup,
	})
	return u
}

// Claude Code logs a streaming response several times: the early snapshots
// carry a partial output count and the last carries the real one. Keeping the
// first-seen record threw away most of the output tokens.
func TestDedupKeepsTheCompleteSnapshot(t *testing.T) {
	const key = "claude|msg_1|req_A"
	recs := []model.Usage{
		usageOut(key, 11_000, 1),   // first snapshot: output barely started
		usageOut(key, 11_000, 1),   // replayed into another session file
		usageOut(key, 11_000, 363), // the finished response
	}
	var stats Stats
	got := dedup(recs, &stats)
	if len(got) != 1 {
		t.Fatalf("kept %d records, want 1", len(got))
	}
	if got[0].Output != 363 {
		t.Fatalf("kept output %d, want the complete 363", got[0].Output)
	}
	if stats.Dropped != 2 {
		t.Fatalf("dropped %d, want 2", stats.Dropped)
	}
}

// Selection must not depend on the order files happen to be scanned in, since
// collection runs in parallel.
func TestDedupIsOrderIndependent(t *testing.T) {
	const key = "claude|msg_1|req_A"
	small := usageOut(key, 11_000, 1)
	mid := usageOut(key, 11_000, 40)
	full := usageOut(key, 11_000, 363)

	for _, order := range [][]model.Usage{
		{small, mid, full},
		{full, mid, small},
		{mid, full, small},
		{full, small, mid},
	} {
		got := dedup(order, nil)
		if len(got) != 1 || got[0].Output != 363 {
			t.Fatalf("order %v: kept %+v", outputs(order), got)
		}
	}
}

func outputs(recs []model.Usage) []int64 {
	out := make([]int64, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Output)
	}
	return out
}

// The surviving record keeps its position, so a stable input order gives a
// stable output order.
func TestDedupPreservesPosition(t *testing.T) {
	recs := []model.Usage{
		usageOut("a", 100, 5),
		usageOut("b", 100, 5),
		usageOut("a", 100, 900), // supersedes the first record in place
		usageOut("c", 100, 5),
	}
	got := dedup(recs, nil)
	if len(got) != 3 {
		t.Fatalf("kept %d, want 3", len(got))
	}
	if got[0].Dedup != "a" || got[1].Dedup != "b" || got[2].Dedup != "c" {
		t.Fatalf("order changed: %s %s %s", got[0].Dedup, got[1].Dedup, got[2].Dedup)
	}
	if got[0].Output != 900 {
		t.Fatalf("position 0 kept output %d, want 900", got[0].Output)
	}
}

// Every agent must produce deduplication keys, or scanning two roots that hold
// the same session counts it twice. Codex in particular has no per-response id,
// which made overlapping roots silently double the totals.
func TestEveryParserEmitsDedupKeys(t *testing.T) {
	cases := []struct {
		name  string
		parse func(string) []model.Usage
		file  string
	}{
		{"claude", parseClaude, "claude.jsonl"},
		{"claude-dupes", parseClaude, "claude_dupes.jsonl"},
		{"codex", parseCodex, "codex.jsonl"},
		{"grok", parseGrok, "grok_updates.jsonl"},
	}
	for _, c := range cases {
		recs := c.parse(filepath.Join("testdata", c.file))
		if len(recs) == 0 {
			t.Fatalf("%s: parsed nothing", c.name)
		}
		seen := map[string]bool{}
		for i, r := range recs {
			if r.Dedup == "" {
				t.Errorf("%s record %d has no dedup key: %+v", c.name, i, r)
				continue
			}
			// Within one file the keys must be distinct, or real records
			// collapse into each other.
			if seen[r.Dedup] && c.name != "claude-dupes" {
				t.Errorf("%s: duplicate key %q for distinct records", c.name, r.Dedup)
			}
			seen[r.Dedup] = true
		}
	}
}

// Parsing the same session from two roots must yield the same keys, which is
// what makes a migrated snapshot or an overlapping root safe to scan.
//
// Agents that expose an id key on that id and match wherever the file sits.
// Claude records predating message ids, and Gemini, have nothing to key on but
// their position, so they match only when the copy preserves the directory
// structure -- which a snapshot of a whole config tree does.
func TestKeysAreStableAcrossRoots(t *testing.T) {
	cases := []struct {
		name  string
		parse func(string) []model.Usage
		file  string
		rel   string // where the file sits inside its root
	}{
		{"codex", parseCodex, "codex.jsonl", "sessions/2026/08/25"},
		{"claude", parseClaude, "claude.jsonl", "projects/-home-u-proj"},
		{"claude-dupes", parseClaude, "claude_dupes.jsonl", "projects/-home-u-proj"},
	}
	for _, c := range cases {
		data, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		// Two roots holding the same tree, as a live directory and a migrated
		// snapshot of it would.
		var parsed [][]model.Usage
		for _, root := range []string{t.TempDir(), t.TempDir()} {
			dir := filepath.Join(root, c.rel)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(dir, c.file)
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				t.Fatal(err)
			}
			parsed = append(parsed, c.parse(dst))
		}
		a, b := parsed[0], parsed[1]
		if len(a) == 0 {
			t.Fatalf("%s: parsed nothing", c.name)
		}
		if len(a) != len(b) {
			t.Fatalf("%s: %d vs %d records", c.name, len(a), len(b))
		}
		for i := range a {
			if a[i].Dedup == "" {
				t.Fatalf("%s record %d has no key", c.name, i)
			}
			if a[i].Dedup != b[i].Dedup {
				t.Errorf("%s record %d: %q in one root, %q in the other",
					c.name, i, a[i].Dedup, b[i].Dedup)
			}
		}
		// Scanning both roots must total the same as scanning one.
		merged := dedup(append(append([]model.Usage{}, a...), b...), nil)
		want := len(dedup(append([]model.Usage{}, a...), nil))
		if len(merged) != want {
			t.Errorf("%s: two roots gave %d records, one root gives %d", c.name, len(merged), want)
		}
		var one, two int64
		for _, r := range dedup(append([]model.Usage{}, a...), nil) {
			one += r.Total()
		}
		for _, r := range merged {
			two += r.Total()
		}
		if one != two {
			t.Errorf("%s: two roots totalled %d, one root totals %d", c.name, two, one)
		}
	}
}
