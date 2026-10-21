package source

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"token-count/internal/model"
)

// usageRecord renders the token_usage_record line Codex writes for every
// upstream response, half a second into the second so that a test can tell
// its time from that of the token_count event that follows it. The turn and
// thread figures it also carries are running sums, which the parser ignores.
func usageRecord(sec int, resp, usage string) string {
	return fmt.Sprintf(`{"timestamp":"2026-08-25T01:%02d:%02d.500Z","ordinal":%d,"type":"token_usage_record","payload":{"thread_id":"s-1","turn_id":"t-1","session_id":"s-1","root_turn_id":"t-1","response_id":%q,"usage":%s,"turn_token_usage":%s,"thread_token_usage":%s}}`,
		sec/60, sec%60, sec, resp, usage, usage, usage)
}

// codexAt is the time of a line the helpers write at sec, with the half
// second a record carries when record is set.
func codexAt(sec int, record bool) time.Time {
	t := time.Date(2026, 8, 25, 1, sec/60, sec%60, 0, time.UTC)
	if record {
		t = t.Add(500 * time.Millisecond)
	}
	return t
}

func dedupKeys(recs []model.Usage) string {
	keys := make([]string, 0, len(recs))
	for _, r := range recs {
		keys = append(keys, r.Dedup)
	}
	return strings.Join(keys, " ")
}

// A log written before Codex wrote records parses exactly as it did: one
// record per exchange, at the token_count event's time, keyed by its time,
// model and usage.
func TestCodexLegacyLogUnchanged(t *testing.T) {
	recs := parseFile(t, parseCodex, filepath.Join("testdata", "codex_republished.jsonl"))
	want := "codex|use|2026-08-25T02:00:05.000Z|gpt-5.6-sol|1000|600|0|50|10 codex|use|2026-08-25T02:00:09.000Z|gpt-5.6-sol|1200|900|0|30|0 codex|use|2026-08-27T09:00:07.000Z|gpt-5.6-sol|900|100|0|20|5 codex|use|2026-08-27T09:00:12.000Z|gpt-5.6-sol|1100|900|0|40|0"
	if got := dedupKeys(recs); got != want {
		t.Fatalf("keys %q, want %q", got, want)
	}
	for i, want := range []string{"2026-08-25T02:00:05Z", "2026-08-25T02:00:09Z", "2026-08-27T09:00:07Z", "2026-08-27T09:00:12Z"} {
		if got := recs[i].Time.UTC().Format(time.RFC3339); got != want {
			t.Errorf("record %d at %s, want %s", i, got, want)
		}
	}
}

// A log of records alone counts each of them, at its own time, keyed by its
// response id, with the model and project the log names.
func TestCodexRecordsOnly(t *testing.T) {
	path := writeCodexLog(t,
		usageRecord(1, "resp_1", codexTok(100, 40, 10)),
		usageRecord(2, "resp_2", codexTok(200, 150, 20)),
		usageRecord(3, "resp_3", codexTok(300, 0, 30)),
	)
	recs := parseFile(t, parseCodex, path)
	if total, calls := sumOf(recs); total != 110+220+330 || calls != 3 {
		t.Fatalf("got %d tokens over %d calls, want %d over 3", total, calls, 110+220+330)
	}
	if got, want := dedupKeys(recs), "codex|resp|resp_1 codex|resp|resp_2 codex|resp|resp_3"; got != want {
		t.Fatalf("keys %q, want %q", got, want)
	}
	if r := recs[1]; r.Input != 50 || r.CacheRead != 150 || r.Output != 20 || r.Calls != 1 {
		t.Fatalf("second record %+v, want input 50 beside 150 cached, output 20, one call", r)
	}
	for i, r := range recs {
		if !r.Time.Equal(codexAt(i+1, true)) || r.Model != "gpt-5.6-sol" || r.Project != "/home/u/proj" {
			t.Fatalf("record %d: %+v", i, r)
		}
	}
}

