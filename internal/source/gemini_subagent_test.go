package source

import (
	"path/filepath"
	"testing"
)

// Gemini CLI nests subagent recordings under their parent session id:
// chats/<parent-session-id>/<subagent-session-id>.jsonl.
func TestGeminiSubagentChatsAreRead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "tmp", "p", "chats", "parent-id", "sub-id.jsonl"),
		geminiFixture(t, "session-2026-08-26T01-00-jsonl000.jsonl"))
	t.Setenv("GEMINI_DIR", root)
	recs, _ := CollectStats([]string{"gemini"}, nil, nil)
	if total, calls := sumOf(recs); total != 1420+1560 || calls != 2 {
		t.Fatalf("total %d over %d calls, want %d over 2", total, calls, 1420+1560)
	}
}
