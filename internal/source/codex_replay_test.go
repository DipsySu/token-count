package source

import (
	"path/filepath"
	"strings"
	"testing"
)

// A Codex branch copies its parent's token_count events verbatim, and a
// MultiAgent V2 subagent replays them rewritten to its spawn instant before
// its own turn. Either way the parent's exchanges count once, from the parent:
// copies are matched across files by time, model and usage (ccusage #1237),
// and a subagent's leading burst of replayed usage is not its own.
func TestCodexCopiedAndReplayedHistory(t *testing.T) {
	home, _ := filepath.Abs(filepath.Join("testdata", "home", ".codex"))
	t.Setenv("CODEX_HOME", home)
	recs, _ := CollectStats([]string{"codex"}, nil, nil)
	var day []string
	var total int64
	for _, r := range recs {
		if !strings.Contains(r.Source, "2026/08/27") {
			continue
		}
		day = append(day, filepath.Base(r.Source)[len("rollout-2026-08-27T16-00-00-"):])
		total += r.Total()
	}
	// parent 1200 + 600, branch's own 350, subagent's own 420.
	want := "par-1.jsonl par-1.jsonl br-1.jsonl sub-1.jsonl"
	if got := strings.Join(day, " "); got != want || total != 2570 {
		t.Errorf("counted %s = %d, want %s = 2570", got, total, want)
	}
}