// A newer Codex resuming an older session appends records after the old
// token_count events, and still publishes a token_count event after each
// record, repeating its usage, sometimes more than once. The old events count
// as they always did; from the first record on the records count instead of
// the events, so every response counts once, keyed by its id and timed by its
// record.
func TestCodexMixedLogCountsEveryResponseOnce(t *testing.T) {
	a, b := codexTok(100, 0, 10), codexTok(200, 0, 20)
	c, d := codexTok(300, 100, 30), codexTok(400, 300, 40)
	path := writeCodexLog(t,
		tokenCount(1, a, a),
		tokenCount(2, b, codexTok(300, 0, 30)),
		codexResumed,
		usageRecord(10, "resp_c", c),
		tokenCount(11, c, codexTok(600, 100, 60)),
		usageRecord(12, "resp_d", d),
		tokenCount(13, d, codexTok(1000, 400, 100)),
		tokenCount(14, d, codexTok(1000, 400, 100)), // published again
	)
	recs := parseFile(t, parseCodex, path)
	if total, calls := sumOf(recs); total != 110+220+330+440 || calls != 4 {
		t.Fatalf("got %d tokens over %d calls, want %d over 4", total, calls, 110+220+330+440)
	}
	if got, want := dedupKeys(recs), "codex|use|2026-08-25T01:00:01.000Z|gpt-5.6-sol|100|0|0|10|0 codex|use|2026-08-25T01:00:02.000Z|gpt-5.6-sol|200|0|0|20|0 codex|resp|resp_c codex|resp|resp_d"; got != want {
		t.Fatalf("keys %q, want %q", got, want)
	}
	for i, want := range []time.Time{codexAt(1, false), codexAt(2, false), codexAt(10, true), codexAt(12, true)} {
		if !recs[i].Time.Equal(want) {
			t.Errorf("record %d at %s, want %s", i, recs[i].Time.UTC(), want)
		}
	}
}

// A response id is unique across every log, so a response that two logs
// carry, as a fork or a copy of a log does, counts once although each log
// reports it under a thread of its own.
func TestCodexSameResponseInTwoLogsCountsOnce(t *testing.T) {
	dir := t.TempDir()
	shared, own := codexTok(100, 0, 10), codexTok(50, 0, 5)
	parent := writeLines(t, filepath.Join(dir, "rollout-2026-08-25T09-00-00-s-1.jsonl"),
		codexMeta("s-1", "s-1"), codexTurn,
		usageRecord(1, "resp_shared", shared),
		tokenCount(1, shared, shared),
	)
	fork := writeLines(t, filepath.Join(dir, "rollout-2026-08-25T09-05-00-fork-1.jsonl"),
		codexMeta("fork-1", "fork-1"), codexTurn,
		usageRecord(1, "resp_shared", shared),
		tokenCount(1, shared, shared),
		usageRecord(5, "resp_own", own),
		tokenCount(5, own, codexTok(150, 0, 15)),
	)
	kept := dedup(append(parseFile(t, parseCodex, parent), parseFile(t, parseCodex, fork)...), nil)
	if total, calls := sumOf(kept); total != 110+55 || calls != 2 {
		t.Fatalf("got %d tokens over %d calls, want %d over 2", total, calls, 110+55)
	}
}

// Compacting the context is an upstream request of its own. Codex records it,
// and then publishes only the unchanged running total beside an empty last
// exchange, so the record is the one place the request is reported.
func TestCodexCompactionRecordCounts(t *testing.T) {
	a, b, k := codexTok(100, 0, 10), codexTok(30, 0, 3), codexTok(90, 60, 9)
	path := writeCodexLog(t,
		usageRecord(1, "resp_1", a),
		tokenCount(1, a, a),
		usageRecord(2, "resp_compact", k),
		codexCompacts,
		tokenCount(3, codexTok(0, 0, 0), a),
		usageRecord(4, "resp_2", b),
		tokenCount(4, b, codexTok(130, 0, 13)),
	)
	recs := parseFile(t, parseCodex, path)
	if total, calls := sumOf(recs); total != 110+99+33 || calls != 3 {
		t.Fatalf("got %d tokens over %d calls, want %d over 3", total, calls, 110+99+33)
	}
	if got, want := dedupKeys(recs), "codex|resp|resp_1 codex|resp|resp_compact codex|resp|resp_2"; got != want {
		t.Fatalf("keys %q, want %q", got, want)
	}
}

// A record of no usage is not a call. It still marks the log as one that
// records its responses, so the token_count events after it are not counted
// in place of the records.
func TestCodexZeroUsageRecordIgnored(t *testing.T) {
	a := codexTok(100, 0, 10)
	path := writeCodexLog(t,
		usageRecord(1, "resp_0", codexTok(0, 0, 0)),
		usageRecord(2, "resp_1", a),
		tokenCount(2, a, a),
	)
	recs := parseFile(t, parseCodex, path)
	if len(recs) != 1 || recs[0].Dedup != "codex|resp|resp_1" || recs[0].Total() != 110 {
		t.Fatalf("got %+v, want the one call resp_1", recs)
	}
}

