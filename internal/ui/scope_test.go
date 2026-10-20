package ui

import (
	"bytes"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"token-count/internal/aggregate"
	"token-count/internal/model"
	"token-count/internal/source"
)

// line is the visible text of one line of the frame, without trailing space.
func line(m Model, i int) string {
	lines := strings.Split(m.View(), "\n")
	if i >= len(lines) {
		return ""
	}
	return strings.TrimRight(stripANSI(lines[i]), " ")
}

// resized is m redrawn at another size, as a resize of the terminal does.
func resized(m Model, w, h int) Model {
	return send(m, tea.WindowSizeMsg{Width: w, Height: h})
}

// The line under the tab bar says what the table covers: the dates the
// window admits, how many days that is, and the offset they are local to.
// The span tab alone did not say which days "day" meant, and nothing said
// which zone decided where a day begins.
func TestScopeLineNamesTheWindow(t *testing.T) {
	pinLocal(t) // before the bounds below, which are local midnights
	since := aggregate.Day(testNow).AddDate(0, 0, -5)
	until := aggregate.Day(testNow).AddDate(0, 0, -2)
	for _, c := range []struct {
		name string
		edit func(m *Model)
		want string
	}{
		{"day", func(m *Model) {}, " 2026-08-12 → 2026-08-25 · 14 days · UTC+08:00"},
		{"today", func(m *Model) { m.period = aggregate.PeriodToday }, " 2026-08-25 · today · UTC+08:00"},
		{"week", func(m *Model) { m.period = aggregate.PeriodWeek }, " 2026-07-06 → 2026-08-25 · 51 days · UTC+08:00"},
		{"month", func(m *Model) { m.period = aggregate.PeriodMonth }, " 2025-09-01 → 2026-08-25 · 359 days · UTC+08:00"},
		// An open start has no date of its own: the earliest record in
		// scope stands in for it.
		{"all", func(m *Model) { m.period = aggregate.PeriodAll }, " since 2026-08-22 → 2026-08-25 · UTC+08:00"},
		{"year", func(m *Model) { m.period = aggregate.PeriodYear }, " since 2026-08-22 → 2026-08-25 · UTC+08:00"},
		{"all, nothing in scope", func(m *Model) { m.period, m.agent = aggregate.PeriodAll, "gemini" },
			" all history → 2026-08-25 · UTC+08:00 · gemini"},
		{"explicit dates", func(m *Model) { m.opts.Since, m.opts.Until = &since, &until },
			" 2026-08-20 → 2026-08-23 · 4 days · UTC+08:00"},
		{"--since alone", func(m *Model) { m.opts.Since = &since }, " from 2026-08-20 · UTC+08:00"},
	} {
		m := newTestModel(t, 120, 30)
		c.edit(&m)
		m.recompute()
		if got := line(m, 2); got != c.want {
			t.Errorf("%s: scope line\n %q, want\n %q", c.name, got, c.want)
		}
	}
}

// The filters narrowing the table follow the window on the scope line, so an
// unexpected number always has a visible cause. When the line is too narrow,
// the window gives way first: its day count, then the offset, then the year,
// then the window itself. The filters keep their places longest, the search
// term above all, then the agent, the model split and the sort.
func TestScopeLineKeepsFiltersLongest(t *testing.T) {
	m := newTestModel(t, 120, 30)
	m = send(m, runes("a"), runes("a"), runes("m"), runes("s"), runes("/"), runes("cod"), key(tea.KeyEnter))
	for _, c := range []struct {
		w    int
		want string
	}{
		{120, " 2026-08-12 → 2026-08-25 · 14 days · UTC+08:00 · codex · by model · ↓total · /cod"},
		{81, " 2026-08-12 → 2026-08-25 · 14 days · UTC+08:00 · codex · by model · ↓total · /cod"},
		{80, " 2026-08-12 → 2026-08-25 · UTC+08:00 · codex · by model · ↓total · /cod"},
		{71, " 2026-08-12 → 2026-08-25 · UTC+08:00 · codex · by model · ↓total · /cod"},
		{70, " 2026-08-12 → 2026-08-25 · codex · by model · ↓total · /cod"},
		{59, " 2026-08-12 → 2026-08-25 · codex · by model · ↓total · /cod"},
		{58, " 08-12 → 08-25 · codex · by model · ↓total · /cod"},
		{49, " 08-12 → 08-25 · codex · by model · ↓total · /cod"},
		{48, " codex · by model · ↓total · /cod"},
		{33, " codex · by model · ↓total · /cod"},
		{32, " codex · by model · /cod"},
		{30, " codex · by model · /cod"},
	} {
		if got := line(resized(m, c.w, 30), 2); got != c.want {
			t.Errorf("%d columns: scope line\n %q, want\n %q", c.w, got, c.want)
		}
	}
	// A search term too long for any line is cut, never dropped.
	long := send(m, runes("/"), runes(strings.Repeat("x", 40)), key(tea.KeyEnter))
	if got := line(resized(long, 30, 30), 2); !strings.HasPrefix(got, " /codxxx") || !strings.HasSuffix(got, "…") {
		t.Errorf("a long search term at 30 columns: %q", got)
	}
	// The agent keeps its colour.
	withFancy(t, true)
	if !strings.Contains(m.View(), agentStyle("codex").Render("codex")) {
		t.Error("the agent filter is not drawn in the agent's colour")
	}
}

