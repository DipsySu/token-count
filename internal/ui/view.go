package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

const detailHeight = 6

func (m Model) View() string {
	w, h := m.width, m.height
	if w < 30 {
		w = 80
	}
	if h < 10 {
		h = 24
	}
	st := newStyles()
	lines := strings.Split(m.helpOrBody(w, h, st), "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:h], "\n")
}

func (m Model) helpOrBody(w, h int, st Styles) string {
	if m.help {
		return m.helpView(w, h, st)
	}
	return strings.Join(m.compose(w, h, st), "\n")
}

// compose lays the screen out top to bottom. Every section returns a fixed
// number of lines so the total always matches the terminal height.
func (m Model) compose(w, h int, st Styles) []string {
	var out []string
	out = append(out, m.titleBar(w, st))
	out = append(out, m.tabBar(w, st))

	if m.showCards() {
		out = append(out, "")
		out = append(out, m.cards(w, st)...)
	}
	out = append(out, "")

	if n := m.notice(); n != "" {
		out = append(out, st.Notice.Render(truncate(" "+n, w)))
	}

	switch {
	case m.loading:
		out = append(out, m.loadingBody(w, h-len(out)-1, st)...)
	case m.err != nil:
		out = append(out, st.Err.Render(truncate("  "+m.err.Error(), w)))
	case m.view == aggregate.ViewHeat:
		out = append(out, m.heatView(w, st)...)
	default:
		out = append(out, m.tableView(w, st)...)
	}

	if m.detail && !m.loading {
		out = append(out, m.detailView(w, st)...)
	}

	for len(out) < h-1 {
		out = append(out, "")
	}
	out = append(out[:h-1], m.footer(w, st))
	return out
}

func (m Model) titleBar(w int, st Styles) string {
	left := " " + gradientBold(m.opts.Name, rampTitle())
	if !fancyEnabled() {
		left = st.Title.Render(" " + m.opts.Name)
	}
	if m.opts.Version != "" {
		left += st.Version.Render(" v" + m.opts.Version)
	}
	right := st.Muted.Render(time.Now().Format("2006-01-02 15:04") + " ")
	return joinLR(left, right, w)
}

// tabBar draws the two independent axes: which span of time is shown, and how
// it is grouped. They are separate groups because they combine freely -- a
// project ranking for last week is a valid state -- so exactly one tab in each
// group is always lit, and each group has its own key.
func (m Model) tabBar(w int, st Styles) string {
	var b strings.Builder
	b.WriteByte(' ')
	for _, p := range periods() {
		if p == m.period {
			b.WriteString(st.TabOn.Render(string(p)))
		} else {
			b.WriteString(st.Tab.Render(string(p)))
		}
	}
	// A wide, dim divider: the two groups must not read as one strip that a
	// single pair of arrow keys could walk end to end.
	b.WriteString(st.Faint.Render("   \u2502  "))
	for _, v := range views() {
		label := viewLabel(v)
		if m.view == v {
			b.WriteString(st.TabOn.Render(label))
		} else {
			b.WriteString(st.Tab.Render(label))
		}
	}
	return joinLR(b.String(), m.statusChips(st), w)
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

// statusChips shows the filters that are currently narrowing the view, so an
// unexpected number always has a visible cause.
func (m Model) statusChips(st Styles) string {
	var chips []string
	if m.agent != "" {
		chips = append(chips, agentStyle(m.agent).Render(m.agent))
	}
	if m.byModel {
		chips = append(chips, st.Muted.Render("by-model"))
	}
	if m.sort != sortDefault {
		chips = append(chips, st.Muted.Render("↓"+m.sort.String()))
	}
	if m.seeking {
		chips = append(chips, st.Key.Render("/"+m.search+"▏"))
	} else if m.search != "" {
		chips = append(chips, st.Key.Render("/"+m.search))
	}
	if len(chips) == 0 {
		chips = append(chips, st.Faint.Render("all agents"))
	}
	if m.showCat() {
		// The mascot's expression is graded, not decorative; naming the grade
		// keeps it readable instead of a private joke.
		chips = append(chips, gradientByValue(
			catMoodChip(m.mood(), m.snapshot["today"].Totals.Total()), 0.35, rampCat()))
	}
	return strings.Join(chips, st.Faint.Render(" · ")) + " "
}

// cards renders the four headline figures with a trend line and a comparison
// against the equivalent previous period. When there is width to spare the
// mascot sits to their left, occupying the same four lines.
func (m Model) cards(w int, st Styles) []string {
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
	var cat []string
	if m.showCat() {
		cat = catStrip(catArt(m.mood()))
		catCols = catWidth + 1
	}

	cw := (w - 1 - catCols) / len(items)
	if cw < 12 {
		cw = 12
	}

	rows := make([][]string, 4)
	peak := m.peakDay()
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
			if peak > 0 {
				t = clamp01(float64(total) / float64(peak) / cardHeat(it.key))
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
		if cat != nil && i < len(cat) {
			line += cat[i] + " "
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

// mood grades today against the busiest day on record.
func (m Model) mood() catMood {
	return moodFor(m.snapshot["today"].Totals.Total(), m.peakDay())
}

// peakDay is the largest single-day total seen, used as the yardstick for both
// the mascot's mood and the card hues.
func (m Model) peakDay() int64 {
	var peak int64
	for _, v := range m.snapshot["year"].Trend {
		if v > peak {
			peak = v
		}
	}
	for _, v := range m.snapshot["today"].Trend {
		if v > peak {
			peak = v
		}
	}
	return peak
}

// showCat reports whether there is room to spend columns on the mascot.
func (m Model) showCat() bool {
	return m.cat && fancyEnabled() && m.width >= 84 && m.showCards()
}

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

func (m Model) loadingBody(w, h int, st Styles) []string {
	frame := spinner[m.spin%len(spinner)]
	var out []string
	if m.cat && fancyEnabled() && w >= 52 {
		// The cat blinks and swishes while the scan runs, so the wait has
		// something to watch other than a spinner.
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
			st.Dim.Render("  " + frame + "  scanning session logs"),
			"",
			st.Faint.Render("     ~/.claude  ~/.codex  ~/.grok  ~/.gemini  ~/.local/share/opencode"),
		}
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out
}

func (m Model) footer(w int, st Styles) string {
	if m.seeking {
		return st.Footer.Render(truncate(" type to filter   enter accept   esc clear", w))
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
			b.WriteString(st.Key.Render("\u2191" + model.Comma(int64(above))))
			b.WriteString(" ")
		}
		if below > 0 {
			b.WriteString(st.Key.Render("\u2193" + model.Comma(int64(below))))
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
		{"SPAN  \u2190 \u2192", "VIEW  tab"},
		{"1 today    4 month", "t  time"},
		{"2 day      5 year", "p  by project / cwd"},
		{"3 week     0 all", "M  by model"},
		{"", "H  hour-of-week heat"},
		{"", ""},
		{"ROWS", "FILTER"},
		{"j k \u2191 \u2193  move", "a  cycle agent"},
		{"enter    expand row", "A  all agents"},
		{"E / C    expand all", "m  split by model"},
		{"g / G    top / end", "s  cycle sort"},
		{"d        detail", "/  search"},
		{"c        mascot", "esc  clear"},
		{"r        rescan", ""},
	}
}

var helpNotes = []string{
	"",
	"totals = input + cache read + cache write + output",
	"calls  = upstream API requests, not turns",
	"cache is processed volume, not billed cost",
	"",
	"Messages replayed into resumed sessions count once.",
}

// helpLines builds the overlay content. Each line is styled on its own: a
// newline inside a styled block makes lipgloss re-wrap the whole thing.
func helpLines(name string, st Styles, withTitle, withNotes bool) []string {
	const leftW = 22
	var out []string
	if withTitle {
		out = append(out,
			st.Title.Render(name)+st.Muted.Render(" \u2014 local AI agent token usage"),
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
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// --- layout helpers -------------------------------------------------------

func joinLR(left, right string, width int) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if space := width - lw - rw; space >= 1 {
		return left + strings.Repeat(" ", space) + right
	}
	if lw >= width {
		return truncate(left, width)
	}
	return left // right side does not fit; drop it rather than overflow
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return runewidth.Truncate(s, width, "…")
}

// truncateLeft drops leading characters instead of trailing ones. Paths are
// distinguished by their tail, so that is the half worth keeping.
func truncateLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	r := []rune(s)
	for i := range r {
		cand := string(r[i:])
		if runewidth.StringWidth(cand)+1 <= width {
			return "…" + cand
		}
	}
	return runewidth.Truncate(s, width, "…")
}

// padRight pads a possibly styled string to width without disturbing its
// escape sequences.
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return truncate(s, width)
	}
	return s + strings.Repeat(" ", width-w)
}

func padLeft(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return truncate(s, width)
	}
	return strings.Repeat(" ", width-w) + s
}

func padCol(s string, width int, right bool) string {
	if right {
		return padLeft(s, width)
	}
	return padRight(s, width)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
