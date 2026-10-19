package aggregate

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the DST tests load named zones even where the system has none

	"token-count/internal/model"
)

var zone = time.FixedZone("CST", 8*3600)

// TestMain pins the local zone, which every date in this package is read in,
// so that results do not depend on where the tests happen to run.
func TestMain(m *testing.M) {
	time.Local = zone
	m.Run()
}

// setLocal makes the named zone local until the test ends.
func setLocal(tb testing.TB, name string) {
	tb.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		tb.Fatal(err)
	}
	prev := time.Local
	time.Local = loc
	tb.Cleanup(func() { time.Local = prev })
}

func rec(day, agent string, in int64) model.Usage {
	return recAt(day+" 12:00", agent, in)
}

// recAt stamps a record in the local zone, which is zone unless the test
// switched it with setLocal.
func recAt(stamp, agent string, in int64) model.Usage {
	ts, err := time.ParseInLocation("2006-01-02 15:04", stamp, time.Local)
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
	now := time.Date(2026, 8, 25, 18, 0, 0, 0, zone)
	snap := Snapshot(recs, now)

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

// A card compares the period so far with the same stretch of the previous
// one. Against the whole of last week, a Monday morning of steady usage would
// read as a fall of six sevenths.
func TestSnapshotComparesLikeForLike(t *testing.T) {
	var recs []model.Usage
	for d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); d.Month() < 11; d = d.AddDate(0, 0, 1) {
		recs = append(recs, recAt(d.Format("2006-01-02")+" 09:00", "claude", 100))
	}
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local) // a Monday
	snap := Snapshot(recs, now)
	for key, records := range map[string]int64{"today": 1, "week": 1, "month": 19} {
		s := snap[key]
		if s.Totals.Records != records || s.Prev != s.Totals {
			t.Fatalf("%s: %d records against %d before, want %d against as many",
				key, s.Totals.Records, s.Prev.Records, records)
		}
		if d := model.Change(s.Totals.Total(), s.Prev.Total()); !d.Known || d.Pct != 0 {
			t.Fatalf("%s change %v, want flat", key, d)
		}
	}
}

// Mid-month the card compares with last month up to the same day and hour;
// past the end of a shorter last month, with all of it.
func TestSnapshotMonthComparesUpToTheSameDay(t *testing.T) {
	var recs []model.Usage
	for d := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC); d.Month() < 4; d = d.AddDate(0, 0, 1) {
		recs = append(recs, recAt(d.Format("2006-01-02")+" 12:00", "claude", 100))
	}
	for _, c := range []struct {
		now           time.Time
		records, prev int64
	}{
		{time.Date(2026, 3, 15, 10, 0, 0, 0, time.Local), 15, 14}, // to Feb 15 10:00
		{time.Date(2026, 3, 31, 10, 0, 0, 0, time.Local), 31, 28}, // all of February
	} {
		got := Snapshot(recs, c.now)["month"]
		if got.Totals.Records != c.records || got.Prev.Records != c.prev {
			t.Fatalf("at %s: %d records against %d, want %d against %d",
				c.now, got.Totals.Records, got.Prev.Records, c.records, c.prev)
		}
	}
}

// Yesterday counts only up to the current clock time, end exclusive.
func TestSnapshotTodayComparesUpToTheClock(t *testing.T) {
	recs := []model.Usage{
		recAt("2026-08-24 09:59", "claude", 1),
		recAt("2026-08-24 10:00", "claude", 10),
		recAt("2026-08-24 10:01", "claude", 100),
		recAt("2026-08-24 23:00", "claude", 1000),
		recAt("2026-08-25 09:00", "claude", 5),
	}
	snap := Snapshot(recs, time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local))
	if got := snap["today"].Prev; got.Records != 1 || got.Input != 1 {
		t.Fatalf("yesterday by 10:00 is %+v, want only the 09:59 record", got)
	}
	if got := snap["today"].Totals.Records; got != 1 {
		t.Fatalf("today %d records", got)
	}
}

