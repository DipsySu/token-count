package source

import (
	"path/filepath"
	"strings"
	"testing"
)

// Codex moves old rollouts to archived_sessions/. ccusage reads both trees;
// a rollout at the same relative path in both is counted from sessions/.
func TestCodexArchivedSessions(t *testing.T) {
	home, _ := filepath.Abs(filepath.Join("testdata", "home", ".codex"))
	t.Setenv("CODEX_HOME", home)
	recs, _ := CollectStats([]string{"codex"}, nil, nil)
	if total, calls := sumOf(recs); total != 2290+530 || calls != 3 {
		t.Errorf("total %d calls %d, want 2820 over 3 calls (sessions 2290 + archived-only 530)", total, calls)
	}
	for _, r := range recs {
		if strings.Contains(r.Source, "archived_sessions") && strings.Contains(r.Source, "act-1") {
			t.Errorf("counted the archived copy of a live rollout: %s", r.Source)
		}
	}
}
