package source

import (
	"path/filepath"
	"testing"
)

func TestParseClaude(t *testing.T) {
	recs := parseClaude(filepath.Join("testdata", "claude.jsonl"))
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.Agent != "claude" || r.Model != "claude-fable-5" {
		t.Fatalf("agent/model: %+v", r)
	}
	if r.Input != 2 || r.CacheRead != 100 || r.CacheWrite != 10 || r.Output != 20 {
		t.Fatalf("tokens: %+v", r)
	}
}

func TestParseCodex(t *testing.T) {
	recs := parseCodex(filepath.Join("testdata", "codex.jsonl"))
	if len(recs) != 2 {
		t.Fatalf("got %d records", len(recs))
	}
	if recs[0].Model != "gpt-5.6-sol" {
		t.Fatalf("model %s", recs[0].Model)
	}
	if recs[0].Input != 100 { // 200 - 100 cached
		t.Fatalf("input %d", recs[0].Input)
	}
	if recs[1].Input != 50 {
		t.Fatalf("input %d", recs[1].Input)
	}
}

func TestParseGrok(t *testing.T) {
	recs := parseGrok(filepath.Join("testdata", "grok_updates.jsonl"))
	if len(recs) != 1 {
		t.Fatalf("got %d records: %+v", len(recs), recs)
	}
	if recs[0].Model != "grok-4.6" {
		t.Fatalf("model %s", recs[0].Model)
	}
	if recs[0].CacheRead != 80 || recs[0].Input != 20 {
		t.Fatalf("tokens %+v", recs[0])
	}
}