// While the search box is open it takes the place of the scope line, and
// says how many records match and how to leave it. The hints give way to the
// term on a narrow terminal; the term is never what goes.
func TestSearchBoxTakesTheScopeLine(t *testing.T) {
	m := send(newTestModel(t, 120, 30), runes("/"), runes("cod"))
	got := line(m, 2)
	if !strings.HasPrefix(got, " / cod") || !strings.HasSuffix(got, "4 records match · enter keep · esc clear") {
		t.Errorf("120 columns: %q", got)
	}
	if got = line(resized(m, 40, 30), 2); !strings.HasPrefix(got, " / cod") ||
		!strings.HasSuffix(got, "4 records match") {
		t.Errorf("40 columns: %q", got)
	}
	long := strings.Repeat("y", 50)
	m = send(m, runes(long))
	if got = line(resized(m, 80, 30), 2); !strings.Contains(got, "cod"+long) || strings.Contains(got, "enter keep") {
		t.Errorf("a long term at 80 columns is not shown whole: %q", got)
	}
	if got = line(resized(m, 30, 30), 2); !strings.HasPrefix(got, " / ") || !strings.Contains(got, "yyyy") {
		t.Errorf("a long term at 30 columns: %q", got)
	}
	if m = send(m, key(tea.KeyEnter)); strings.HasPrefix(line(m, 2), " / ") {
		t.Errorf("enter left the search box on the scope line: %q", line(m, 2))
	}
}

// tabWords are the words the tab bar may show: the tabs, the divider, and
// the minimal strip's brackets and keys.
var tabWords = map[string]bool{
	"today": true, "day": true, "week": true, "month": true, "year": true, "all": true,
	"time": true, "project": true, "model": true, "hours": true,
	"│": true, "‹": true, "›": true, "←→": true, "tab": true,
}

// The tab bar never cuts a label in half. When the strip does not fit it
// drops the padding of the inactive tabs, and then shows only the active
// span and view with the keys that move them. It used to be cut at the
// edge, mid-word, and from 80 columns down the filter chips beside it hid
// the search term.
func TestTabBarNeverCutsALabel(t *testing.T) {
	for _, fancy := range []bool{false, true} {
		withFancy(t, fancy)
		for _, c := range []struct {
			period aggregate.Period
			view   aggregate.View
		}{
			{aggregate.PeriodDay, aggregate.ViewTime},
			{aggregate.PeriodMonth, aggregate.ViewProject},
			{aggregate.PeriodToday, aggregate.ViewHeat},
		} {
			m := newTestModel(t, 120, 30)
			m.period, m.view, m.cat = c.period, c.view, false
			m.recompute()
			for w := minWidth; w <= 120; w++ {
				bar := m.tabBar(w, newStyles())
				if got := lipgloss.Width(bar); got > w {
					t.Fatalf("%s/%s at %d: %d wide", c.period, c.view, w, got)
				}
				vis := stripANSI(bar)
				for _, word := range strings.Fields(vis) {
					if !tabWords[word] {
						t.Fatalf("%s/%s at %d: %q is not a whole label: %q", c.period, c.view, w, word, vis)
					}
				}
				if !strings.Contains(vis, " "+string(c.period)+" ") || !strings.Contains(vis, " "+viewLabel(c.view)+" ") {
					t.Fatalf("%s/%s at %d: the active tabs are missing: %q", c.period, c.view, w, vis)
				}
			}
		}
	}
	m := newTestModel(t, 120, 30)
	m.cat = false
	for _, c := range []struct {
		w    int
		want string
	}{
		{72, "  today  day  week  month  year  all    │   time  project  model  hours"},
		{71, " today day week month year all │ time project model hours"},
		{57, " today day week month year all │ time project model hours"},
		{56, " ‹ day › ←→   ‹ time › tab"},
	} {
		if got := strings.TrimRight(stripANSI(m.tabBar(c.w, newStyles())), " "); got != c.want {
			t.Errorf("%d columns: tab bar\n %q, want\n %q", c.w, got, c.want)
		}
	}
	m.period, m.view = aggregate.PeriodMonth, aggregate.ViewProject
	if got := strings.TrimRight(stripANSI(m.tabBar(30, newStyles())), " "); got != " ‹ month ›   ‹ project ›" {
		t.Errorf("30 columns, month by project: %q", got)
	}
}

