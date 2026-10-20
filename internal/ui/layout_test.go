package ui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

// testZone stands in for the local zone during tests. It has no daylight
// saving, so no test can straddle a clock change, and pinning it means day
// boundaries do not depend on the TZ the suite happens to run under.
var testZone = time.FixedZone("UTC+8", 8*3600)

// testNow is the instant the tests treat as now: a Tuesday afternoon, so
// records stamped that morning are already part of today.
var testNow = time.Date(2026, 8, 25, 15, 0, 0, 0, testZone)

func testClock() time.Time { return testNow }

// pinLocal makes testZone the local zone until the test ends. Records must be
// made after it is pinned, because model.Make converts them to local time.
func pinLocal(t testing.TB) {
	t.Helper()
	pinZone(t, testZone)
}

// pinZone makes loc the local zone until the test ends.
func pinZone(t testing.TB, loc *time.Location) {
	t.Helper()
	if time.Local == loc {
		return
	}
	prev := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = prev })
}

// sample is four days of usage by three agents, ending on the day of now, so
// that the fixture moves with whatever clock a test pins.
func sample(now time.Time) []model.Usage {
	var out []model.Usage
	base := aggregate.Day(now).Add(10 * time.Hour)
	for i, a := range []string{"claude", "codex", "grok"} {
		for d := 0; d < 4; d++ {
			u, ok := model.Make(model.MakeOpts{
				Agent:     a,
				Time:      base.AddDate(0, 0, -d),
				Model:     a + "-model",
				Project:   "/home/u/p" + string(rune('a'+i)),
				Input:     int64(1000 * (i + 1)),
				CacheRead: int64(50_000 * (d + 1)),
				Output:    int64(300 * (i + 1)),
				Reasoning: 50,
			})
			if ok {
				out = append(out, u)
			}
		}
	}
	return out
}

// newTestModel is a loaded model on the fixed clock and zone, sized w x h.
func newTestModel(t *testing.T, w, h int) Model {
	t.Helper()
	pinLocal(t)
	m := New(Options{Period: aggregate.PeriodDay, Agents: model.Agents, Version: "test", Now: testClock})
	m.width, m.height = w, h
	m.loading = false
	m.setRecords(sample(testNow))
	m.recompute()
	return m
}

// Every rendered line must fit the terminal, or the display wraps and the
// layout falls apart.
func TestViewFitsTerminalAtAnyWidth(t *testing.T) {
	for _, w := range []int{40, 52, 60, 72, 80, 96, 120, 200} {
		for _, h := range []int{12, 18, 24, 40} {
			m := newTestModel(t, w, h)
			for _, view := range []aggregate.View{aggregate.ViewTime, aggregate.ViewProject, aggregate.ViewModel, aggregate.ViewHeat} {
				m.view = view
				m.recompute()
				out := m.View()
				lines := strings.Split(out, "\n")
				if len(lines) != h {
					t.Fatalf("w=%d h=%d view=%s: %d lines, want %d", w, h, view, len(lines), h)
				}
				for i, ln := range lines {
					if got := lipgloss.Width(ln); got > w {
						t.Fatalf("w=%d h=%d view=%s line %d is %d wide: %q", w, h, view, i, got, ln)
					}
				}
			}
		}
	}
}

// Below the smallest size the layout is drawn for, the screen says what it
// needs, at the terminal's real size. It used to draw an 80x24 frame there,
// which the terminal cut down to its left columns and bottom rows, so the
// totals and the title were the first things lost.
func TestTinyTerminalSaysWhatItNeeds(t *testing.T) {
	withFancy(t, true)
	for _, size := range [][2]int{{1, 1}, {10, 3}, {20, 30}, {29, 40}, {29, 10}, {30, 9}, {80, 9}, {80, 5}, {200, 1}} {
		w, h := size[0], size[1]
		m := newTestModel(t, w, h)
		body := m.View()
		lines := strings.Split(body, "\n")
		if len(lines) != h {
			t.Errorf("%dx%d: %d lines", w, h, len(lines))
		}
		for i, ln := range lines {
			if got := lipgloss.Width(ln); got > w {
				t.Errorf("%dx%d: line %d is %d wide: %q", w, h, i, got, stripANSI(ln))
			}
			if leak := styleLeak(ln); leak != "" {
				t.Errorf("%dx%d: line %d: %s", w, h, i, leak)
			}
		}
		if vis := stripANSI(body); w >= 20 && h >= 2 &&
			(!strings.Contains(vis, "terminal too small") || !strings.Contains(vis, "need 30x10")) {
			t.Errorf("%dx%d does not say what it needs:\n%s", w, h, vis)
		}
	}
	// At the minimum itself the table is drawn, total and all.
	m := newTestModel(t, minWidth, minHeight)
	if vis := stripANSI(m.View()); strings.Contains(vis, "too small") || !strings.Contains(vis, "TOTAL") {
		t.Errorf("at %dx%d the table should be drawn:\n%s", minWidth, minHeight, vis)
	}
}

