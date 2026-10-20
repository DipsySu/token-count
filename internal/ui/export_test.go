package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the DST test loads a named zone even where the system has none

	"token-count/internal/aggregate"
	"token-count/internal/model"
	"token-count/internal/source"
)

// usageAt is one record of in input tokens, made on the pinned clock and zone.
func usageAt(t *testing.T, agent string, at time.Time, in int64) model.Usage {
	t.Helper()
	u, ok := model.Make(model.MakeOpts{
		Agent:   agent,
		Time:    at,
		Model:   agent + "-model",
		Project: "/w/" + agent,
		Input:   in,
	})
	if !ok {
		t.Fatalf("record for %s at %v was rejected", agent, at)
	}
	return u
}

// report runs the --json path end to end and decodes what it printed.
func report(t *testing.T, recs []model.Usage, opts Options) Report {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteJSON(&buf, opts, BuildExport(recs, opts), source.Stats{}); err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("decoding %s: %v", buf.String(), err)
	}
	return rep
}

func rowsTotal(rows []ReportRow) int64 {
	var n int64
	for _, r := range rows {
		n += r.Total
	}
	return n
}

func deref(s *string) string {
	if s == nil {
		return "<none>"
	}
	return *s
}

// A text export must print every row, however many there are. It used to
// reuse the TUI's screenful (a fixed height of 1000 lines), so 1,100 projects
// printed only 494 of them while TOTAL still counted all 1,100.
func TestPlainPrintsEveryRow(t *testing.T) {
	pinLocal(t)
	const projects = 1100
	var recs []model.Usage
	for i := 0; i < projects; i++ {
		// Each project totals 2 tokens, so every figure prints exactly,
		// without the k/M rounding that would hide a missing row.
		u, ok := model.Make(model.MakeOpts{
			Agent:   "claude",
			Time:    testNow.Add(-time.Hour),
			Project: fmt.Sprintf("/w/p%04d", i),
			Input:   1,
			Output:  1,
		})
		if !ok {
			t.Fatal("record rejected")
		}
		recs = append(recs, u)
	}
	opts := Options{Period: aggregate.PeriodAll, View: aggregate.ViewProject, Now: testClock}
	// A narrow export ends each project line on the TOTAL column.
	parent := regexp.MustCompile(`^\s*\S+\s+/w/p(\d{4})\s+(\S+)(\s|$)`)
	for _, width := range []int{50, 100} {
		withExportWidth(t, width)
		var buf bytes.Buffer
		WritePlain(&buf, opts, BuildExport(recs, opts), source.Stats{})

		printed := map[string]int{}
		var parentSum int64
		children := 0
		total := int64(-1)
		for _, ln := range strings.Split(stripANSI(buf.String()), "\n") {
			if m := parent.FindStringSubmatch(ln); m != nil {
				printed[m[1]]++
				n, err := strconv.ParseInt(strings.ReplaceAll(m[2], ",", ""), 10, 64)
				if err != nil {
					t.Fatalf("unreadable total in %q", ln)
				}
				parentSum += n
			}
			if strings.Contains(ln, "└ claude") {
				children++
			}
			if f := strings.Fields(ln); len(f) > 2 && f[0] == "RANGE" && f[1] == "TOTAL" {
				total, _ = strconv.ParseInt(strings.ReplaceAll(f[2], ",", ""), 10, 64)
			}
		}
		if len(printed) != projects {
			t.Errorf("%d columns: printed %d of %d projects", width, len(printed), projects)
		}
		for p, n := range printed {
			if n != 1 {
				t.Errorf("%d columns: project %s printed %d times", width, p, n)
			}
		}
		if children != projects {
			t.Errorf("%d columns: printed %d of %d child rows", width, children, projects)
		}
		if total != 2*projects || total != parentSum {
			t.Errorf("%d columns: TOTAL %d; the printed parents add up to %d, the records to %d",
				width, total, parentSum, 2*projects)
		}
	}
}

// The review's case: 10 tokens today and 100 earlier. A "today" report has to
// describe today alone in every field, in every view. The heat view used to
// grid today while its rows and calls covered all history (heat.total 10, the
// rows 110), and every view counted all loaded records.
func TestReportScopesEveryFieldToTheWindow(t *testing.T) {
	pinLocal(t)
	recs := []model.Usage{
		usageAt(t, "claude", testNow.Add(-time.Hour), 10),
		usageAt(t, "codex", testNow.AddDate(0, 0, -3), 100),
	}
	for _, view := range views() {
		rep := report(t, recs, Options{Period: aggregate.PeriodToday, View: view, Now: testClock})
		if got := rowsTotal(rep.Rows); got != 10 {
			t.Errorf("%s: rows add up to %d, want 10", view, got)
		}
		if rep.Calls != 1 || rep.Records != 1 {
			t.Errorf("%s: calls %d, records %d; want 1 and 1", view, rep.Calls, rep.Records)
		}
		if view == aggregate.ViewHeat {
			if rep.Heat == nil || rep.Heat.Total != 10 {
				t.Errorf("%s: heat %+v, want a total of 10", view, rep.Heat)
			}
		} else if rep.Heat != nil {
			t.Errorf("%s: only the heat view carries a grid", view)
		}
		// The window is reported even though no dates were passed.
		if deref(rep.Since) != "2026-08-25" || deref(rep.Until) != "2026-08-25" {
			t.Errorf("%s: window %s..%s, want 2026-08-25..2026-08-25", view, deref(rep.Since), deref(rep.Until))
		}
		if rep.Date != "2026-08-25" || rep.UTCOffset != "+08:00" {
			t.Errorf("%s: date %q offset %q, want 2026-08-25 +08:00", view, rep.Date, rep.UTCOffset)
		}
	}
}

