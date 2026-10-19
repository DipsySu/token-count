package model

import (
	"strings"
	"testing"
	"time"
)

func TestParseTimeISO(t *testing.T) {
	ts, ok := ParseTime("2026-08-25T03:14:40.250Z")
	if !ok {
		t.Fatal("parse")
	}
	if ts.UTC().Day() != 25 || ts.UTC().Year() != 2026 {
		t.Fatalf("got %s", ts.UTC())
	}
}

func TestParseUnix(t *testing.T) {
	ts, ok := ParseTime(int64(1787550336))
	if !ok || ts.UTC().Year() != 2026 {
		t.Fatalf("unix %v %v", ok, ts)
	}
	ts, ok = ParseTime(int64(1787550336000))
	if !ok || ts.UTC().Year() != 2026 {
		t.Fatalf("unix ms %v %v", ok, ts)
	}
}

func TestMakeInputIncludesCache(t *testing.T) {
	u, ok := Make(MakeOpts{
		Agent:              "codex",
		Time:               time.Date(2026, 8, 25, 0, 0, 0, 0, time.Local),
		Input:              14976,
		CacheRead:          11008,
		Output:             224,
		InputIncludesCache: true,
	})
	if !ok {
		t.Fatal("make")
	}
	if u.Input != 3968 || u.CacheRead != 11008 || u.Total() != 14976+224 {
		t.Fatalf("%+v total=%d", u, u.Total())
	}
}

func TestMakeReasoningAdded(t *testing.T) {
	u, ok := Make(MakeOpts{
		Agent:                   "gemini",
		Time:                    time.Date(2026, 8, 25, 0, 0, 0, 0, time.Local),
		Input:                   100,
		Output:                  30,
		Reasoning:               146,
		OutputIncludesReasoning: false,
	})
	if !ok {
		t.Fatal("make")
	}
	if u.Output != 176 || u.Reasoning != 146 {
		t.Fatalf("%+v", u)
	}
}

func TestFmtTokens(t *testing.T) {
	if FmtTokens(9999) != "9,999" {
		t.Fatalf("%s", FmtTokens(9999))
	}
	if FmtTokens(12000) != "12.0k" {
		t.Fatalf("%s", FmtTokens(12000))
	}
	if FmtTokens(1_270_000) != "1.27M" {
		t.Fatalf("%s", FmtTokens(1_270_000))
	}
}

func TestParseTimeNaiveStampsAreUTC(t *testing.T) {
	// These agents write local-looking stamps with no offset; treating them as
	// UTC is what the previous implementation did by accident, so it must stay
	// deliberate now.
	for _, s := range []string{
		"2026-08-25T03:14:40",
		"2026-08-25T03:14:40.250",
		"2026-08-25 03:14:40",
	} {
		ts, ok := ParseTime(s)
		if !ok {
			t.Fatalf("%q did not parse", s)
		}
		if got := ts.UTC().Format("2006-01-02T15:04:05"); got != "2026-08-25T03:14:40" {
			t.Fatalf("%q became %s", s, got)
		}
	}
}

func TestParseTimeHonoursExplicitOffset(t *testing.T) {
	ts, ok := ParseTime("2026-08-25T11:14:40+08:00")
	if !ok {
		t.Fatal("parse")
	}
	if got := ts.UTC().Format("15:04:05"); got != "03:14:40" {
		t.Fatalf("offset ignored: %s", got)
	}
	// A negative offset must not be mistaken for one of the date separators.
	ts, ok = ParseTime("2026-08-24T22:14:40-05:00")
	if !ok {
		t.Fatal("parse negative offset")
	}
	if got := ts.UTC().Format("2006-01-02T15:04:05"); got != "2026-08-25T03:14:40" {
		t.Fatalf("negative offset: %s", got)
	}
}

func TestParseTimeUnitScales(t *testing.T) {
	want := "2026-08-24" // 1787550336 UTC
	for _, v := range []int64{
		1787550336,             // seconds
		1787550336_000,         // milliseconds
		1787550336_000_000,     // microseconds
		1787550336_000_000_000, // nanoseconds
	} {
		ts, ok := ParseTime(v)
		if !ok {
			t.Fatalf("%d did not parse", v)
		}
		if got := ts.UTC().Format("2006-01-02"); got != want {
			t.Fatalf("%d became %s", v, got)
		}
	}
}

