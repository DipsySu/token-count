package source

import (
	"os"
	"path/filepath"
	"testing"
)

func geminiFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "home", ".gemini", "tmp", "redacted", "chats", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// On resume Gemini CLI migrates a legacy session-x.json by appending every
// message to session-x.jsonl, and leaves the .json in place. The .jsonl holds
// the whole history, so the .json beside it must not be counted again.
func TestGeminiMigratedJSONIsNotCountedTwice(t *testing.T) {
	root := t.TempDir()
	chats := filepath.Join(root, "tmp", "p", "chats")
	writeFile(t, filepath.Join(chats, "session-x.json"), geminiFixture(t, "session-2026-08-25T01-00-json0000.json"))
	writeFile(t, filepath.Join(chats, "session-x.jsonl"), geminiFixture(t, "session-2026-08-26T01-00-jsonl000.jsonl"))
	t.Setenv("GEMINI_DIR", root)
	recs, _ := CollectStats([]string{"gemini"}, nil, nil)
	if total, calls := sumOf(recs); total != 1420+1560 || calls != 2 {
		t.Fatalf("total %d over %d calls, want %d over 2 (the .jsonl only)", total, calls, 1420+1560)
	}
}