// Explicit dates replace the span's window, for the rows, the grid and the
// counts alike, and the report says which dates it used.
func TestReportHonoursExplicitDates(t *testing.T) {
	pinLocal(t)
	recs := []model.Usage{
		usageAt(t, "claude", testNow.Add(-time.Hour), 10),
		usageAt(t, "codex", testNow.AddDate(0, 0, -3), 100),
		usageAt(t, "grok", testNow.AddDate(0, 0, -10), 1000),
	}
	since := aggregate.Day(testNow).AddDate(0, 0, -5)
	until := aggregate.Day(testNow).AddDate(0, 0, -2)

	for _, view := range views() {
		rep := report(t, recs, Options{Period: aggregate.PeriodToday, View: view, Since: &since, Until: &until, Now: testClock})
		if got := rowsTotal(rep.Rows); got != 100 || rep.Records != 1 || rep.Calls != 1 {
			t.Errorf("%s: rows %d, records %d, calls %d; want 100, 1, 1", view, got, rep.Records, rep.Calls)
		}
		if view == aggregate.ViewHeat && (rep.Heat == nil || rep.Heat.Total != 100) {
			t.Errorf("%s: heat %+v, want a total of 100", view, rep.Heat)
		}
		if deref(rep.Since) != "2026-08-20" || deref(rep.Until) != "2026-08-23" {
			t.Errorf("%s: window %s..%s, want 2026-08-20..2026-08-23", view, deref(rep.Since), deref(rep.Until))
		}
	}

	// A side without a bound is left out rather than invented.
	rep := report(t, recs, Options{Period: aggregate.PeriodToday, Since: &since, Now: testClock})
	if deref(rep.Since) != "2026-08-20" || rep.Until != nil || rep.Records != 2 {
		t.Errorf("--since only: window %s..%s with %d records, want 2026-08-20..<none> with 2",
			deref(rep.Since), deref(rep.Until), rep.Records)
	}
	rep = report(t, recs, Options{Period: aggregate.PeriodAll, View: aggregate.ViewProject, Now: testClock})
	if rep.Since != nil || deref(rep.Until) != "2026-08-25" || rep.Records != 3 || rowsTotal(rep.Rows) != 1110 {
		t.Errorf("all time: window %s..%s, %d records, rows %d; want <none>..2026-08-25, 3, 1110",
			deref(rep.Since), deref(rep.Until), rep.Records, rowsTotal(rep.Rows))
	}
}

// The report names the dates its window admits, not the dates its bounds
// happen to fall on. A bound is the instant time.Date gives for a local
// midnight, and where a DST change skips midnight time.Date moves it into the
// evening before: Santiago jumps from 00:00 to 01:00 on 6 September 2026, so
// the bound standing for the 6th is 23:00 on the 5th, and formatting it named
// the 5th while the rows held only the 6th.
func TestReportNamesTheDatesASkippedMidnightStandsFor(t *testing.T) {
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	pinZone(t, santiago)
	sixth := time.Date(2026, 9, 6, 0, 0, 0, 0, santiago)
	if sixth.Day() != 5 {
		t.Skipf("the zone data no longer skips midnight on 2026-09-06: %v", sixth)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, santiago)
	clock := func() time.Time { return now }
	recs := []model.Usage{
		usageAt(t, "claude", now.Add(-2*time.Hour), 10),
		// Half an hour after the bound's instant, but still on the 5th.
		usageAt(t, "codex", time.Date(2026, 9, 5, 23, 30, 0, 0, santiago), 100),
		usageAt(t, "grok", time.Date(2026, 8, 24, 10, 0, 0, 0, santiago), 1000),
		usageAt(t, "gemini", time.Date(2026, 8, 23, 10, 0, 0, 0, santiago), 10000),
	}
	for _, c := range []struct {
		name         string
		opts         Options
		since, until string
		rows         int64
	}{
		{"today", Options{Period: aggregate.PeriodToday}, "2026-09-06", "2026-09-06", 10},
		{"day", Options{Period: aggregate.PeriodDay}, "2026-08-24", "2026-09-06", 1110},
		{"--since 2026-09-06", Options{Period: aggregate.PeriodToday, Since: &sixth}, "2026-09-06", "<none>", 10},
		{"--until 2026-09-06", Options{Period: aggregate.PeriodToday, Until: &sixth}, "<none>", "2026-09-06", 11110},
	} {
		c.opts.Now = clock
		rep := report(t, recs, c.opts)
		if deref(rep.Since) != c.since || deref(rep.Until) != c.until || rowsTotal(rep.Rows) != c.rows {
			t.Errorf("%s: reported %s..%s over rows of %d; want %s..%s over %d",
				c.name, deref(rep.Since), deref(rep.Until), rowsTotal(rep.Rows), c.since, c.until, c.rows)
		}
		if rep.Date != "2026-09-06" || rep.UTCOffset != "-03:00" {
			t.Errorf("%s: date %s at %s, want 2026-09-06 at -03:00", c.name, rep.Date, rep.UTCOffset)
		}
	}
}