func TestMakeCacheLargerThanInput(t *testing.T) {
	// Some providers report a cache read exceeding the input it is part of.
	// The overlap must not be double-counted as fresh input.
	u, ok := Make(MakeOpts{
		Agent:              "grok",
		Time:               time.Date(2026, 8, 25, 0, 0, 0, 0, time.Local),
		Input:              100,
		CacheRead:          500,
		Output:             10,
		InputIncludesCache: true,
	})
	if !ok {
		t.Fatal("make")
	}
	if u.Input != 0 {
		t.Fatalf("input should collapse to 0, got %d", u.Input)
	}
	if u.Total() != 510 {
		t.Fatalf("total %d", u.Total())
	}
}

func TestMakeCallsDefaultAndOverride(t *testing.T) {
	at := time.Date(2026, 8, 25, 0, 0, 0, 0, time.Local)
	u, _ := Make(MakeOpts{Agent: "claude", Time: at, Input: 5})
	if u.Calls != 1 {
		t.Fatalf("default calls %d", u.Calls)
	}
	// Grok logs a whole turn at once and reports how many requests it made.
	u, _ = Make(MakeOpts{Agent: "grok", Time: at, Input: 5, Calls: 24})
	if u.Calls != 24 {
		t.Fatalf("calls %d", u.Calls)
	}
	var tot Totals
	tot.AddUsage(u)
	if tot.Calls != 24 || tot.Records != 1 {
		t.Fatalf("totals %+v", tot)
	}
}

func TestTotalsDerivedMetrics(t *testing.T) {
	tot := Totals{Input: 100, CacheRead: 900, CacheWrite: 0, Output: 50, Reasoning: 10, Calls: 5}
	if tot.Total() != 1050 {
		t.Fatalf("total %d", tot.Total())
	}
	if tot.Fresh() != 150 {
		t.Fatalf("fresh %d", tot.Fresh())
	}
	if got := tot.CacheHit(); got < 0.89 || got > 0.91 {
		t.Fatalf("cache hit %f", got)
	}
	if tot.PerCall() != 210 {
		t.Fatalf("per call %d", tot.PerCall())
	}
	if got := tot.ReasonShare(); got < 0.19 || got > 0.21 {
		t.Fatalf("reason share %f", got)
	}
	// No calls and no output must not divide by zero.
	empty := Totals{}
	if empty.PerCall() != 0 || empty.CacheHit() != 0 || empty.ReasonShare() != 0 {
		t.Fatal("empty totals should be inert")
	}
}

func TestBarStaysWithinWidth(t *testing.T) {
	for _, w := range []int{1, 3, 8, 14, 30} {
		for _, n := range []int64{0, 1, 49, 50, 99, 100, 1000} {
			got := Bar(n, 100, w)
			if len([]rune(got)) != w {
				t.Fatalf("Bar(%d,100,%d) is %d runes: %q", n, w, len([]rune(got)), got)
			}
		}
	}
	if strings.TrimRight(Bar(0, 100, 5), " ") != "" {
		t.Fatal("zero should draw nothing")
	}
	if Bar(100, 100, 5) != "█████" {
		t.Fatalf("full bar %q", Bar(100, 100, 5))
	}
	// A tiny non-zero value must still be visible.
	if strings.TrimRight(Bar(1, 1_000_000, 10), " ") == "" {
		t.Fatal("a nonzero value must render something")
	}
}

func TestSparklineScale(t *testing.T) {
	got := Sparkline([]int64{0, 50, 100})
	if len([]rune(got)) != 3 {
		t.Fatalf("len %q", got)
	}
	r := []rune(got)
	if r[0] != '▁' || r[2] != '█' {
		t.Fatalf("endpoints %q", got)
	}
	if Sparkline(nil) != "" {
		t.Fatal("nil series")
	}
	// An all-zero series must not panic or claim a peak.
	if got := Sparkline([]int64{0, 0, 0}); got != "▁▁▁" {
		t.Fatalf("zero series %q", got)
	}
}

