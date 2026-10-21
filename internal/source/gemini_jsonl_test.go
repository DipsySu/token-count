package source

import (
	"path/filepath"
	"testing"
)

// Gemini CLI now records chats as JSONL under tmp/<project>/chats/: a
// metadata line, then one line per message write. A message is appended again
// whenever it changes (tokens arrive after the stream, tool calls finish), so
// the latest line of each id is the message; $set lines update metadata only.
func TestGeminiJSONLChats(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("testdata", "home", ".gemini"))
	t.Setenv("GEMINI_DIR", root)
	recs, _ := CollectStats([]string{"gemini"}, nil, nil)
	var jsonl []int64
	for _, r := range recs {
		if filepath.Ext(r.Source) == ".jsonl" {
			jsonl = append(jsonl, r.Input, r.CacheRead, r.Output, r.Reasoning, r.Total())
		}
	}
	// Same exchange as gemini_tool.json: input+tool-cached, cached,
	// output+thoughts, thoughts, Gemini's own total.
	want := []int64{1200 + 150 - 800, 800, 40 + 30, 30, 1420, 1500 - 1000, 1000, 60, 0, 1560}
	if len(jsonl) != len(want) {
		t.Fatalf("jsonl records %v, want %v", jsonl, want)
	}
	for i := range want {
		if jsonl[i] != want[i] {
			t.Fatalf("jsonl records %v, want %v", jsonl, want)
		}
	}
	if total, calls := sumOf(recs); total != 2*(1420+1560) || calls != 4 {
		t.Errorf("total %d over %d calls, want %d over 4 (json + jsonl)", total, calls, 2*(1420+1560))
	}
}