// Dropping columns must never drop the ones that carry the answer.
func TestFitColumnsKeepsEssentials(t *testing.T) {
	m := newTestModel(t, 80, 24)
	for _, byModel := range []bool{false, true} {
		m.byModel = byModel
		for _, w := range []int{30, 34, 38, 40, 55, 70, 100, 160} {
			cols := fitColumns(m.columns(), w)
			if !has(cols, "name") || !has(cols, "total") || has(cols, "model") != byModel {
				t.Fatalf("by-model %v, width %d dropped an essential column: %+v", byModel, w, cols)
			}
			used := 1
			for _, c := range cols {
				used += c.width + 1
			}
			if used > w && w >= 30 {
				t.Fatalf("by-model %v, width %d: columns need %d", byModel, w, used)
			}
		}
	}
}

// Split by model, an agent's rows differ only in their model, so the MODEL
// column has to survive any width the table is drawn at. It used to go with
// the other medium columns below 82 columns, which at 80, the commonest
// width, printed the two rows below as identical "claude" lines.
func TestByModelKeepsTheModelColumn(t *testing.T) {
	pinLocal(t)
	var recs []model.Usage
	for _, mdl := range []string{"claude-opus-4", "claude-haiku-4"} {
		u, _ := model.Make(model.MakeOpts{Agent: "claude", Time: testNow.Add(-time.Hour), Model: mdl, Input: 100})
		recs = append(recs, u)
	}
	for _, p := range []aggregate.Period{aggregate.PeriodToday, aggregate.PeriodDay} {
		for w := 30; w <= 200; w++ {
			m := New(Options{Period: p, Agents: model.Agents, ByModel: true, Now: testClock})
			m.width, m.height = w, 30
			m.loading = false
			m.setRecords(recs)
			m.recompute()
			m.setAllOpen(true)
			lines := m.tableView(w, newStyles())
			if head := stripANSI(lines[0]); !strings.Contains(head, "MODEL") {
				t.Fatalf("%s at %d columns has no MODEL column: %q", p, w, head)
			}
			seen := map[string]bool{}
			for _, ln := range lines {
				if got := lipgloss.Width(ln); got > w {
					t.Fatalf("%s at %d columns: a line is %d wide: %q", p, w, got, stripANSI(ln))
				}
				row := strings.TrimSpace(stripANSI(ln))
				if !strings.Contains(row, "claude") {
					continue
				}
				if seen[row] {
					t.Fatalf("%s at %d columns: two model rows read the same: %q", p, w, row)
				}
				seen[row] = true
			}
			if len(seen) != 2 {
				t.Fatalf("%s at %d columns: %d model rows, want 2", p, w, len(seen))
			}
		}
	}
}

// A narrow terminal should shed detail columns rather than overflow.
func TestFitColumnsShedsLowPriorityFirst(t *testing.T) {
	m := newTestModel(t, 80, 24)
	wide := fitColumns(m.columns(), 160)
	narrow := fitColumns(m.columns(), 44)
	if len(narrow) >= len(wide) {
		t.Fatalf("narrow kept %d columns, wide kept %d", len(narrow), len(wide))
	}
	if has(narrow, "cachew") || has(narrow, "think") {
		t.Fatalf("low-priority columns survived a 44-wide table: %+v", narrow)
	}
}