// A record without a response id is keyed like a token_count exchange, by
// thread and position, taking the next position after the calls before it.
func TestCodexRecordWithoutResponseID(t *testing.T) {
	a, b, c := codexTok(100, 0, 10), codexTok(200, 0, 20), codexTok(300, 0, 30)
	path := writeCodexLog(t,
		tokenCount(1, a, a),
		usageRecord(2, "resp_b", b),
		tokenCount(2, b, codexTok(300, 0, 30)),
		usageRecord(3, "", c),
		tokenCount(3, c, codexTok(600, 0, 60)),
	)
	recs := parseFile(t, parseCodex, path)
	if got, want := dedupKeys(recs), "codex|use|2026-08-25T01:00:01.000Z|gpt-5.6-sol|100|0|0|10|0 codex|resp|resp_b codex|s-1|3"; got != want {
		t.Fatalf("keys %q, want %q", got, want)
	}
}

// A record belongs to the response it reports: it takes the model the log
// named before it, and its own time. A model switch that Codex logs between a
// record and the token_count event repeating it applies to the calls after.
func TestCodexRecordTakesModelAndTimeOfItsResponse(t *testing.T) {
	a, b := codexTok(100, 0, 10), codexTok(200, 0, 20)
	switched := `{"timestamp":"2026-08-25T01:00:02.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.5-codex"}}}`
	path := writeCodexLog(t,
		usageRecord(1, "resp_1", a),
		switched,
		tokenCount(3, a, a),
		usageRecord(4, "resp_2", b),
		tokenCount(5, b, codexTok(300, 0, 30)),
	)
	recs := parseFile(t, parseCodex, path)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if recs[0].Model != "gpt-5.6-sol" || !recs[0].Time.Equal(codexAt(1, true)) {
		t.Errorf("first call: model %s at %s, want gpt-5.6-sol at %s", recs[0].Model, recs[0].Time.UTC(), codexAt(1, true))
	}
	if recs[1].Model != "gpt-5.5-codex" || !recs[1].Time.Equal(codexAt(4, true)) {
		t.Errorf("second call: model %s at %s, want gpt-5.5-codex at %s", recs[1].Model, recs[1].Time.UTC(), codexAt(4, true))
	}
}

// A record whose time cannot be read is dropped, as an untimed token_count
// event is. It is still a record, so the event that repeats it does not count
// in its place.
func TestCodexUntimedRecordDropped(t *testing.T) {
	a, b := codexTok(100, 0, 10), codexTok(200, 0, 20)
	untimed := strings.Replace(usageRecord(1, "resp_1", a), `"timestamp":"2026-08-25T01:00:01.500Z"`, `"timestamp":""`, 1)
	path := writeCodexLog(t,
		untimed,
		tokenCount(1, a, a),
		usageRecord(2, "resp_2", b),
		tokenCount(2, b, codexTok(300, 0, 30)),
	)
	recs := parseFile(t, parseCodex, path)
	if got := dedupKeys(recs); got != "codex|resp|resp_2" {
		t.Fatalf("keys %q, want only resp_2", got)
	}
}

// The fixture has the shape of a log written by a Codex that records its
// responses: calls whose token_count events repeat them, once re-published,
// a compaction that only its record reports, and a last response that the
// interrupted turn never reported.
func TestParseCodexRecordsFixture(t *testing.T) {
	recs := parseFile(t, parseCodex, filepath.Join("testdata", "codex_records.jsonl"))
	const want = 12300 + 12700 + 204000 + 15250 + 16180
	if total, calls := sumOf(recs); total != want || calls != 5 {
		t.Fatalf("got %d tokens over %d calls, want %d over 5", total, calls, want)
	}
	if got, want := dedupKeys(recs), "codex|resp|resp_0b01 codex|resp|resp_0b02 codex|resp|resp_0b03 codex|resp|resp_0b04 codex|resp|resp_0b05"; got != want {
		t.Fatalf("keys %q, want %q", got, want)
	}
	for _, r := range recs {
		if r.Model != "gpt-6" || r.Project != "/home/u/proj" {
			t.Fatalf("model/project not carried: %+v", r)
		}
	}
	if r := recs[1]; r.Input != 700 || r.CacheRead != 11800 || r.Output != 200 || r.Reasoning != 64 {
		t.Fatalf("second call %+v, want input 700 beside 11800 cached, output 200 of which 64 reasoning", r)
	}
}