// The plain heatmap reads the same window as everything else, from the full
// export or from the scoped one the command line hands it.
func TestPlainHeatUsesTheReportWindow(t *testing.T) {
	pinLocal(t)
	recs := []model.Usage{
		usageAt(t, "claude", testNow.Add(-time.Hour), 10),
		usageAt(t, "codex", testNow.AddDate(0, 0, -3), 100),
	}
	opts := Options{Period: aggregate.PeriodToday, View: aggregate.ViewHeat, Now: testClock}
	for name, ex := range map[string]Export{"scoped": ScopeExport(recs, opts), "built": BuildExport(recs, opts)} {
		var buf bytes.Buffer
		WriteHeat(&buf, opts, ex, source.Stats{})
		if out := stripANSI(buf.String()); !strings.Contains(out, "total 10\n") {
			t.Errorf("%s: today's plain heatmap should total 10:\n%s", name, out)
		}
	}
}

// The TUI and the exports have to scope and group identically, in every
// combination of span and view, or the two front ends tell different stories.
func TestTUIAndExportAgree(t *testing.T) {
	pinLocal(t)
	recs := append(sample(testNow),
		usageAt(t, "gemini", testNow.AddDate(0, 0, -20), 7),
		usageAt(t, "opencode", testNow.AddDate(-1, 0, 0), 9))
	for _, p := range periods() {
		for _, v := range views() {
			opts := Options{Period: p, View: v, Agents: model.Agents, Now: testClock}
			m := New(opts)
			m.loading = false
			m.setRecords(recs)
			m.recompute()
			ex := BuildExport(recs, opts)

			if len(m.scoped) != len(ex.Records) {
				t.Errorf("%s/%s: TUI scoped %d records, export %d", p, v, len(m.scoped), len(ex.Records))
			}
			if v == aggregate.ViewHeat {
				if m.heat != aggregate.Heatmap(ex.Records) {
					t.Errorf("%s/%s: heat grids differ", p, v)
				}
			} else if !reflect.DeepEqual(m.rows, ex.Rows) {
				t.Errorf("%s/%s: rows differ", p, v)
			}
			if !reflect.DeepEqual(m.since, ex.Since) || !reflect.DeepEqual(m.until, ex.Until) {
				t.Errorf("%s/%s: windows differ", p, v)
			}
			// The heatmap's lighter export has to cover the same ground.
			sc := ScopeExport(recs, opts)
			if !reflect.DeepEqual(sc.Records, ex.Records) || !reflect.DeepEqual(sc.Since, ex.Since) ||
				!reflect.DeepEqual(sc.Until, ex.Until) || sc.Rows != nil || sc.Snap != nil {
				t.Errorf("%s/%s: ScopeExport is not the window and records of BuildExport", p, v)
			}
		}
	}
}

// Splitting by model has to show the model in a text export too. It used to
// drop the MODEL column, which printed indistinguishable "└ claude" rows, and
// once that was fixed it still dropped it on a terminal under 82 columns.
func TestPlainByModelShowsTheModelColumn(t *testing.T) {
	pinLocal(t)
	var recs []model.Usage
	for _, m := range []string{"claude-opus-4", "claude-haiku-4"} {
		u, _ := model.Make(model.MakeOpts{Agent: "claude", Time: testNow.Add(-time.Hour), Model: m, Input: 100})
		recs = append(recs, u)
	}
	// 41 columns is the narrowest terminal an export follows; on a narrower
	// one it uses the width it gives a pipe.
	for _, width := range []int{41, 60, 81, 100} {
		withExportWidth(t, width)
		for _, p := range []aggregate.Period{aggregate.PeriodToday, aggregate.PeriodDay} {
			opts := Options{Period: p, View: aggregate.ViewTime, ByModel: true, Now: testClock}
			var buf bytes.Buffer
			WritePlain(&buf, opts, BuildExport(recs, opts), source.Stats{})
			out := stripANSI(buf.String())
			for _, m := range []string{"MODEL", "claude-opus-4", "claude-haiku-4"} {
				if !strings.Contains(out, m) {
					t.Errorf("%s --by-model --plain at %d columns does not show %s:\n%s", p, width, m, out)
				}
			}
		}
	}
}