func TestHelpAndDetailFitTerminal(t *testing.T) {
	for _, w := range []int{40, 80, 120} {
		m := newTestModel(t, w, 30)
		_ = w
		m.help = true
		for i, ln := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(ln) > w {
				t.Fatalf("help w=%d line %d too wide", w, i)
			}
		}
		// The help box must also fit vertically, or its border is clipped.
		if got := len(strings.Split(m.View(), "\n")); got != 30 {
			t.Fatalf("help w=%d rendered %d lines, want 30", w, got)
		}
		_ = w
		m.help, m.detail = false, true
		for i, ln := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(ln) > w {
				t.Fatalf("detail w=%d line %d too wide", w, i)
			}
		}
	}
}

// The cursor must stay on a real row no matter how the view changes under it.
func TestClampKeepsCursorInRange(t *testing.T) {
	m := newTestModel(t, 100, 24)
	m.setAllOpen(true)
	m.cursor = len(m.flat) - 1
	m.clamp()
	if m.cursor >= len(m.flat) {
		t.Fatalf("cursor %d out of %d", m.cursor, len(m.flat))
	}
	m.setAllOpen(false)
	if m.cursor >= len(m.flat) {
		t.Fatalf("collapsing left cursor at %d of %d", m.cursor, len(m.flat))
	}
	m.agent = "nonexistent"
	m.recompute()
	if m.cursor != 0 || m.offset != 0 {
		t.Fatalf("empty result should reset cursor: %d/%d", m.cursor, m.offset)
	}
	if got := m.View(); got == "" {
		t.Fatal("empty result rendered nothing")
	}
}

// The table total must equal the sum of its top-level rows, expanded or not.
func TestTotalRowMatchesRowsWhenExpanded(t *testing.T) {
	m := newTestModel(t, 120, 30)
	collapsed := aggregate.Sum(m.rows).Total()
	m.setAllOpen(true)
	if got := aggregate.Sum(m.rows).Total(); got != collapsed {
		t.Fatalf("expanding changed the total: %d vs %d", got, collapsed)
	}
	var direct int64
	for _, r := range m.records {
		direct += r.Total()
	}
	if collapsed != direct {
		t.Fatalf("table total %d, records total %d", collapsed, direct)
	}
}

// The help overlay must never be clipped: on a short terminal it drops its
// footnotes rather than losing the key bindings or its border.
func TestHelpBoxNeverClipped(t *testing.T) {
	for _, h := range []int{14, 20, 24, 26, 30, 44} {
		for _, w := range []int{50, 56, 80, 120} {
			m := newTestModel(t, w, h)
			m.help = true
			body := m.View()
			lines := strings.Split(body, "\n")
			if len(lines) != h {
				t.Fatalf("w=%d h=%d: %d lines", w, h, len(lines))
			}
			// A bordered box needs room for its chrome; below that the
			// bindings are shown plainly, which is the intended fallback.
			if h < 22 {
				if !strings.Contains(body, "cycle agent") {
					t.Fatalf("w=%d h=%d: bindings lost in plain fallback", w, h)
				}
				continue
			}
			top := strings.Count(body, "╭")
			bottom := strings.Count(body, "╰")
			if top != 1 || bottom != 1 {
				t.Fatalf("w=%d h=%d: box border clipped (top=%d bottom=%d)", w, h, top, bottom)
			}
			if !strings.Contains(body, "cycle agent") {
				t.Fatalf("w=%d h=%d: key bindings missing", w, h)
			}
		}
	}
}

// Gradients and the mascot must degrade cleanly: no stray escape sequences,
// no width changes, no lost information.
func TestFancyOffProducesPlainOutput(t *testing.T) {
	withFancy(t, false)
	for _, w := range []int{50, 80, 120} {
		m := newTestModel(t, w, 30)
		for _, view := range []aggregate.View{aggregate.ViewTime, aggregate.ViewProject, aggregate.ViewHeat} {
			m.view = view
			m.recompute()
			body := m.View()
			for i, ln := range strings.Split(body, "\n") {
				if lipgloss.Width(ln) > w {
					t.Fatalf("fancy-off w=%d view=%s line %d too wide", w, view, i)
				}
			}
			// The mascot is a fancy-only flourish. Match on the mouth, which
			// appears in every pose and nowhere else in the UI.
			if strings.Contains(body, catMouth) {
				t.Fatalf("fancy-off w=%d view=%s still drew the mascot", w, view)
			}
		}
	}
}