// The filters moved to the scope line; the tab bar carries only the tabs and
// the mascot's mood.
func TestTabBarCarriesNoFilters(t *testing.T) {
	m := send(newTestModel(t, 120, 30), runes("a"), runes("m"), runes("s"), runes("/"), runes("zz"), key(tea.KeyEnter))
	if bar := stripANSI(m.tabBar(120, newStyles())); strings.Contains(bar, "claude") ||
		strings.Contains(bar, "by-model") || strings.Contains(bar, "↓") || strings.Contains(bar, "/zz") {
		t.Errorf("the tab bar still carries filter chips: %q", bar)
	}
	withFancy(t, true)
	m = newTestModel(t, 120, 30)
	bar := stripANSI(m.tabBar(120, newStyles()))
	mood := false
	for _, word := range []string{"napping", "content", "busy", "wired"} {
		mood = mood || strings.Contains(bar, " "+word+" ")
	}
	if !mood {
		t.Errorf("the mood chip is gone from the tab bar: %q", bar)
	}
}

// The cards keep their own scope, which differs from the table's, so a
// caption names it: they would otherwise read as the result of the search
// typed just above them.
func TestOverviewCaptionNamesTheCardsScope(t *testing.T) {
	pinLocal(t)
	since := aggregate.Day(testNow).AddDate(0, 0, -5)
	for _, c := range []struct {
		name string
		edit func(m *Model)
		want string
	}{
		{"default", func(m *Model) {}, " OVERVIEW · all agents · not narrowed by span or search"},
		{"agent", func(m *Model) { m.agent = "codex" }, " OVERVIEW · codex · not narrowed by span or search"},
		{"dates", func(m *Model) { m.opts.Since = &since },
			" OVERVIEW · all agents · within --since/--until · not narrowed by span or search"},
		// A load limited with -a covers only those agents; "all agents"
		// claimed the others too.
		{"-a subset", func(m *Model) { m.opts.Agents = []string{"claude", "opencode"} },
			" OVERVIEW · claude,opencode · not narrowed by span or search"},
		{"-a subset, agent", func(m *Model) { m.opts.Agents, m.agent = []string{"claude", "opencode"}, "claude" },
			" OVERVIEW · claude · not narrowed by span or search"},
	} {
		m := newTestModel(t, 120, 30)
		c.edit(&m)
		m.recompute()
		if got := line(m, 3); got != c.want {
			t.Errorf("%s: caption %q, want %q", c.name, got, c.want)
		}
	}
	m := newTestModel(t, 120, 30)
	cards := stripANSI(strings.Join(strings.Split(m.View(), "\n")[4:8], "\n"))
	for _, label := range []string{"TODAY", "THIS WEEK", "vs yest", "vs last wk", "vs last mo"} {
		if !strings.Contains(cards, label) {
			t.Errorf("the cards lost %q:\n%s", label, cards)
		}
	}
}

// The help overlay explains what the card deltas compare: the same elapsed
// part of the previous day, week or month, not the whole of it.
func TestHelpExplainsLikeForLikeDeltas(t *testing.T) {
	m := newTestModel(t, 120, 50)
	m.help = true
	if body := stripANSI(m.View()); !strings.Contains(body, "like for like") {
		t.Errorf("the help does not explain the card deltas:\n%s", body)
	}
}

