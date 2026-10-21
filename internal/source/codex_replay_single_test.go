package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A subagent spawned after its parent's first call replays that one event,
// stamped at the spawn instant. A lone replayed event is still replay.
func TestCodexSingleReplayedEventIsDropped(t *testing.T) {
	src := filepath.Join("testdata", "home", ".codex", "sessions", "2026", "08", "27", "rollout-2026-08-27T16-03-00-sub-1.jsonl")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if !strings.Contains(l, `"timestamp":"2026-08-27T08:03:00.030Z"`) {
			keep = append(keep, l)
		}
	}
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "08", "27")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLines(t, filepath.Join(dir, filepath.Base(src)), keep...)
	t.Setenv("CODEX_HOME", home)
	recs, _ := CollectStats([]string{"codex"}, nil, nil)
	if total, calls := sumOf(recs); total != 420 || calls != 1 {
		t.Fatalf("total %d over %d calls, want 420 over 1 (the subagent's own)", total, calls)
	}
}