// The mascot must never change the table's geometry: turning it off may only
// reflow the summary cards, never drop or resize a column.
func TestMascotDoesNotAffectTable(t *testing.T) {
	withFancy(t, true)
	m := newTestModel(t, 120, 30)
	withCat := m.tableView(120, newStyles())
	m.cat = false
	withoutCat := m.tableView(120, newStyles())
	if len(withCat) != len(withoutCat) {
		t.Fatalf("table height changed with the mascot: %d vs %d", len(withCat), len(withoutCat))
	}
	for i := range withCat {
		if stripANSI(withCat[i]) != stripANSI(withoutCat[i]) {
			t.Fatalf("table row %d changed with the mascot:\n%q\n%q",
				i, stripANSI(withCat[i]), stripANSI(withoutCat[i]))
		}
	}
}

// Below the width threshold the mascot has to yield the columns to the data.
func TestMascotYieldsOnNarrowTerminals(t *testing.T) {
	withFancy(t, true)
	for _, w := range []int{40, 60, 80} {
		m := newTestModel(t, w, 30)
		if m.showCat() {
			t.Fatalf("mascot claimed columns at width %d", w)
		}
	}
	m := newTestModel(t, 100, 30)
	if !m.showCat() {
		t.Fatal("mascot should appear when there is room")
	}
	// It also has to yield when there is no vertical room for the cards,
	// which go before the table has fewer than its four rows.
	m = newTestModel(t, 100, 16)
	if m.showCat() {
		t.Fatal("mascot appeared without the cards it aligns to")
	}
}

// Gradient helpers must preserve the visible text exactly; only styling is
// added. A drifting cell count would break every column in the table.
func TestGradientsPreserveVisibleText(t *testing.T) {
	withFancy(t, true)
	r := rampVolume()
	for _, s := range []string{"tcount", "1.11B", "  padded  ", "█████", "", "▁▂▃"} {
		if got := stripANSI(gradientText(s, r)); got != s {
			t.Fatalf("gradientText(%q) -> %q", s, got)
		}
		if got := stripANSI(gradientBold(s, r)); got != s {
			t.Fatalf("gradientBold(%q) -> %q", s, got)
		}
		if got := stripANSI(gradientByValue(s, 0.5, r)); got != s {
			t.Fatalf("gradientByValue(%q) -> %q", s, got)
		}
	}
}

// The bar's ramp is sampled across the whole column, so a longer bar must
// always end on a warmer colour than a shorter one. Sampling across just the
// filled part would make every bar end the same colour and strip the hue of
// any meaning.
func TestGradientBarHueTracksLength(t *testing.T) {
	withFancy(t, true)
	r := rampVolume()
	const width = 20
	lastRed := -1
	for _, filled := range []int{2, 5, 9, 14, 20} {
		bar := gradientBar(strings.Repeat("█", filled), width-filled, r, newStyles().Track)
		red := warmestFill(t, bar)
		if red <= lastRed {
			t.Fatalf("bar of %d cells is not warmer than the shorter one (%d vs %d)", filled, red, lastRed)
		}
		lastRed = red
	}
}

// warmestFill returns the red channel of the colour on the last *filled* cell
// of a bar, as a proxy for "how warm the bar gets". It deliberately ignores the
// trailing track, which is drawn in a fixed colour and would otherwise be
// mistaken for the end of the ramp.
func warmestFill(t *testing.T, s string) int {
	t.Helper()
	matches := ansiFilled.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		t.Fatalf("no coloured fill found in %q", s)
	}
	last := matches[len(matches)-1]
	n, err := strconv.Atoi(last[1])
	if err != nil {
		t.Fatalf("bad colour %q", last[1])
	}
	return n
}

// A truecolour foreground immediately followed by a bar glyph.
var ansiFilled = regexp.MustCompile(`\x1b\[38;2;(\d+);\d+;\d+m[\x{2588}\x{258f}\x{258e}\x{258d}\x{258c}\x{258b}\x{258a}\x{2589}]`)

