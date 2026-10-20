package source

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
	recs := parseFile(t, parseClaude, filepath.Join("testdata", "claude_dupes.jsonl"))
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

// dedup returns the survivors in time order, then by agent and key, whatever
// order the records came in. Collection runs in parallel, and the order is
// visible downstream: buckets that tie on size and label are listed in the
// order their first record arrived.
func TestDedupReturnsRecordsInTimeOrder(t *testing.T) {
	at := func(r model.Usage, min int) model.Usage {
		r.Time = time.Date(2026, 8, 25, 12, min, 0, 0, time.Local)
		return r
	}
	recs := []model.Usage{
		at(usageOut("claude|c", 100, 5), 3),
		at(usageOut("claude|b", 100, 5), 1),
		at(usageOut("claude|a", 100, 5), 1),
		at(usageOut("claude|a", 100, 900), 2), // the complete snapshot, logged later
		at(usage("codex", "codex|a", 7), 1),
		at(usage("claude", "", 9), 0),
	}
	got := dedup(recs, nil)
	var order []string
	for _, r := range got {
		order = append(order, r.Time.Format("15:04")+" "+r.Dedup)
	}
	if want := "12:00  12:01 claude|b 12:01 codex|a 12:02 claude|a 12:03 claude|c"; strings.Join(order, " ") != want {
		t.Fatalf("order %q, want %q", strings.Join(order, " "), want)
	}
}

// tied is a Claude record of a fixed size, so that records made from it tie
// on size and differ only where a test changes them.
func tied(key string) model.Usage {
	return model.Usage{
		Agent:     "claude",
		Time:      time.Date(2026, 8, 25, 12, 0, 0, 0, time.Local),
		Model:     "claude-fable-5",
		Project:   "/home/u/proj",
		Input:     10,
		CacheRead: 90,
		Output:    5,
		Calls:     1,
		Dedup:     key,
		Source:    "/home/u/.claude/projects/-home-u-proj/s-1.jsonl",
	}
}

// with returns r changed by f.
func with(r model.Usage, f func(*model.Usage)) model.Usage {
	f(&r)
	return r
}

// Copies of one response that tie on size can differ in everything else, and
// the one that survives decides where the response is counted: its project,
// model and day. So the choice must follow from the copies alone, whatever
// order they arrive in. A copy that Claude Code made in another session's log
// loses to the original, which says where the call was made; otherwise the
// copy logged first wins, and after that the first in a fixed order of the
// remaining fields.
func TestDedupBreaksTiesByContent(t *testing.T) {
	const key = "claude|msg_1|req_A"
	base := tied(key)
	cases := []struct {
		name       string
		want, lose model.Usage
	}{
		{"time", base, with(base, func(r *model.Usage) { r.Time = r.Time.Add(5 * time.Second) })},
		{"source", base, with(base, func(r *model.Usage) { r.Source = strings.Replace(r.Source, "s-1", "s-2", 1) })},
		{"project", base, with(base, func(r *model.Usage) { r.Project += "/sub" })},
		{"model", base, with(base, func(r *model.Usage) { r.Model = "claude-opus-5-5" })},
		{"calls", base, with(base, func(r *model.Usage) { r.Calls = 2 })},
		{"split", base, with(base, func(r *model.Usage) { r.Input, r.CacheRead = 20, 80 })},
		// The copy is earlier and comes first in every other field, and
		// still loses.
		{"copy", with(base, func(r *model.Usage) {
			r.Time = r.Time.Add(time.Second)
			r.Source = strings.Replace(r.Source, "s-1", "s-2", 1)
			r.Project += "/sub"
		}), with(base, func(r *model.Usage) { r.Dedup = copyMark + key })},
	}
	for _, c := range cases {
		want := c.want
		want.Dedup = key // survivors carry the plain key
		for _, recs := range [][]model.Usage{{c.want, c.lose}, {c.lose, c.want}} {
			var stats Stats
			got := dedup(recs, &stats)
			if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
				t.Errorf("%s: kept %+v\nwant %+v", c.name, got, want)
			}
			if stats.Dropped != 1 || stats.DroppedTokens != c.lose.Total() {
				t.Errorf("%s: stats %+v", c.name, stats)
			}
		}
	}
}