// The total row says it sums the table's range, where its name column has
// room for that, in the TUI and in --plain alike.
func TestTotalRowSaysRangeTotal(t *testing.T) {
	m := newTestModel(t, 120, 30)
	if body := stripANSI(m.View()); !strings.Contains(body, " RANGE TOTAL ") {
		t.Errorf("120 columns: no RANGE TOTAL:\n%s", body)
	}
	// Split by model at 34 columns the name column is 8 cells wide.
	m.byModel = true
	m.recompute()
	lines := m.tableView(34, newStyles())
	if total := stripANSI(lines[len(lines)-1]); !strings.HasPrefix(total, " TOTAL ") {
		t.Errorf("34 columns by model: total row %q, want TOTAL", total)
	}

	pinLocal(t)
	opts := Options{Period: aggregate.PeriodAll, View: aggregate.ViewProject, Now: testClock}
	var buf bytes.Buffer
	WritePlain(&buf, opts, BuildExport(sample(testNow), opts), source.Stats{})
	if !strings.Contains(stripANSI(buf.String()), "\n RANGE TOTAL ") {
		t.Errorf("--plain has no RANGE TOTAL:\n%s", stripANSI(buf.String()))
	}
}

// --plain opens with what it covers: the span and view, the dates, the
// offset and the filters, and labels the cards as the overview they are.
func TestPlainHeaderNamesTheWindowAndFilters(t *testing.T) {
	pinLocal(t)
	recs := sample(testNow)
	since := aggregate.Day(testNow).AddDate(0, 0, -5)
	for _, c := range []struct {
		opts Options
		want string
	}{
		{Options{Period: aggregate.PeriodDay},
			"tcount  ·  day  ·  time  ·  2026-08-12 → 2026-08-25  ·  UTC+08:00"},
		{Options{Period: aggregate.PeriodToday, View: aggregate.ViewModel, Name: "tsum"},
			"tsum  ·  today  ·  model  ·  2026-08-25  ·  UTC+08:00"},
		{Options{Period: aggregate.PeriodAll, View: aggregate.ViewProject, Agents: []string{"claude", "codex"}},
			"tcount  ·  all  ·  project  ·  since 2026-08-22 → 2026-08-25  ·  UTC+08:00  ·  claude,codex"},
		{Options{Period: aggregate.PeriodDay, Since: &since, ByModel: true, Agents: model.Agents},
			"tcount  ·  day  ·  time  ·  from 2026-08-20  ·  UTC+08:00  ·  by model"},
	} {
		c.opts.Now = testClock
		var buf bytes.Buffer
		WritePlain(&buf, c.opts, BuildExport(recs, c.opts), source.Stats{})
		lines := strings.Split(stripANSI(buf.String()), "\n")
		if got := strings.TrimRight(lines[0], " "); got != c.want {
			t.Errorf("header\n %q, want\n %q", got, c.want)
		}
		cards := ""
		for _, ln := range lines {
			if strings.Contains(ln, "TODAY") {
				cards = ln
			}
		}
		if !strings.HasPrefix(cards, " overview  TODAY ") {
			t.Errorf("the cards line is not labelled as the overview: %q", cards)
		}
	}
}

// A load limited with -a is a filter like any other, so the scope line names
// the agents it covers. It said nothing, and the figures read as everyone's.
func TestScopeLineNamesTheAgentSubset(t *testing.T) {
	m := newTestModel(t, 120, 30)
	m.opts.Agents = []string{"claude", "opencode"}
	m.recompute()
	if got, want := line(m, 2), " 2026-08-12 → 2026-08-25 · 14 days · UTC+08:00 · claude,opencode"; got != want {
		t.Errorf("scope line\n %q, want\n %q", got, want)
	}
	// An agent picked with a names the narrower scope by itself.
	m = send(m, runes("a"))
	if got, want := line(m, 2), " 2026-08-12 → 2026-08-25 · 14 days · UTC+08:00 · claude"; got != want {
		t.Errorf("scope line with an agent picked\n %q, want\n %q", got, want)
	}
	// Every agent named is no subset.
	m = newTestModel(t, 120, 30)
	m.opts.Agents = append([]string{}, model.Agents...)
	m.recompute()
	if got, want := line(m, 2), " 2026-08-12 → 2026-08-25 · 14 days · UTC+08:00"; got != want {
		t.Errorf("scope line with every agent\n %q, want\n %q", got, want)
	}
}
