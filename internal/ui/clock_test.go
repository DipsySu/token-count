package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"token-count/internal/aggregate"
	"token-count/internal/model"
	"token-count/internal/source"
)

// What counts as today comes from the injected clock, never the wall clock,
// so the same records give the same answer whatever day the suite runs on.
func TestInjectedClockDecidesToday(t *testing.T) {
	pinLocal(t)
	recs := sample(testNow) // one record per agent per day, today included

	m := New(Options{Period: aggregate.PeriodToday, Agents: model.Agents, Now: testClock})
	m.width, m.height = 120, 30
	m.loading = false
	m.setRecords(recs)
	m.recompute()
	if got := len(m.scoped); got != 3 {
		t.Errorf("TUI scoped %d records to today, want 3", got)
	}
	var want int64
	for _, r := range recs {
		if r.Date().Equal(aggregate.Day(testNow)) {
			want += r.Total()
		}
	}
	if got := m.snapshot["today"].Totals.Total(); got != want {
		t.Errorf("today card %d, want %d", got, want)
	}
	if got := stripANSI(m.titleBar(120, newStyles())); !strings.Contains(got, "2026-08-25 15:00") {
		t.Errorf("title bar is not stamped with the clock's time: %q", strings.TrimSpace(got))
	}

	ex := BuildExport(recs, Options{Period: aggregate.PeriodToday, Now: testClock})
	if len(ex.Records) != 3 {
		t.Errorf("export scoped %d records to today, want 3", len(ex.Records))
	}
	if ex.Since == nil || ex.Until == nil ||
		ex.Since.Format("2006-01-02") != "2026-08-25" || ex.Until.Format("2006-01-02") != "2026-08-25" {
		t.Errorf("export window %v..%v, want 2026-08-25 on both sides", ex.Since, ex.Until)
	}
}

// A recompute or an export reads the clock exactly once. A second reading
// could fall after midnight and make one answer describe two different days,
// so this clock jumps a whole day on every reading: any extra read shows.
func TestClockIsReadOncePerComputation(t *testing.T) {
	pinLocal(t)
	reads := 0
	clock := func() time.Time {
		reads++
		return testNow.AddDate(0, 0, reads-1)
	}

	m := New(Options{Period: aggregate.PeriodToday, Agents: model.Agents, Now: clock})
	m.width, m.height = 120, 30
	m.loading = false
	m.setRecords(sample(testNow))
	m.recompute()
	if reads != 1 {
		t.Fatalf("recompute read the clock %d times", reads)
	}
	// Rendering draws what was computed and must not consult the clock again.
	body := stripANSI(m.View())
	if reads != 1 {
		t.Fatalf("rendering read the clock again (%d reads)", reads)
	}
	if !strings.Contains(body, "2026-08-25") {
		t.Errorf("the title bar does not show the instant the figures describe")
	}

	for _, view := range views() {
		reads = 0
		opts := Options{Period: aggregate.PeriodToday, View: view, Now: clock}
		ex := BuildExport(sample(testNow), opts)
		var buf bytes.Buffer
		if err := WriteJSON(&buf, opts, ex, source.Stats{}); err != nil {
			t.Fatal(err)
		}
		WritePlain(&buf, opts, ex, source.Stats{})
		WriteHeat(&buf, opts, ex, source.Stats{})
		if reads != 1 {
			t.Errorf("%s export read the clock %d times", view, reads)
		}

		reads = 0
		WriteHeat(&buf, opts, ScopeExport(sample(testNow), opts), source.Stats{})
		if reads != 1 {
			t.Errorf("%s scoped export read the clock %d times", view, reads)
		}
	}
}

// A nil clock is the wall clock, which is what the real binary runs with.
func TestNilClockIsTheWallClock(t *testing.T) {
	before := time.Now()
	got := Options{}.now()
	if got.Before(before) || got.After(time.Now()) {
		t.Fatalf("nil clock gave %v, not the current time", got)
	}
}
