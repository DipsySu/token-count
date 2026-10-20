package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

const detailHeight = 6

// minWidth and minHeight are the smallest terminal the layout is drawn for.
const (
	minWidth  = 30
	minHeight = 10
)

func (m Model) View() string {
	w, h := m.width, m.height
	st := newStyles()
	if w < minWidth || h < minHeight {
		return tooSmall(w, h, st)
	}
	lines := strings.Split(m.helpOrBody(w, h, st), "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:h], "\n")
}

// tooSmall stands in for the layout on a terminal smaller than it is drawn
// for. Drawing the frame at the minimum size anyway would not help: the
// terminal cuts whatever does not fit, keeping the left columns and the
// bottom rows, so the totals and the title would be the first things lost.
func tooSmall(w, h int, st Styles) string {
	note := []string{
		st.Warn.Render(truncate("terminal too small", w)),
		st.Faint.Render(truncate(fmt.Sprintf("need %dx%d", minWidth, minHeight), w)),
	}
	if h < len(note) {
		note = note[:max(h, 0)]
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, strings.Join(note, "\n"))
}

func (m Model) helpOrBody(w, h int, st Styles) string {
	if m.help {
		return m.helpView(w, h, st)
	}
	return strings.Join(m.compose(w, h, st), "\n")
}

// compose lays the screen out top to bottom, drawing what layout decided
// fits, so the frame is always exactly the terminal's height.
func (m Model) compose(w, h int, st Styles) []string {
	l := m.layout()
	out := make([]string, 0, h)
	out = append(out, m.titleBar(w, st), m.tabBar(w, st), m.scopeLine(w, st))
	if l.cards {
		out = append(out, m.caption(w, st))
		out = append(out, m.cards(w, st, l.cat)...)
	}
	if l.spacer {
		out = append(out, "")
	}
	if l.notice != "" {
		style := st.Notice
		if l.warn {
			style = st.Warn
		}
		out = append(out, style.Render(truncate(" "+l.notice, w)))
	}

	switch {
	case m.loading:
		out = append(out, m.loadingBody(w, l.body, st)...)
	case m.err != nil:
		out = append(out, st.Err.Render(truncate("  "+m.err.Error(), w)))
	case m.view == aggregate.ViewHeat:
		out = append(out, heatFit(m.heatView(w, st), l.body)...)
	default:
		out = append(out, m.tableView(w, st)...)
	}

	if l.detail {
		out = append(out, m.detailView(w, st)...)
	}

	for len(out) < h-1 {
		out = append(out, "")
	}
	return append(out[:h-1], m.footer(w, st))
}

func (m Model) titleBar(w int, st Styles) string {
	left := " " + gradientBold(m.opts.Name, rampTitle())
	if !fancyEnabled() {
		left = st.Title.Render(" " + m.opts.Name)
	}
	if m.opts.Version != "" {
		left += st.Version.Render(" v" + m.opts.Version)
	}
	// The stamp is the instant the figures were computed against, not a live
	// clock: after midnight a live clock would show a date the cards below no
	// longer describe. Before the first computation there are no figures to
	// date, so the scan screen shows the current time.
	at := m.asOf
	if at.IsZero() {
		at = m.opts.now()
	}
	right := st.Muted.Render(at.In(time.Local).Format("2006-01-02 15:04") + " ")
	return joinLR(left, right, w)
}

// tabBar draws the two independent axes: which span of time is shown, and how
// it is grouped. They are separate groups because they combine freely -- a
// project ranking for last week is a valid state -- so exactly one tab in each
// group is always lit, and each group has its own key. The filters live on
// the scope line below; the mascot's mood sits on the right when it fits.
//
// A strip that does not fit is not cut, which would leave half a label:
// it gives up the padding of its unlit tabs first, and then shows only the
// lit tab of each group, with the key that moves it.
func (m Model) tabBar(w int, st Styles) string {
	chip := ""
	if m.layout().cat {
		// The mascot's expression is graded, not decorative; naming the grade
		// keeps it readable instead of a private joke.
		chip = gradientByValue(catMoodChip(m.mood, m.snapshot["today"].Totals.Total()), 0.35, rampCat()) + " "
	}
	strips := []string{m.tabStrip(st, false), m.tabStrip(st, true), m.litTabs(st, true), m.litTabs(st, false)}
	for _, s := range strips {
		if ansi.StringWidth(s) <= w {
			return joinLR(s, chip, w)
		}
	}
	return truncate(strips[len(strips)-1], w)
}

// tabStrip is every tab of both groups. A compact strip drops the padding of
// the unlit tabs and narrows the divider; one space keeps two unlit tabs
// apart, while the lit tab's own padding sets it off from its neighbours.
func (m Model) tabStrip(st Styles, compact bool) string {
	type tab struct {
		label string
		lit   bool
		rule  bool // the divider between the groups
	}
	var tabs []tab
	for _, p := range periods() {
		tabs = append(tabs, tab{label: string(p), lit: p == m.period})
	}
	tabs = append(tabs, tab{rule: true})
	for _, v := range views() {
		tabs = append(tabs, tab{label: viewLabel(v), lit: v == m.view})
	}

	var b strings.Builder
	b.WriteByte(' ')
	unlit := st.Tab
	if compact {
		unlit = st.Tab.Padding(0)
	}
	for i, t := range tabs {
		if compact && i > 0 && !t.lit && !tabs[i-1].lit {
			b.WriteByte(' ')
		}
		switch {
		case t.rule && compact:
			b.WriteString(st.Faint.Render("│"))
		case t.rule:
			// A wide, dim divider: the two groups must not read as one
			// strip that a single pair of arrow keys could walk end to end.
			b.WriteString(st.Faint.Render("   │  "))
		case t.lit:
			b.WriteString(st.TabOn.Render(t.label))
		default:
			b.WriteString(unlit.Render(t.label))
		}
	}
	return b.String()
}

// litTabs is the lit tab of each group alone, with the keys that move it
// when there is room for them.
func (m Model) litTabs(st Styles, keys bool) string {
	lit := func(label, key string) string {
		s := st.Faint.Render("‹") + st.TabOn.Render(label) + st.Faint.Render("›")
		if keys {
			s += " " + st.Key.Render(key)
		}
		return s
	}
	return " " + lit(string(m.period), "←→") + "   " + lit(viewLabel(m.view), "tab")
}

func viewLabel(v aggregate.View) string {
	switch v {
	case aggregate.ViewProject:
		return "project"
	case aggregate.ViewModel:
		return "model"
	case aggregate.ViewHeat:
		return "hours"
	default:
		return "time"
	}
}

// cards renders the four headline figures with a trend line and a comparison
// against the same elapsed part of the previous period. With cat set the
// mascot sits to their left, occupying the same four lines.
func (m Model) cards(w int, st Styles, cat bool) []string {
	items := []struct {
		key, title string
		period     aggregate.Period
	}{
		{"today", "TODAY", aggregate.PeriodToday},
		{"week", "THIS WEEK", aggregate.PeriodWeek},
		{"month", "THIS MONTH", aggregate.PeriodMonth},
		{"year", "THIS YEAR", aggregate.PeriodYear},
	}

	catCols := 0
	var art []string
	if cat {
		art = catStrip(catArt(m.mood))
		catCols = catWidth + 1
	}

	cw := (w - 1 - catCols) / len(items)
	if cw < 12 {
		cw = 12
	}

	rows := make([][]string, 4)
	for _, it := range items {
		snap := m.snapshot[it.key]
		inner := cw - 2

		label := st.Faint.Render(it.title)
		if m.period == it.period {
			label = st.Key.Render(it.title)
		}

		total := snap.Totals.Total()
		value := st.CardNum.Render(model.FmtTokens(total))
		if fancyEnabled() {
			// Hue tracks how the figure compares with the busiest day, so a
			// heavy period reads warm at a glance.
			t := 0.0
			if m.peak > 0 {
				t = clamp01(float64(total) / float64(m.peak) / cardHeat(it.key))
			}
			value = gradientBold(model.FmtTokens(total), shiftRamp(rampVolume(), t))
		}

		spark := ""
		if len(snap.Trend) > 1 {
			spark = gradientText(model.Sparkline(fitSeries(snap.Trend, inner)), rampSpark())
			if !fancyEnabled() {
				spark = st.Spark.Render(model.Sparkline(fitSeries(snap.Trend, inner)))
			}
		}

		delta := ""
		if it.key != "year" {
			d := model.Change(total, snap.Prev.Total())
			sty := st.Down
			if d.Up {
				sty = st.Up
			}
			if !d.Known {
				sty = st.Faint
			}
			delta = sty.Render(d.String()) + st.Faint.Render(" "+prevLabel(it.key))
		} else {
			delta = st.Faint.Render(model.Comma(snap.Totals.Calls) + " calls")
		}

		rows[0] = append(rows[0], padVisible(label, cw, false))
		rows[1] = append(rows[1], padVisible(value, cw, false))
		rows[2] = append(rows[2], padVisible(spark, cw, false))
		rows[3] = append(rows[3], padVisible(delta, cw, false))
	}

	out := make([]string, 0, 4)
	for i, r := range rows {
		line := " "
		if art != nil && i < len(art) {
			line += art[i] + " "
		}
		line += strings.Join(r, "")
		out = append(out, truncate(line, w))
	}
	return out
}

// cardHeat scales each card's hue range: a month naturally holds far more than
// a day, so they are normalised against different expectations.
func cardHeat(key string) float64 {
	switch key {
	case "week":
		return 5
	case "month":
		return 18
	case "year":
		return 60
	default:
		return 1
	}
}

// shiftRamp narrows a ramp to the stretch around t, so a single figure gets a
// colour from the right part of the scale rather than the whole sweep.
func shiftRamp(r ramp, t float64) ramp {
	if len(r) == 0 {
		return r
	}
	lo := r.at(clamp01(t - 0.12))
	hi := r.at(clamp01(t + 0.12))
	return ramp{lo, hi}
}

// showCat reports whether the mascot is drawn in the current frame.
func (m Model) showCat() bool { return m.layout().cat }

// prevLabel names what a card's delta compares with. Each is like for like:
// yesterday up to the same time of day, last week up to the same weekday and
// time, last month up to the same day and time.
func prevLabel(key string) string {
	switch key {
	case "today":
		return "vs yest"
	case "week":
		return "vs last wk"
	case "month":
		return "vs last mo"
	default:
		return ""
	}
}

// fitSeries samples a series down to at most width points, keeping the most
// recent data when it has to drop some.
func fitSeries(vals []int64, width int) []int64 {
	if width <= 0 || len(vals) <= width {
		return vals
	}
	return vals[len(vals)-width:]
}

// loadingBody is the scan screen in h lines.
func (m Model) loadingBody(w, h int, st Styles) []string {
	frame := spinner[m.spin%len(spinner)]
	var out []string
	if m.cat && fancyEnabled() && w >= 52 && h >= 4 {
		// The cat blinks and swishes while the scan runs, so the wait has
		// something to watch other than a spinner. It is four lines tall.
		art := catStrip(catBlink(m.spin / 3))
		notes := []string{
			st.Dim.Render(frame + " scanning session logs"),
			st.Faint.Render("~/.claude  ~/.codex  ~/.grok"),
			st.Faint.Render("~/.gemini  ~/.local/share/opencode"),
			"",
		}
		for i, ln := range art {
			line := "  " + ln + "   "
			if i < len(notes) {
				line += notes[i]
			}
			out = append(out, truncate(line, w))
		}
	} else {
		out = []string{
			st.Dim.Render(truncate("  "+frame+"  scanning session logs", w)),
			"",
			st.Faint.Render(truncate("     ~/.claude  ~/.codex  ~/.grok  ~/.gemini  ~/.local/share/opencode", w)),
		}
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out[:max(h, 0)]
}

func (m Model) footer(w int, st Styles) string {
	if m.input.Focused() {
		// ctrl+c is the way out of the box, so the explanation gives way to
		// it, and the record count, which the box's own line repeats, gives
		// way to both.
		quit := st.Key.Render("ctrl+c") + st.Footer.Render(" quit")
		full := st.Footer.Render(" type an agent, model or project") + st.Faint.Render("   ") + quit
		right := m.footerRight(st)
		for _, left := range []string{full, " " + quit} {
			if ansi.StringWidth(left)+ansi.StringWidth(right) < w {
				return joinLR(left, right, w)
			}
			if ansi.StringWidth(left) <= w {
				return left
			}
		}
		return truncate(" "+quit, w)
	}
	if m.loading {
		return st.Footer.Render(truncate(" scanning…", w))
	}
	keys := []struct{ k, v string }{
		{"←→", "span"},
		{"tab", "view"},
		{"enter", "expand"},
		{"a", "agent"},
		{"s", "sort"},
		{"/", "find"},
		{"?", "help"},
		{"q", "quit"},
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, st.Key.Render(k.k)+st.Footer.Render(" "+k.v))
	}
	line := " " + strings.Join(parts, st.Faint.Render("  "))
	return joinLR(line, m.footerRight(st), w)
}

// footerRight reports the record count and, when the table is scrolled, how
// many rows lie outside the visible window.
func (m Model) footerRight(st Styles) string {
	var b strings.Builder
	if n := len(m.flat); n > 0 {
		vis := m.bodyRows()
		above := m.offset
		below := n - min(m.offset+vis, n)
		if above > 0 {
			b.WriteString(st.Key.Render("↑" + model.Comma(int64(above))))
			b.WriteString(" ")
		}
		if below > 0 {
			b.WriteString(st.Key.Render("↓" + model.Comma(int64(below))))
			b.WriteString(" ")
		}
	}
	b.WriteString(st.Faint.Render(model.Comma(int64(len(m.scoped))) + " rec "))
	return b.String()
}

// helpRows is the key-binding grid: two fixed columns so the right column
// starts at the same offset on every line.
func helpRows() [][2]string {
	return [][2]string{
		{"SPAN  ← →", "VIEW  tab"},
		{"1 today    4 month", "t  time"},
		{"2 day      5 year", "p  by project / cwd"},
		{"3 week     0 all", "M  by model"},
		{"", "H  hour-of-week heat"},
		{"", ""},
		{"ROWS", "FILTER"},
		{"j k ↑ ↓  move", "a  cycle agent"},
		{"enter    expand row", "A  all agents"},
		{"E / C    expand all", "m  split by model"},
		{"g / G    top / end", "s  cycle sort"},
		{"d        detail", "/  search, live"},
		{"c        mascot", "esc  clear search"},
		{"r        rescan", ""},
		{"q        quit", "ctrl+c  quit, anywhere"},
	}
}

// helpNotes explain what the screen shows. They are the first thing the
// overlay drops on a short terminal, so the key bindings never are.
var helpNotes = []string{
	"",
	"search: agent, model or project, any case, ~/ paths;",
	"  CJK and pastes work; enter keeps it, esc clears it",
	"the line under the tabs: dates, UTC offset, filters",
	"OVERVIEW cards ignore the span and the search; their",
	"  deltas are like for like: today vs yesterday so far",
	"a ⚠ line means some logs could not be read;",
	"  totals are then a lower bound",
	"",
	"totals = input + cache read + cache write + output",
	"calls  = upstream API requests, not turns",
	"cache is processed volume, not billed cost",
	"Repeated records (resumed, copied logs) count once.",
}

// helpLines builds the overlay content. Each line is styled on its own: a
// newline inside a styled block makes lipgloss re-wrap the whole thing.
func helpLines(name string, st Styles, withTitle, withNotes bool) []string {
	const leftW = 22
	var out []string
	if withTitle {
		out = append(out,
			st.Title.Render(name)+st.Muted.Render(" — local AI agent token usage"),
			"")
	}
	for _, r := range helpRows() {
		if r[0] == "" && r[1] == "" {
			out = append(out, "")
			continue
		}
		line := padRight(r[0], leftW) + r[1]
		if r[1] == "VIEW" || r[1] == "FILTER" {
			line = st.Header.Render(line)
		}
		out = append(out, line)
	}
	if withNotes {
		for _, n := range helpNotes {
			if n == "" {
				out = append(out, "")
				continue
			}
			out = append(out, st.Faint.Render(n))
		}
	}
	return out
}

// helpView sizes the overlay to the terminal instead of overflowing it. Notes
// go first, then the title, then the box chrome; the key bindings themselves
// are never dropped. Each candidate is measured after rendering, because a
// long note can wrap and add lines the raw count would not predict.
func (m Model) helpView(w, h int, st Styles) string {
	boxW := min(60, max(24, w-6))
	for _, attempt := range []struct{ title, notes bool }{
		{true, true},
		{true, false},
		{false, false},
	} {
		lines := helpLines(m.opts.Name, st, attempt.title, attempt.notes)
		box := st.Help.Width(boxW).Render(strings.Join(lines, "\n"))
		if lipgloss.Height(box) <= h {
			return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
		}
	}
	// Too short for any box: show the bindings plainly rather than clipping a
	// border off the screen.
	lines := helpLines(m.opts.Name, st, false, false)
	if len(lines) > h {
		lines = lines[:h]
	}
	// The two columns need 42 cells; a narrower terminal clips them rather
	// than letting a line wrap and push the screen out of shape.
	for i := range lines {
		lines[i] = truncate(lines[i], w)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// --- layout helpers -------------------------------------------------------
//
// Everything below takes strings that may already be styled. Widths are
// measured, and strings cut, with the same ANSI-aware rules from x/ansi:
// counting escape bytes as cells cuts a styled string far too early, and a cut
// that lands inside a styled run drops its reset, so the colour bleeds into
// whatever is printed next.

func joinLR(left, right string, width int) string {
	lw := ansi.StringWidth(left)
	rw := ansi.StringWidth(right)
	if space := width - lw - rw; space >= 1 {
		return left + strings.Repeat(" ", space) + right
	}
	if lw >= width {
		return truncate(left, width)
	}
	return left // right side does not fit; drop it rather than overflow
}

// truncate cuts s to at most width cells, marking the cut with an ellipsis.
// Escape sequences past the cut are kept, so every style is still closed.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// truncateLeft drops leading characters instead of trailing ones. Paths are
// distinguished by their tail, so that is the half worth keeping.
func truncateLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	sw := ansi.StringWidth(s)
	if sw <= width {
		return s
	}
	// Drop just enough leading cells to leave room for the ellipsis. A wide
	// character straddling the cut is kept whole, which can leave the result
	// a cell too wide, so drop one more cell until it fits. Only when no part
	// of the tail fits beside the ellipsis, as at width 1, is the string cut
	// from the right instead.
	for drop := sw - width + 1; drop < sw; drop++ {
		if out := ansi.TruncateLeft(s, drop, "…"); ansi.StringWidth(out) <= width {
			return out
		}
	}
	return ansi.Truncate(s, width, "…")
}

// padRight fits a possibly styled string to exactly width cells without
// disturbing its escape sequences.
func padRight(s string, width int) string {
	s, w := fit(s, width)
	if w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}

func padLeft(s string, width int) string {
	s, w := fit(s, width)
	if w < width {
		s = strings.Repeat(" ", width-w) + s
	}
	return s
}

// fit truncates s to width when it is wider and reports the width it ends up
// with. That can be a cell short of width when a double-width character
// straddles the cut, which is why the padding helpers measure after cutting.
func fit(s string, width int) (string, int) {
	w := ansi.StringWidth(s)
	if w > width {
		s = truncate(s, width)
		w = ansi.StringWidth(s)
	}
	return s, w
}

// sgrReset is how termenv, under lipgloss, closes every style it renders.
const sgrReset = "\x1b[0m"

// underlay styles a line whose cells already carry styles of their own.
// Wrapping the line once is not enough: the reset that closes each styled
// cell closes the outer style too, so a highlight would stop at the first
// coloured cell. The outer style is reopened after every reset instead, and
// each cell's own colour still shows on top of it.
func underlay(line string, style lipgloss.Style) string {
	if open, _, ok := strings.Cut(style.Render("x"), "x"); ok && open != "" {
		line = strings.ReplaceAll(line, sgrReset, sgrReset+open)
	}
	return style.Render(line)
}

func padCol(s string, width int, right bool) string {
	if right {
		return padLeft(s, width)
	}
	return padRight(s, width)
}
