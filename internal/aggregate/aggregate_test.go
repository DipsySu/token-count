package aggregate

import (
	"strings"
	"testing"
	"time"

	"token-count/internal/model"
)

var zone = time.FixedZone("CST", 8*3600)

func rec(day, agent string, in int64) model.Usage {
	return recAt(day+" 12:00", agent, in)
}

func recAt(stamp, agent string, in int64) model.Usage {
	ts, err := time.ParseInLocation("2006-01-02 15:04", stamp, zone)
	if err != nil {
		panic(err)
	}
	u, _ := model.Make(model.MakeOpts{Agent: agent, Time: ts, Input: in, Output: 1})
	return u
}

func TestGroupTimeNestsAgentsUnderDay(t *testing.T) {
	recs := []model.Usage{
		rec("2026-08-25", "claude", 10),
		rec("2026-08-25", "grok", 5),
		rec("2026-08-24", "claude", 3),
	}
	buckets := GroupTime(recs, PeriodDay, false)
	if len(buckets) != 2 {
		t.Fatalf("want 2 day rows, got %d", len(buckets))
	}
	if buckets[0].Key != "2026-08-25" {
		t.Fatalf("newest first: %s", buckets[0].Key)
	}
	// A parent totals its children rather than repeating them as sibling rows.
	if got := buckets[0].Totals.Total(); got != 10+1+5+1 {
		t.Fatalf("parent total %d", got)
	}
	if len(buckets[0].Children) != 2 {
		t.Fatalf("want 2 agent children, got %d", len(buckets[0].Children))
	}
	if buckets[0].Children[0].Agent != "claude" {
		t.Fatalf("children sort by volume: %+v", buckets[0].Children)
	}
	if buckets[0].Children[0].Depth != 1 {
		t.Fatalf("child depth %d", buckets[0].Children[0].Depth)
	}
}

func TestSumIgnoresChildren(t *testing.T) {
	recs := []model.Usage{rec("2026-08-25", "claude", 10), rec("2026-08-25", "grok", 5)}
	buckets := GroupTime(recs, PeriodDay, false)
	flat := Flatten(buckets, map[string]bool{"2026-08-25": true})
	if len(flat) != 3 {
		t.Fatalf("expanded rows %d", len(flat))
	}
	// Summing the flattened list must not double-count the expanded children.
	if got := Sum(flat).Total(); got != 17 {
		t.Fatalf("sum %d, want 17", got)
	}
}

func TestFlattenOnlyExpandsOpenRows(t *testing.T) {
	recs := []model.Usage{rec("2026-08-25", "claude", 10), rec("2026-08-24", "grok", 5)}
	buckets := GroupTime(recs, PeriodDay, false)
	if got := len(Flatten(buckets, map[string]bool{})); got != 2 {
		t.Fatalf("collapsed %d", got)
	}
	if got := len(Flatten(buckets, map[string]bool{"2026-08-25": true})); got != 3 {
		t.Fatalf("one open %d", got)
	}
}

func TestWeekKey(t *testing.T) {
	u := rec("2026-08-25", "claude", 1)
	key, label := BucketKey(PeriodWeek, u.Time)
	if !strings.HasPrefix(key, "2026-W") {
		t.Fatalf("key %s", key)
	}
	if !strings.Contains(label, "–") {
		t.Fatalf("label should show the range: %s", label)
	}
}

func TestSnapshotWindows(t *testing.T) {
	recs := []model.Usage{
		rec("2026-08-25", "claude", 10),
		rec("2026-08-24", "claude", 4),
		rec("2026-01-02", "claude", 7),
		rec("2025-06-01", "claude", 99),
	}
	today := time.Date(2026, 8, 25, 0, 0, 0, 0, zone)
	snap := Snapshot(recs, today)

	if got := snap["today"].Totals.Input; got != 10 {
		t.Fatalf("today %d", got)
	}
	// 2026-08-25 is a Tuesday, so the ISO week starts 08-24.
	if got := snap["week"].Totals.Input; got != 14 {
		t.Fatalf("week %d", got)
	}
	if got := snap["year"].Totals.Input; got != 21 {
		t.Fatalf("year %d", got)
	}
	if got := snap["all"].Totals.Input; got != 120 {
		t.Fatalf("all %d", got)
	}
	if got := snap["today"].Prev.Input; got != 4 {
		t.Fatalf("yesterday %d", got)
	}
	if len(snap["today"].Trend) != 14 {
		t.Fatalf("trend length %d", len(snap["today"].Trend))
	}
}

func TestSeriesZeroFillsGaps(t *testing.T) {
	recs := []model.Usage{rec("2026-08-25", "claude", 10), rec("2026-08-23", "claude", 5)}
	from := time.Date(2026, 8, 23, 0, 0, 0, 0, zone)
	to := time.Date(2026, 8, 25, 0, 0, 0, 0, zone)
	got := Series(recs, from, to)
	if len(got) != 3 {
		t.Fatalf("len %d", len(got))
	}
	if got[0] != 6 || got[1] != 0 || got[2] != 11 {
		t.Fatalf("series %v", got)
	}
}

func TestHeatmapMondayFirst(t *testing.T) {
	// 2026-08-25 is a Tuesday.
	recs := []model.Usage{recAt("2026-08-25 09:30", "claude", 10)}
	h := Heatmap(recs)
	local := recs[0].Time.In(time.Local)
	wd := (int(local.Weekday()) + 6) % 7
	if h.Cells[wd][local.Hour()] != 11 {
		t.Fatalf("cell not placed: %+v", h.Cells[wd])
	}
	if h.Total != 11 || h.Max != 11 || h.Min != 11 {
		t.Fatalf("total %d max %d min %d", h.Total, h.Max, h.Min)
	}
}

// Min must be the smallest *non-zero* cell: an empty hour is not a data point,
// and treating zero as the floor would flatten the shading.
func TestHeatmapMinIgnoresEmptyCells(t *testing.T) {
	recs := []model.Usage{
		recAt("2026-08-24 09:00", "claude", 999),
		recAt("2026-08-25 15:00", "claude", 50_000),
	}
	h := Heatmap(recs)
	if h.Min != 1000 {
		t.Fatalf("min %d, want the quietest active hour", h.Min)
	}
	if h.Max != 50_001 {
		t.Fatalf("max %d", h.Max)
	}
}

func TestGroupProjectRanksByVolume(t *testing.T) {
	mk := func(project string, in int64) model.Usage {
		u := rec("2026-08-25", "claude", in)
		u.Project = project
		return u
	}
	buckets := GroupProject([]model.Usage{mk("/a", 5), mk("/b", 50), mk("/b", 1)})
	if len(buckets) != 2 {
		t.Fatalf("len %d", len(buckets))
	}
	if buckets[0].Project != "/b" {
		t.Fatalf("largest first: %+v", buckets[0])
	}
	if buckets[0].Totals.Records != 2 {
		t.Fatalf("records %d", buckets[0].Totals.Records)
	}
}

func TestPeriodBoundsCoverExpectedWindow(t *testing.T) {
	today := time.Date(2026, 8, 25, 0, 0, 0, 0, zone)
	since, until := PeriodBounds(PeriodDay, today)
	if since == nil || until == nil {
		t.Fatal("day bounds should be set")
	}
	if got := int(until.Sub(*since).Hours() / 24); got != 13 {
		t.Fatalf("day window spans %d days", got)
	}
	if since, _ := PeriodBounds(PeriodAll, today); since != nil {
		t.Fatal("all should not clamp the start")
	}
}
