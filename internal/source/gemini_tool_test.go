package source

import (
	"path/filepath"
	"testing"
)

// Gemini CLI's chat recording logs toolUsePromptTokenCount as tokens.tool.
// It is prompt-side input on top of tokens.input (which already includes
// cached), and the API's totalTokenCount = input + output + thoughts + tool.
func TestParseGeminiToolTokens(t *testing.T) {
	recs := parseFile(t, parseGemini, filepath.Join("testdata", "gemini_tool.json"))
	if len(recs) != 2 {
		t.Fatalf("got %d records", len(recs))
	}
	want := []struct{ in, cr, out, rsn, total int64 }{
		{1200 - 800 + 150, 800, 40 + 30, 30, 1420},
		{1500 - 1000, 1000, 60, 0, 1560},
	}
	for i, w := range want {
		r := recs[i]
		if r.Input != w.in || r.CacheRead != w.cr || r.CacheWrite != 0 || r.Output != w.out || r.Reasoning != w.rsn {
			t.Errorf("rec %d tokens: %+v want %+v", i, r, w)
		}
		if r.Total() != w.total {
			t.Errorf("rec %d total %d, gemini logged %d", i, r.Total(), w.total)
		}
	}
}