func TestChangeDelta(t *testing.T) {
	if got := Change(110, 100); !got.Up || got.String() != "↑10%" {
		t.Fatalf("%+v %s", got, got)
	}
	if got := Change(50, 100); got.Up || got.String() != "↓50%" {
		t.Fatalf("%+v %s", got, got)
	}
	// No baseline means no percentage to quote.
	if got := Change(100, 0); got.Known || got.String() != "—" {
		t.Fatalf("%+v %s", got, got)
	}
	if got := Change(0, 0); got.Known || got.String() != "—" {
		t.Fatalf("%+v", got)
	}
}

func TestShortModelTrimsDateSuffix(t *testing.T) {
	if got := ShortModel("claude-haiku-4-5-20251001"); got != "claude-haiku-4-5" {
		t.Fatalf("%s", got)
	}
	// A version that merely looks numeric must survive.
	if got := ShortModel("claude-opus-5"); got != "claude-opus-5" {
		t.Fatalf("%s", got)
	}
	if got := ShortModel("anthropic/claude-fable-5"); got != "claude-fable-5" {
		t.Fatalf("%s", got)
	}
	if ShortModel("") != "" {
		t.Fatal("empty")
	}
}

func TestShortProjectKeepsDistinctPaths(t *testing.T) {
	// Two machines' paths for the same project name must not collapse into
	// one label, or their usage appears merged in the project view.
	a := ShortProject("/home/someone/Developer/tool-lib/pay-module")
	b := ShortProject("/Users/someone/Developer/tool-lib/pay-module")
	if a == b {
		t.Fatalf("distinct paths collapsed to %q", a)
	}
}

func TestHeatCellRamp(t *testing.T) {
	if HeatCell(0, 1, 100) != " " {
		t.Fatalf("zero should be blank, got %q", HeatCell(0, 1, 100))
	}
	if HeatCell(100, 1, 100) != "█" {
		t.Fatalf("peak %q", HeatCell(100, 1, 100))
	}
	// The quietest active cell must still be visible, not blank.
	if HeatCell(1, 1, 1_000_000) == " " {
		t.Fatal("small values vanish")
	}
}

// Real hour-of-week usage spans a couple of orders of magnitude. The scale has
// to spread that across the ramp: normalising against zero would crush every
// active hour into one end, which is what made an earlier version render the
// whole grid a single colour.
func TestHeatLevelSpreadsSkewedData(t *testing.T) {
	// Percentiles measured from actual session logs.
	lo, hi := int64(436_210), int64(114_145_970)
	samples := []int64{436_210, 1_426_219, 3_877_894, 11_298_142, 21_020_040, 32_961_529, 114_145_970}

	var levels []float64
	prev := -1.0
	for _, v := range samples {
		got := HeatLevel(v, lo, hi)
		if got < prev {
			t.Fatalf("not monotonic at %d: %f after %f", v, got, prev)
		}
		prev = got
		levels = append(levels, got)
	}
	if levels[0] != 0 {
		t.Fatalf("quietest active cell should anchor the scale: %f", levels[0])
	}
	if levels[len(levels)-1] != 1 {
		t.Fatalf("peak should top the scale: %f", levels[len(levels)-1])
	}
	// The median must land mid-scale rather than pinned to either end.
	if mid := levels[3]; mid < 0.35 || mid > 0.75 {
		t.Fatalf("median at %f is not spread across the ramp", mid)
	}
	// Every step must be distinguishable, which is the whole point.
	for i := 1; i < len(levels); i++ {
		if levels[i]-levels[i-1] < 0.02 {
			t.Fatalf("levels %d and %d are indistinguishable: %f vs %f", i-1, i, levels[i-1], levels[i])
		}
	}
	if got := HeatLevel(0, lo, hi); got != 0 {
		t.Fatalf("zero %f", got)
	}
	// A degenerate range must not divide by zero.
	if got := HeatLevel(5, 5, 5); got != 1 {
		t.Fatalf("single-value range %f", got)
	}
	if got := HeatLevel(5, 0, 0); got != 0 {
		t.Fatalf("empty range %f", got)
	}
}
