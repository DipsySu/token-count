package source

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"token-count/internal/model"
)

// codexTok renders one Codex usage block. Codex counts cached input inside
// input_tokens, so a block of (100, 0, 10) totals 110 tokens.
func codexTok(input, cached, output int64) string {
	return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}`,
		input, cached, output, input+output)
}

// tokenCount renders a token_count event the way Codex writes it. An empty
// last or total leaves that block out, as older logs do.
func tokenCount(sec int, last, total string) string {
	var info []string
	if total != "" {
		info = append(info, `"total_token_usage":`+total)
	}
	if last != "" {
		info = append(info, `"last_token_usage":`+last)
	}
	return fmt.Sprintf(`{"timestamp":"2026-08-25T01:%02d:%02d.000Z","type":"event_msg","payload":{"type":"token_count","info":{%s},"rate_limits":{"primary":{"used_percent":1.0}}}}`,
		sec/60, sec%60, strings.Join(info, ","))
}

func codexMeta(session, id string) string {
	return fmt.Sprintf(`{"timestamp":"2026-08-25T01:00:00.000Z","type":"session_meta","payload":{"session_id":%q,"id":%q,"cwd":"/home/u/proj"}}`,
		session, id)
}

const (
	codexTurn     = `{"timestamp":"2026-08-25T01:00:00.000Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`
	codexResumed  = `{"timestamp":"2026-08-25T01:00:00.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol"}}}`
	codexCompacts = `{"timestamp":"2026-08-25T01:00:00.000Z","type":"compacted","payload":{"message":""}}`
)

func writeLines(t *testing.T, path string, lines ...string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeCodexLog writes a rollout log for thread s-1, headed the way Codex
// starts every log.
func writeCodexLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout-2026-08-25T09-00-00-s-1.jsonl")
	return writeLines(t, path, append([]string{codexMeta("s-1", "s-1"), codexTurn}, lines...)...)
}

func sumOf(recs []model.Usage) (total, calls int64) {
	var t model.Totals
	for _, r := range recs {
		t.AddUsage(r)
	}
	return t.Total(), t.Calls
}

// Codex publishes token_count again when only its rate limits changed,
// repeating the last exchange's usage. Summing every event counted that
// exchange twice.
func TestCodexRepublishedEventCountsOnce(t *testing.T) {
	use := codexTok(100, 0, 10)
	path := writeCodexLog(t,
		tokenCount(1, use, use),
		tokenCount(2, use, use), // the same state, published again
	)
	recs := parseFile(t, parseCodex, path)
	if total, calls := sumOf(recs); total != 110 || calls != 1 {
		t.Fatalf("got %d tokens over %d calls, want 110 over 1", total, calls)
	}
}

// After compacting its context Codex reports the unchanged running total with
// an all-zero last exchange. That is not a call, and must not disturb the
// exchanges around it.
func TestCodexCompactionEventIgnored(t *testing.T) {
	first := codexTok(100, 0, 10)
	path := writeCodexLog(t,
		tokenCount(1, first, first),
		codexCompacts,
		tokenCount(3, codexTok(0, 0, 0), first),
		tokenCount(4, codexTok(30, 0, 3), codexTok(130, 0, 13)),
	)
	recs := parseFile(t, parseCodex, path)
	if total, calls := sumOf(recs); total != 143 || calls != 2 {
		t.Fatalf("got %d tokens over %d calls, want 143 over 2", total, calls)
	}
}

// A resumed Codex process starts a fresh running total, so the total drops.
// The exchange that reports it is real, and its state is the new baseline that
// later re-publications repeat.
func TestCodexResetCounted(t *testing.T) {
	a, b := codexTok(100, 0, 10), codexTok(200, 0, 20)
	c, d := codexTok(50, 0, 5), codexTok(60, 0, 6)
	path := writeCodexLog(t,
		tokenCount(1, a, a),
		tokenCount(2, b, codexTok(300, 0, 30)),
		codexResumed,
		tokenCount(4, c, c), // the new process: the total dropped
		tokenCount(5, c, c), // and its state published again
		tokenCount(6, d, codexTok(110, 0, 11)),
	)
	recs := parseFile(t, parseCodex, path)
	if total, calls := sumOf(recs); total != 110+220+55+66 || calls != 4 {
		t.Fatalf("got %d tokens over %d calls, want %d over 4", total, calls, 110+220+55+66)
	}
}

// Logs written before Codex reported a running total carry last_token_usage
// only, and every event is an exchange.
func TestCodexLegacyLogCountsLast(t *testing.T) {
	path := writeCodexLog(t,
		tokenCount(1, codexTok(100, 0, 10), ""),
		tokenCount(2, codexTok(200, 0, 20), ""),
	)
	recs := parseFile(t, parseCodex, path)
	if total, calls := sumOf(recs); total != 330 || calls != 2 {
		t.Fatalf("got %d tokens over %d calls, want 330 over 2", total, calls)
	}
}

// An event without last_token_usage still says how far the running total
// moved: the first total is the exchange itself, a later one the difference,
// and a total that dropped starts over.
func TestCodexMissingLastUsesTotalDelta(t *testing.T) {
	path := writeCodexLog(t,
		tokenCount(1, "", codexTok(100, 0, 10)),
		tokenCount(2, "", codexTok(300, 0, 30)),
		tokenCount(3, "", codexTok(300, 0, 30)), // published again
		tokenCount(4, "", codexTok(50, 0, 5)),   // a fresh process
	)
	recs := parseFile(t, parseCodex, path)
	if total, calls := sumOf(recs); total != 110+220+55 || calls != 3 {
		t.Fatalf("got %d tokens over %d calls, want %d over 3", total, calls, 110+220+55)
	}
	if len(recs) == 3 && (recs[1].Input != 200 || recs[1].Output != 20) {
		t.Fatalf("second exchange %+v, want the difference of the totals", recs[1])
	}
}