// The tab strip has two independent axes. Arrow keys walk the span; tab walks
// the view. Neither may disturb the other, and every view must be reachable —
// an earlier version rendered all nine tabs as one strip while the arrow keys
// only cycled the six spans, so project/model/hours could not be reached with
// the keys the footer advertised.
func TestSpanAndViewAxesAreIndependent(t *testing.T) {
	m := newTestModel(t, 120, 30)

	// Tab cycles every view and returns to the start, leaving the span alone.
	startSpan := m.period
	seen := map[aggregate.View]bool{m.view: true}
	for i := 0; i < len(views()); i++ {
		m.cycleView(1)
		seen[m.view] = true
		if m.period != startSpan {
			t.Fatalf("cycling the view changed the span to %s", m.period)
		}
	}
	for _, v := range views() {
		if !seen[v] {
			t.Fatalf("view %s is unreachable by cycling", v)
		}
	}
	if m.view != aggregate.ViewTime {
		t.Fatalf("a full cycle should return to time, got %s", m.view)
	}

	// Arrow keys walk every span and leave the view alone.
	m.view = aggregate.ViewProject
	spans := map[aggregate.Period]bool{m.period: true}
	for i := 0; i < len(periods()); i++ {
		m.setPeriod(nextPeriod(m.period))
		spans[m.period] = true
		if m.view != aggregate.ViewProject {
			t.Fatalf("changing the span knocked the view to %s", m.view)
		}
	}
	for _, p := range periods() {
		if !spans[p] {
			t.Fatalf("span %s is unreachable by cycling", p)
		}
	}
}

// Every combination of the two axes has to render, since they combine freely.
func TestEveryAxisCombinationRenders(t *testing.T) {
	for _, p := range periods() {
		for _, v := range views() {
			m := newTestModel(t, 120, 30)
			m.period, m.view = p, v
			m.recompute()
			body := m.View()
			if strings.TrimSpace(stripANSI(body)) == "" {
				t.Fatalf("%s/%s rendered nothing", p, v)
			}
			for i, ln := range strings.Split(body, "\n") {
				if lipgloss.Width(ln) > 120 {
					t.Fatalf("%s/%s line %d too wide", p, v, i)
				}
			}
		}
	}
}

// The heatmap has to honour the span like every other view. It used to ignore
// it, which made the lit span tab a lie, and changing the span silently threw
// you out of the view.
func TestHeatmapHonoursSpan(t *testing.T) {
	m := newTestModel(t, 120, 30)
	m.view = aggregate.ViewHeat

	m.period = aggregate.PeriodAll
	m.recompute()
	all := m.heat.Total

	m.period = aggregate.PeriodToday
	m.recompute()
	today := m.heat.Total

	if all <= today {
		t.Fatalf("all-time heat (%d) should exceed today's (%d)", all, today)
	}
	// And the view must survive the span change.
	if m.view != aggregate.ViewHeat {
		t.Fatalf("changing the span left the heat view: %s", m.view)
	}
}

// The invoked name has to reach the rendered surfaces too, not just the help
// text, or an installed alias looks half-applied.
func TestInvokedNameReachesTheUI(t *testing.T) {
	withFancy(t, true)
	pinLocal(t)
	for _, name := range []string{"tcount", "tokenSummary", "tsum"} {
		m := New(Options{Agents: model.Agents, Name: name, Version: "0.3.0", Now: testClock})
		m.width, m.height = 110, 30
		m.loading = false
		m.setRecords(sample(testNow))
		m.recompute()

		if got := stripANSI(m.titleBar(m.width, newStyles())); !strings.Contains(got, name) {
			t.Errorf("%s: title bar shows %q", name, strings.TrimSpace(got))
		}
		m.help = true
		if got := stripANSI(m.View()); !strings.Contains(got, name) {
			t.Errorf("%s: help overlay does not name it", name)
		}
	}
	// No name threaded through still renders something sensible.
	m := New(Options{Agents: model.Agents})
	if m.opts.Name == "" {
		t.Fatal("an empty name should fall back to a default")
	}
}
