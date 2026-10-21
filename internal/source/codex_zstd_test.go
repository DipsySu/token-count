package source

import (
	"path/filepath"
	"strings"
	"testing"
)

// Codex compresses old rollouts to .jsonl.zst. Compression keeps the rollout
// the same one: next to a plain copy in its dir the plain one is read (Codex
// restores it before deleting the .zst on resume), and the live sessions/
// copy wins over archived_sessions/ whatever the encoding, as in ccusage.
func TestCodexZstdRollouts(t *testing.T) {
	home, _ := filepath.Abs(filepath.Join("testdata", "home", ".codex"))
	t.Setenv("CODEX_HOME", home)
	recs, stats := CollectStats([]string{"codex"}, nil, nil)
	if len(stats.Issues) != 0 {
		t.Fatalf("issues %+v", stats.Issues)
	}
	var got []string
	var total int64
	for _, r := range recs {
		if strings.Contains(r.Source, "2026/08/28") {
			rel, _ := filepath.Rel(home, r.Source)
			got = append(got, filepath.ToSlash(rel))
			total += r.Total()
		}
	}
	want := "sessions/2026/08/28/rollout-2026-08-28T09-00-00-zst-1.jsonl.zst " +
		"sessions/2026/08/28/rollout-2026-08-28T09-10-00-both-1.jsonl"
	if strings.Join(got, " ") != want || total != 700+300 {
		t.Errorf("counted %v = %d, want %s = 1000", got, total, want)
	}
}