// PrevBounds covers as much of the previous period as has passed of the
// current one, measured on the wall clock.
func TestPrevBounds(t *testing.T) {
	for _, c := range []struct {
		zone       string // "" keeps zone
		period     Period
		now        string
		start, end string
	}{
		{"", PeriodToday, "2026-08-25 10:00", "2026-08-24 00:00", "2026-08-24 10:00"},
		{"", PeriodToday, "2026-08-25 00:00", "2026-08-24 00:00", "2026-08-24 00:00"},
		{"", PeriodWeek, "2026-10-19 10:00", "2026-10-12 00:00", "2026-10-12 10:00"}, // Monday
		{"", PeriodWeek, "2026-10-25 23:00", "2026-10-12 00:00", "2026-10-18 23:00"}, // Sunday
		{"", PeriodMonth, "2026-03-15 10:00", "2026-02-01 00:00", "2026-02-15 10:00"},
		{"", PeriodMonth, "2026-03-28 10:00", "2026-02-01 00:00", "2026-02-28 10:00"},
		// February has no 29th to 31st: past the 28th all of it counts.
		{"", PeriodMonth, "2026-03-29 10:00", "2026-02-01 00:00", "2026-03-01 00:00"},
		{"", PeriodMonth, "2026-03-31 10:00", "2026-02-01 00:00", "2026-03-01 00:00"},
		{"", PeriodMonth, "2028-03-29 10:00", "2028-02-01 00:00", "2028-02-29 10:00"},
		{"", PeriodMonth, "2028-03-30 10:00", "2028-02-01 00:00", "2028-03-01 00:00"},
		{"", PeriodMonth, "2026-01-31 10:00", "2025-12-01 00:00", "2025-12-31 10:00"},
		{"", PeriodYear, "2026-08-25 10:00", "2025-01-01 00:00", "2025-08-25 10:00"},
		{"", PeriodYear, "2028-02-29 10:00", "2027-01-01 00:00", "2027-03-01 00:00"},
		// Clocks went forward between the cut and now, so stepping back 24
		// hours or 7 days would cut at 09:00.
		{"America/New_York", PeriodToday, "2026-03-08 10:00", "2026-03-07 00:00", "2026-03-07 10:00"},
		{"America/New_York", PeriodWeek, "2026-03-08 10:00", "2026-02-23 00:00", "2026-03-01 10:00"},
		// Santiago skipped 2026-09-06 00:00 to 01:00: at 00:30 nothing of
		// that day had passed yet.
		{"America/Santiago", PeriodToday, "2026-09-07 00:30", "2026-09-06 01:00", "2026-09-06 01:00"},
		// Clocks went back from 02:00 to 01:00, so the clock showed 01:30
		// twice; the cut is the first time. In London time.Date would give
		// the second.
		{"America/New_York", PeriodToday, "2026-11-02 01:30", "2026-11-01 00:00", "2026-11-01 01:30 -0400"},
		{"Europe/London", PeriodToday, "2026-10-26 01:30", "2026-10-25 00:00", "2026-10-25 01:30 +0100"},
		{"Europe/London", PeriodWeek, "2026-11-01 01:30", "2026-10-19 00:00", "2026-10-25 01:30 +0100"},
	} {
		t.Run(fmt.Sprintf("%s %s %s", c.zone, c.period, c.now), func(t *testing.T) {
			if c.zone != "" {
				setLocal(t, c.zone)
			}
			// A time with an offset picks one of two instants the clock
			// showed it.
			at := func(s string) time.Time {
				layout := "2006-01-02 15:04"
				if len(s) > len(layout) {
					layout += " -0700"
				}
				ts, err := time.ParseInLocation(layout, s, time.Local)
				if err != nil {
					t.Fatal(err)
				}
				return ts
			}
			start, end := PrevBounds(c.period, at(c.now))
			if !start.Equal(at(c.start)) || !end.Equal(at(c.end)) {
				t.Fatalf("got [%s, %s), want [%s, %s)", start, end, at(c.start), at(c.end))
			}
		})
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

// A local day lasts 23 or 25 hours across a DST change, 23.5 or 24.5 on Lord
// Howe Island, and where the change skips midnight (Santiago) the day starts
// at 01:00. Counting days by dividing a duration by 24 hours merges two of
// them on the short day.
func TestSeriesKeepsDaysApartAcrossDST(t *testing.T) {
	for _, c := range []struct{ zone, change string }{
		{"America/New_York", "2026-03-08"},    // spring forward
		{"America/New_York", "2026-11-01"},    // fall back
		{"Europe/London", "2026-03-29"},       // spring forward
		{"Europe/London", "2026-10-25"},       // fall back
		{"Australia/Lord_Howe", "2026-10-04"}, // forward 30 minutes
		{"Australia/Lord_Howe", "2026-04-05"}, // back 30 minutes
		{"America/Santiago", "2026-09-06"},    // 2026-09-05 24:00 becomes 09-06 01:00
	} {
		t.Run(c.zone+" "+c.change, func(t *testing.T) {
			setLocal(t, c.zone)
			mid, _ := time.Parse("2006-01-02", c.change)
			before := mid.AddDate(0, 0, -1).Format("2006-01-02")
			after := mid.AddDate(0, 0, 1).Format("2006-01-02")
			recs := []model.Usage{
				rec(before, "claude", 10),
				rec(c.change, "claude", 20),
				rec(after, "claude", 30),
			}
			from, _ := time.ParseInLocation("2006-01-02", before, time.Local)
			to, _ := time.ParseInLocation("2006-01-02", after, time.Local)
			got := Series(recs, from, to)
			if want := []int64{11, 21, 31}; !slices.Equal(got, want) {
				t.Fatalf("series %v, want %v", got, want)
			}
		})
	}
}

// Where a DST change skips midnight west of Greenwich, time.Date moves that
// midnight back into the evening before, so reading a date off it lands on
// the wrong day.
func TestGroupTimeKeepsDaysApartWhereMidnightIsSkipped(t *testing.T) {
	setLocal(t, "America/Santiago") // 2026-09-05 24:00 becomes 09-06 01:00
	recs := []model.Usage{
		rec("2026-09-05", "claude", 10),
		rec("2026-09-06", "claude", 20),
		rec("2026-09-07", "claude", 30),
	}
	var keys []string
	for _, b := range GroupTime(recs, PeriodDay, false) {
		keys = append(keys, b.Key)
	}
	if want := []string{"2026-09-07", "2026-09-06", "2026-09-05"}; !slices.Equal(keys, want) {
		t.Fatalf("day rows %v, want %v", keys, want)
	}
}

// The helpers that return a local midnight must read their own results back
// as the day they stand for, even where that midnight was skipped.
func TestCalendarHelpersWhereMidnightIsSkipped(t *testing.T) {
	setLocal(t, "America/Asuncion") // 2023-09-30 24:00 became 10-01 01:00
	now := time.Date(2023, 10, 1, 10, 0, 0, 0, time.Local)
	oct1 := time.Date(2023, 10, 1, 0, 0, 0, 0, time.Local) // reads 09-30 23:00

	if got := Day(now); !got.Equal(oct1) {
		t.Fatalf("Day %s, want %s", got, oct1)
	}
	if got := Day(Day(now)); !got.Equal(oct1) {
		t.Fatalf("Day(Day) %s, want %s", got, oct1)
	}
	if got := StartOfMonth(now); !got.Equal(oct1) {
		t.Fatalf("StartOfMonth %s, want %s", got, oct1)
	}
	if got := StartOfISOWeek(Day(now)); !got.Equal(time.Date(2023, 9, 25, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("StartOfISOWeek %s", got)
	}
	if key, _ := BucketKey(PeriodMonth, now); key != "2023-10" {
		t.Fatalf("month key %s", key)
	}
	for _, c := range []struct {
		period Period
		since  time.Time
	}{
		{PeriodToday, oct1},
		{PeriodDay, time.Date(2023, 9, 18, 0, 0, 0, 0, time.Local)},
		{PeriodMonth, time.Date(2022, 11, 1, 0, 0, 0, 0, time.Local)},
	} {
		for _, today := range []time.Time{now, Day(now)} {
			since, until := PeriodBounds(c.period, today)
			if !since.Equal(c.since) || !until.Equal(oct1) {
				t.Fatalf("%s bounds for %s: %s..%s, want %s..%s", c.period, today, since, until, c.since, oct1)
			}
		}
	}
}

// Apia skipped the whole of 2011-12-30 when it moved across the date line, so
// time.Date gives that day's midnight and the 29th's as the same instant. It
// must still stand for the 29th, the day that happened.
func TestCalendarHelpersWhereADayIsSkipped(t *testing.T) {
	setLocal(t, "Pacific/Apia")
	dec29 := time.Date(2011, 12, 29, 0, 0, 0, 0, time.Local)
	dec31 := time.Date(2011, 12, 31, 0, 0, 0, 0, time.Local)
	now := time.Date(2011, 12, 29, 12, 0, 0, 0, time.Local)
	recs := []model.Usage{rec("2011-12-29", "claude", 100), rec("2011-12-31", "claude", 200)}

	if got := Day(Day(now)); !got.Equal(dec29) {
		t.Fatalf("Day(Day) %s, want %s", got, dec29)
	}
	// The 30th keeps its place in the series, empty.
	if got := Series(recs, dec29, dec31); !slices.Equal(got, []int64{101, 0, 201}) {
		t.Fatalf("series %v, want [101 0 201]", got)
	}
	if got := Snapshot(recs, Day(now))["today"].Totals.Input; got != 100 {
		t.Fatalf("today %d, want the 12-29 record", got)
	}
	since, until := PeriodBounds(PeriodDay, Day(now))
	if want := time.Date(2011, 12, 16, 0, 0, 0, 0, time.Local); !since.Equal(want) || !until.Equal(dec29) {
		t.Fatalf("day bounds %s..%s, want %s..%s", since, until, want, dec29)
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
	// Compare dates rather than divide the span by 24 hours, which DST breaks.
	if got := since.Format("2006-01-02") + ".." + until.Format("2006-01-02"); got != "2026-08-12..2026-08-25" {
		t.Fatalf("day window %s, want the 14 days to 2026-08-25", got)
	}
	if since, _ := PeriodBounds(PeriodAll, today); since != nil {
		t.Fatal("all should not clamp the start")
	}
}