// A process resumed days later can report a running total that also carries
// usage recorded elsewhere, so the total jumps by far more than the exchange
// in between. The exchange's own figure is what it cost.
func TestCodexResumeJumpCountsLast(t *testing.T) {
	a, b := codexTok(100, 0, 10), codexTok(40, 0, 4)
	path := writeCodexLog(t,
		tokenCount(1, a, a),
		codexResumed,
		tokenCount(3, b, codexTok(4_000_140, 0, 14)),
	)
	recs := parseFile(t, parseCodex, path)
	if total, calls := sumOf(recs); total != 154 || calls != 2 {
		t.Fatalf("got %d tokens over %d calls, want 154 over 2", total, calls)
	}
}

// An event whose time cannot be read is dropped as if it were not there. Its
// running total must not become the baseline either: Codex may publish the
// same state again with a readable time, and that copy is then the only one
// left to count. Likewise an event without last_token_usage that follows it
// must take the dropped event's share of the difference.
func TestCodexUntimedEventIsNoBaseline(t *testing.T) {
	untimed := func(line string) string {
		t.Helper()
		out := strings.Replace(line, `"timestamp":"2026-08-25T01:00:02.000Z"`, `"timestamp":""`, 1)
		if out == line {
			t.Fatal("no timestamp to remove")
		}
		return out
	}
	a, b := codexTok(100, 0, 10), codexTok(200, 0, 20)
	for _, c := range []struct {
		name  string
		lines []string
		total int64
	}{
		{"published again", []string{
			tokenCount(1, a, a),
			untimed(tokenCount(2, b, codexTok(300, 0, 30))),
			tokenCount(3, b, codexTok(300, 0, 30)),
		}, 110 + 220},
		{"next event lacks last", []string{
			tokenCount(1, a, a),
			untimed(tokenCount(2, b, codexTok(300, 0, 30))),
			tokenCount(3, "", codexTok(350, 0, 35)),
		}, 110 + 275},
	} {
		recs := parseFile(t, parseCodex, writeCodexLog(t, c.lines...))
		if total, calls := sumOf(recs); total != c.total || calls != 2 {
			t.Errorf("%s: got %d tokens over %d calls, want %d over 2", c.name, total, calls, c.total)
		}
	}
}

// Positions count exchanges rather than events, so a re-publication takes no
// number of its own.
func TestCodexKeysCountExchanges(t *testing.T) {
	a := codexTok(100, 0, 10)
	path := writeCodexLog(t,
		tokenCount(1, a, a),
		tokenCount(2, a, a),
		tokenCount(3, codexTok(200, 0, 20), codexTok(300, 0, 30)),
	)
	recs := parseFile(t, parseCodex, path)
	var keys []string
	for _, r := range recs {
		keys = append(keys, r.Dedup)
	}
	if got, want := strings.Join(keys, " "), "codex|s-1|1 codex|s-1|2"; got != want {
		t.Fatalf("keys %q, want %q", got, want)
	}
}

// Codex writes each subagent thread to its own rollout file, and recent
// versions put the root session's id in session_id there, followed by a copy
// of the parent's session_meta. Keying on session_id merged every subagent's
// exchanges with its parent's, keeping only the largest at each position.
func TestCodexSubagentThreadsAreDistinct(t *testing.T) {
	dir := t.TempDir()
	a, b := codexTok(100, 0, 10), codexTok(200, 0, 20)
	parent := writeLines(t, filepath.Join(dir, "rollout-2026-08-25T09-00-00-root.jsonl"),
		codexMeta("root", "root"), codexTurn,
		tokenCount(1, a, a),
		tokenCount(2, b, codexTok(300, 0, 30)),
	)
	c, d := codexTok(50, 0, 5), codexTok(60, 0, 6)
	child := writeLines(t, filepath.Join(dir, "rollout-2026-08-25T09-00-05-child.jsonl"),
		codexMeta("root", "child"), codexMeta("root", "root"), codexTurn,
		tokenCount(6, c, c),
		tokenCount(7, d, codexTok(110, 0, 11)),
	)
	recs := append(parseFile(t, parseCodex, parent), parseFile(t, parseCodex, child)...)
	kept := dedup(recs, nil)
	if total, calls := sumOf(kept); total != 110+220+55+66 || calls != 4 {
		t.Fatalf("got %d tokens over %d calls, want %d over 4", total, calls, 110+220+55+66)
	}
}

// The fixture follows a real log through a re-publication, a compaction and a
// resume days later.
func TestParseCodexRepublishedFixture(t *testing.T) {
	recs := parseFile(t, parseCodex, filepath.Join("testdata", "codex_republished.jsonl"))
	if total, calls := sumOf(recs); total != 1050+1230+920+1140 || calls != 4 {
		t.Fatalf("got %d tokens over %d calls, want %d over 4", total, calls, 1050+1230+920+1140)
	}
	for _, r := range recs {
		if r.Model != "gpt-5.6-sol" || r.Project != "/home/u/proj" {
			t.Fatalf("model/project not carried: %+v", r)
		}
	}
}
