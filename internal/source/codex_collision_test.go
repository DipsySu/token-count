package source

import (
	"os"
	"path/filepath"
	"testing"
)

// Known behaviour, pinned on purpose: Codex exchanges are deduplicated by
// timestamp + model + token tuple (as ccusage does), so two genuinely different
// sessions that log the same millisecond, model and identical usage merge into
// one record. If this test starts failing, the dedup rule changed: decide
// deliberately and update the README.
func TestCodexIdenticalExchangesInDifferentSessionsMerge(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "09", "01")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"timestamp":"2026-09-01T01:00:00.000Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}
{"timestamp":"2026-09-01T01:00:01.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"cached_input_tokens":100,"output_tokens":10,"reasoning_output_tokens":3},"total_token_usage":{"input_tokens":200,"cached_input_tokens":100,"output_tokens":10}}}}
`
	for _, name := range []string{"rollout-2026-09-01T01-00-00-aaa.jsonl", "rollout-2026-09-01T01-00-00-bbb.jsonl"} {
		if err := os.WriteFile(filepath.Join(day, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", home)
	recs, _ := CollectStats([]string{"codex"}, nil, nil)
	var total int64
	for _, r := range recs {
		total += r.Total()
	}
	if len(recs) != 1 || total != 210 {
		t.Errorf("got %d records totalling %d, want the two sessions merged into 1 record of 210", len(recs), total)
	}
}