// Shuffling the records changes nothing in what dedup returns: the same
// survivors, identical in every field, in the same order, and the same count
// of what was dropped.
func TestDedupIgnoresInputOrder(t *testing.T) {
	var pool []model.Usage
	for i := range 20 {
		r := tied(fmt.Sprintf("claude|msg_%d|req_%d", i, i))
		r.Time = r.Time.Add(time.Duration(i%7) * time.Minute) // some share a time
		pool = append(pool, r,
			with(r, func(r *model.Usage) { r.Time = r.Time.Add(3 * time.Second) }),
			with(r, func(r *model.Usage) { r.Source = strings.Replace(r.Source, "s-1", "s-2", 1) }),
			with(r, func(r *model.Usage) { r.Project += "/sub" }),
			with(r, func(r *model.Usage) { r.Model = "claude-opus-5-5" }),
			with(r, func(r *model.Usage) { r.Input, r.CacheRead = 20, 80 }),
			with(r, func(r *model.Usage) {
				r.Dedup = copyMark + r.Dedup
				r.Project = "/home/u/other"
			}),
			with(r, func(r *model.Usage) { r.Output = 1 }), // a partial snapshot
		)
	}
	// Records without a key are all kept, identical or not.
	for i := range 3 {
		r := tied("")
		r.Time = r.Time.Add(time.Duration(i) * time.Minute)
		pool = append(pool, r, r, with(r, func(r *model.Usage) { r.Project += "/sub" }))
	}
	rng := rand.New(rand.NewPCG(1, 2))
	var (
		want      []model.Usage
		wantStats Stats
	)
	for i := range 200 {
		recs := slices.Clone(pool)
		rng.Shuffle(len(recs), func(i, j int) { recs[i], recs[j] = recs[j], recs[i] })
		var stats Stats
		got := dedup(recs, &stats)
		if i == 0 {
			want, wantStats = got, stats
			if len(got) != 20+9 {
				t.Fatalf("kept %d records, want %d", len(got), 20+9)
			}
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d: output differs from the first", i)
		}
		if stats.Dropped != wantStats.Dropped || stats.DroppedTokens != wantStats.DroppedTokens {
			t.Fatalf("shuffle %d: stats %+v, first %+v", i, stats, wantStats)
		}
	}
}

// claudeLine renders an assistant line of Claude Code's log for the response
// msg_1/req_A, written by session in cwd. origin is what the line says in
// session_id, the session it was first written in, and is left out if empty,
// as older versions do.
func claudeLine(session, origin, cwd string) string {
	sid := ""
	if origin != "" {
		sid = fmt.Sprintf(`,"session_id":%q`, origin)
	}
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-08-25T03:00:00.000Z","cwd":%q,"sessionId":%q%s,"requestId":"req_A","message":{"id":"msg_1","model":"claude-fable-5","usage":{"input_tokens":2,"cache_read_input_tokens":100,"cache_creation_input_tokens":10,"output_tokens":20}}}`,
		cwd, session, sid)
}

// Claude Code copies a session's conversation into the log of a session it
// starts from it, such as a background session. A copied line keeps the ids
// and the time, but takes the new session's id and working directory, and
// keeps the original session's id in session_id. The original says where the
// call was made, so it survives, in whichever order the logs are read; a copy
// whose original is gone still counts.
func TestDedupKeepsTheOriginalOfACopiedClaudeCall(t *testing.T) {
	dir := t.TempDir()
	// The copy comes first by file and by project, so only knowing it for a
	// copy can make the original survive.
	orig := writeLines(t, filepath.Join(dir, "sess-z.jsonl"), claudeLine("sess-z", "sess-z", "/home/u/proj/sub"))
	copied := writeLines(t, filepath.Join(dir, "sess-a.jsonl"), claudeLine("sess-a", "sess-z", "/home/u/proj"))
	a, b := parseFile(t, parseClaude, orig), parseFile(t, parseClaude, copied)
	const key = "claude|msg_1|req_A"
	for _, recs := range [][]model.Usage{append(slices.Clone(a), b...), append(slices.Clone(b), a...)} {
		kept := dedup(recs, nil)
		if len(kept) != 1 || kept[0].Project != "/home/u/proj/sub" || kept[0].Source != orig || kept[0].Dedup != key {
			t.Fatalf("kept %+v, want the original in %s", kept, orig)
		}
	}
	if kept := dedup(b, nil); len(kept) != 1 || kept[0].Project != "/home/u/proj" || kept[0].Dedup != key {
		t.Fatalf("a copy on its own: kept %+v", kept)
	}
	// A line that names no origin, as older versions write them, counts as
	// an original, and ties with another as originals do: here the first
	// by file wins.
	older := writeLines(t, filepath.Join(dir, "sess-c.jsonl"), claudeLine("sess-c", "", "/home/u/proj"))
	c := parseFile(t, parseClaude, older)
	for _, recs := range [][]model.Usage{append(slices.Clone(a), c...), append(slices.Clone(c), a...)} {
		if kept := dedup(recs, nil); len(kept) != 1 || kept[0].Source != older {
			t.Fatalf("kept %+v, want the record from %s", kept, older)
		}
	}
}

// Every agent must produce deduplication keys, or scanning two roots that hold
// the same session counts it twice. Codex in particular has no per-response id,
// which made overlapping roots silently double the totals.
func TestEveryParserEmitsDedupKeys(t *testing.T) {
	cases := []struct {
		name  string
		parse parser
		file  string
	}{
		{"claude", parseClaude, "claude.jsonl"},
		{"claude-dupes", parseClaude, "claude_dupes.jsonl"},
		{"codex", parseCodex, "codex.jsonl"},
		{"codex-republished", parseCodex, "codex_republished.jsonl"},
		{"codex-records", parseCodex, "codex_records.jsonl"},
		{"grok", parseGrok, "grok_updates.jsonl"},
	}
	for _, c := range cases {
		recs := parseFile(t, c.parse, filepath.Join("testdata", c.file))
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
		parse parser
		file  string
		rel   string // where the file sits inside its root
	}{
		{"codex", parseCodex, "codex.jsonl", "sessions/2026/08/25"},
		{"codex-republished", parseCodex, "codex_republished.jsonl", "sessions/2026/08/25"},
		{"codex-records", parseCodex, "codex_records.jsonl", "sessions/2026/09/20"},
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
			parsed = append(parsed, parseFile(t, c.parse, dst))
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
