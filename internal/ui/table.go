package ui

import (
	"strings"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

// col is one table column. prio decides which columns survive on a narrow
// terminal: lower numbers are dropped first.
type col struct {
	title string
	width int
	right bool
	prio  int
	id    string
}

const (
	prioEssential = 0
	prioHigh      = 1
	prioMedium    = 2
	prioLow       = 3
)

// columns describes the full table, before any are dropped to fit the width.
func (m Model) columns() []col {
	nameW, nameTitle := 14, "AGENT"
	switch {
	case m.view == aggregate.ViewProject:
		nameW, nameTitle = 30, "PROJECT"
	case m.view == aggregate.ViewModel:
		nameW, nameTitle = 26, "MODEL"
	case m.period == aggregate.PeriodToday:
		nameW, nameTitle = 14, "AGENT"
	case m.period == aggregate.PeriodWeek:
		nameW, nameTitle = 20, "WEEK"
	case m.period == aggregate.PeriodMonth:
		nameW, nameTitle = 14, "MONTH"
	case m.period == aggregate.PeriodYear:
		nameW, nameTitle = 10, "YEAR"
	default:
		nameW, nameTitle = 14, "DAY"
	}

	cols := []col{
		{nameTitle, nameW, false, prioEssential, "name"},
		{"TOTAL", 8, true, prioEssential, "total"},
		{"", 14, false, prioHigh, "bar"},
		{"IN", 7, true, prioMedium, "in"},
		{"CACHE R", 8, true, prioLow, "cacher"},
		{"CACHE W", 8, true, prioLow, "cachew"},
		{"OUT", 7, true, prioMedium, "out"},
		{"THINK", 7, true, prioLow, "think"},
		{"CALLS", 6, true, prioHigh, "calls"},
		{"AVG", 7, true, prioLow, "avg"},
	}
	if m.byModel && m.view == aggregate.ViewTime {
		cols = append(cols[:1],
			append([]col{{"MODEL", 18, false, prioMedium, "model"}}, cols[1:]...)...)
	}
	return cols
}

// fitColumns drops the lowest-priority columns until the table fits, then
// spends any leftover space on the name column and the bar.
func fitColumns(cols []col, width int) []col {
	const gap = 1
	total := func(cs []col) int {
		n := 0
		for _, c := range cs {
			n += c.width + gap
		}
		return n + 1 // leading indent
	}
	out := append([]col{}, cols...)
	for prio := prioLow; prio > prioEssential && total(out) > width; prio-- {
		kept := out[:0:0]
		for _, c := range out {
			if c.prio == prio {
				continue
			}
			kept = append(kept, c)
		}
		out = kept
	}
	// Still too wide: shrink the name column before giving up on a column.
	if t := total(out); t > width && len(out) > 0 && out[0].id == "name" {
		if shrink := t - width; out[0].width-shrink >= 8 {
			out[0].width -= shrink
		} else {
			out[0].width = 8
		}
	}
	if slack := width - total(out); slack > 0 {
		for i := range out {
			if out[i].id == "bar" {
				grow := min(slack, 12)
				out[i].width += grow
				slack -= grow
				break
			}
		}
		if slack > 0 && len(out) > 0 && out[0].id == "name" {
			out[0].width += min(slack, 24)
		}
	}
	return out
}

func has(cols []col, id string) bool {
	for _, c := range cols {
		if c.id == id {
			return true
		}
	}
	return false
}

func colWidth(cols []col, id string) int {
	for _, c := range cols {
		if c.id == id {
			return c.width
		}
	}
	return 0
}

func (m Model) tableView(width int, st Styles) []string {
	cols := fitColumns(m.columns(), width)

	head := make([]string, 0, len(cols))
	for _, c := range cols {
		title := c.title
		if c.id == "name" {
			title = "  " + title // sit above the labels, clear of the tree markers
		}
		head = append(head, padCol(title, c.width, c.right))
	}
	out := []string{
		st.Header.Render(" " + strings.Join(head, " ")),
		st.Rule.Render(strings.Repeat("─", max(0, width))),
	}

	if len(m.flat) == 0 {
		return append(out, m.emptyState(width, st)...)
	}

	// Every bar shares one scale, set by the largest top-level row. A child
	// therefore reads as a real fraction of the whole table rather than being
	// rescaled against its siblings.
	var scale int64
	for _, b := range m.rows {
		if b.Totals.Total() > scale {
			scale = b.Totals.Total()
		}
	}

	vis := m.bodyRows()
	start := m.offset
	end := min(start+vis, len(m.flat))

	for i := start; i < end; i++ {
		out = append(out, m.renderRow(m.flat[i], cols, scale, st, i == m.cursor))
	}

	// The total sits directly under the last row so the two read together;
	// any leftover height is absorbed below it. How many rows are off-screen
	// is reported in the footer, which always has room for it.
	out = append(out, m.totalRow(cols, st, width))
	return out
}

// emptyState explains why the table is blank, and says what would un-blank it
// when a filter is the cause rather than a genuine absence of data.
func (m Model) emptyState(width int, st Styles) []string {
	reason := "nothing recorded in this range"
	hint := ""
	switch {
	case m.search != "":
		reason = "nothing matches \"" + m.search + "\""
		hint = "esc clears the search"
	case m.agent != "":
		reason = "no " + m.agent + " usage in this range"
		hint = "A shows every agent"
	case m.opts.Since != nil || m.opts.Until != nil:
		hint = "the --since/--until window may be too narrow"
	default:
		hint = "\u2190 \u2192 widens the period"
	}

	if !m.cat || !fancyEnabled() || width < 44 {
		out := []string{"", st.Faint.Render("   " + reason)}
		if hint != "" {
			out = append(out, st.Faint.Render("   "+hint))
		}
		return out
	}
	// A resting cat is friendlier than a bare line, and the wait is over
	// anyway: there is nothing else to put here.
	art := catStrip(catCurled())
	notes := []string{"", reason, hint, ""}
	out := []string{""}
	for i, ln := range art {
		line := "  " + ln + "    "
		if i < len(notes) && notes[i] != "" {
			line += st.Faint.Render(notes[i])
		}
		out = append(out, truncate(line, width))
	}
	return out
}

// renderRow draws one row. Selection is applied per cell so that the styled
// agent colours and bar survive the highlight instead of being overwritten.
func (m Model) renderRow(b model.Bucket, cols []col, scale int64, st Styles, selected bool) string {
	t := b.Totals
	cells := make([]string, 0, len(cols))

	for _, c := range cols {
		var cell string
		switch c.id {
		case "name":
			cell = m.nameCell(b, c.width, st)
		case "model":
			cell = padCol(model.ShortModel(b.Model), c.width, false)
			if b.Model == "" {
				cell = padCol("·", c.width, false)
			}
			cell = st.Child.Render(cell)
		case "total":
			cell = padCol(model.FmtTokens(t.Total()), c.width, true)
			if b.Depth == 0 {
				cell = st.Text.Render(cell)
			} else {
				cell = st.Child.Render(cell)
			}
		case "bar":
			cell = renderBar(t.Total(), scale, c.width, st)
		case "in":
			cell = st.Child.Render(padCol(dashZero(t.Input), c.width, true))
		case "cacher":
			cell = st.Child.Render(padCol(dashZero(t.CacheRead), c.width, true))
		case "cachew":
			cell = st.Child.Render(padCol(dashZero(t.CacheWrite), c.width, true))
		case "out":
			cell = st.Child.Render(padCol(dashZero(t.Output), c.width, true))
		case "think":
			cell = st.Child.Render(padCol(dashZero(t.Reasoning), c.width, true))
		case "calls":
			cell = st.Child.Render(padCol(model.Comma(t.Calls), c.width, true))
		case "avg":
			cell = st.Child.Render(padCol(model.FmtTokens(t.PerCall()), c.width, true))
		default:
			cell = strings.Repeat(" ", c.width)
		}
		cells = append(cells, cell)
	}

	line := " " + strings.Join(cells, " ")
	if selected {
		return st.Sel.Render(padRight(line, m.width))
	}
	return line
}

// nameCell renders the label column, including the expand marker and the
// indent that distinguishes a breakdown row from its parent.
func (m Model) nameCell(b model.Bucket, width int, st Styles) string {
	label := b.Label
	if label == "" {
		label = b.Key
	}

	if b.Depth > 0 {
		body := "  └ " + label
		if b.Agent != "" {
			body = "  └ " + label
			return padRight(st.Faint.Render("  └ ")+agentStyle(b.Agent).Render(truncate(label, max(0, width-4))), width)
		}
		return st.Child.Render(padRight(truncate(body, width), width))
	}

	fit := truncate
	if m.view == aggregate.ViewProject {
		fit = truncateLeft
	}

	marker := "  "
	if len(b.Children) > 0 {
		if m.open[b.Key] {
			marker = st.Key.Render("▾ ")
		} else {
			marker = st.Faint.Render("▸ ")
		}
	}
	styled := st.Text.Render(fit(label, max(0, width-2)))
	if b.Agent != "" && m.view == aggregate.ViewTime && m.period == aggregate.PeriodToday {
		styled = agentStyle(b.Agent).Render(fit(label, max(0, width-2)))
	}
	return padRight(marker+styled, width)
}

func (m Model) totalRow(cols []col, st Styles, width int) string {
	sum := aggregate.Sum(m.rows)
	cells := make([]string, 0, len(cols))
	for _, c := range cols {
		switch c.id {
		case "name":
			cells = append(cells, padCol("TOTAL", c.width, false))
		case "total":
			cells = append(cells, padCol(model.FmtTokens(sum.Total()), c.width, true))
		case "bar":
			// A total has nothing to compare against, so the bar slot carries
			// the cache hit rate instead of a permanently full bar.
			txt := "cache " + model.FmtPct(sum.CacheHit())
			if fancyEnabled() {
				// A high hit rate is the good outcome, so it reads cool.
				cells = append(cells, padVisible(
					gradientByValue(txt, 1-sum.CacheHit(), rampVolume()), c.width, false))
			} else {
				cells = append(cells, padCol(txt, c.width, false))
			}
		case "in":
			cells = append(cells, padCol(dashZero(sum.Input), c.width, true))
		case "cacher":
			cells = append(cells, padCol(dashZero(sum.CacheRead), c.width, true))
		case "cachew":
			cells = append(cells, padCol(dashZero(sum.CacheWrite), c.width, true))
		case "out":
			cells = append(cells, padCol(dashZero(sum.Output), c.width, true))
		case "think":
			cells = append(cells, padCol(dashZero(sum.Reasoning), c.width, true))
		case "calls":
			cells = append(cells, padCol(model.Comma(sum.Calls), c.width, true))
		case "avg":
			cells = append(cells, padCol(model.FmtTokens(sum.PerCall()), c.width, true))
		default:
			cells = append(cells, strings.Repeat(" ", c.width))
		}
	}
	return st.Total.Render(padRight(" "+strings.Join(cells, " "), width))
}

func renderBar(n, scale int64, width int, st Styles) string {
	if width <= 0 {
		return ""
	}
	filled := strings.TrimRight(model.Bar(n, scale, width), " ")
	rest := width - len([]rune(filled))
	if rest < 0 {
		rest = 0
	}
	return gradientBar(filled, rest, rampVolume(), st.Track)
}

func dashZero(n int64) string {
	if n == 0 {
		return "·"
	}
	return model.FmtTokens(n)
}

// heatView draws usage by hour of day across the week, revealing when the
// tokens are actually being spent.
func (m Model) heatView(width int, st Styles) []string {
	days := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	cellW := 2
	if width < 4+24*2+12 {
		cellW = 1
	}

	// The row labels below occupy " Mon " -- five cells -- so the hour ruler
	// has to start at the same offset or every label sits one column off.
	const rowLabelW = 5
	var axis strings.Builder
	axis.WriteString(strings.Repeat(" ", rowLabelW))
	for h := 0; h < 24; h += 3 {
		axis.WriteString(padRight(itoa2(h), cellW*3))
	}
	out := []string{
		st.Header.Render(" WHEN THE TOKENS GO"),
		st.Rule.Render(strings.Repeat("─", max(0, width))),
		st.HeatAxis.Render(axis.String()),
	}

	for d := 0; d < 7; d++ {
		var b strings.Builder
		b.WriteString(st.Faint.Render(" " + days[d] + " "))
		var rowTotal int64
		heat := rampHeat()
		for h := 0; h < 24; h++ {
			v := m.heat.Cells[d][h]
			rowTotal += v
			cell := model.HeatCell(v, m.heat.Min, m.heat.Max)
			pad := cell
			if cellW == 2 {
				pad = cell + cell
			}
			switch {
			case v <= 0:
				b.WriteString(st.HeatOff.Render(pad))
			case fancyEnabled():
				// Colour follows the same log scale as the glyph ramp, so a
				// quiet hour is cool and dim rather than invisible.
				b.WriteString(gradientByValue(pad, model.HeatLevel(v, m.heat.Min, m.heat.Max), heat))
			default:
				b.WriteString(st.HeatOn.Render(pad))
			}
		}
		b.WriteString(st.Faint.Render("  " + model.FmtTokens(rowTotal)))
		out = append(out, truncate(b.String(), width))
	}

	out = append(out, "")
	peakD, peakH := m.heatPeak()
	if m.heat.Total > 0 {
		out = append(out, st.Faint.Render(
			"  peak  "+days[peakD]+" "+itoa2(peakH)+":00   "+
				model.FmtTokens(m.heat.Cells[peakD][peakH])+
				"   total "+model.FmtTokens(m.heat.Total)))
	} else {
		out = append(out, st.Faint.Render("  nothing recorded in this range"))
	}
	return out
}

func (m Model) heatPeak() (day, hour int) {
	var best int64 = -1
	for d := 0; d < 7; d++ {
		for h := 0; h < 24; h++ {
			if m.heat.Cells[d][h] > best {
				best, day, hour = m.heat.Cells[d][h], d, h
			}
		}
	}
	return day, hour
}

func itoa2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// detailView expands the selected row into the figures that do not fit as
// columns.
func (m Model) detailView(width int, st Styles) []string {
	out := []string{st.Rule.Render(strings.Repeat("─", max(0, width)))}
	if m.cursor < 0 || m.cursor >= len(m.flat) {
		for len(out) < detailHeight {
			out = append(out, "")
		}
		return out[:detailHeight]
	}
	b := m.flat[m.cursor]
	t := b.Totals

	title := b.Label
	if b.Project != "" && m.view == aggregate.ViewProject && b.Depth == 0 {
		title = b.Project
	}
	if b.Model != "" && m.view == aggregate.ViewModel && b.Depth == 0 {
		title = b.Model
	}
	out = append(out, st.Text.Render(truncate(" "+title, width)))

	pairs := [][2]string{
		{"total", model.FmtTokens(t.Total())},
		{"fresh", model.FmtTokens(t.Fresh())},
		{"cache hit", model.FmtPct(t.CacheHit())},
		{"calls", model.Comma(t.Calls)},
		{"avg/call", model.FmtTokens(t.PerCall())},
		{"thinking", model.FmtPct(t.ReasonShare()) + " of out"},
	}
	var line1, line2 []string
	for i, p := range pairs {
		cell := st.Faint.Render(p[0]+" ") + st.Text.Render(p[1])
		if i < 3 {
			line1 = append(line1, padRight(cell, 22))
		} else {
			line2 = append(line2, padRight(cell, 22))
		}
	}
	out = append(out, truncate(" "+strings.Join(line1, ""), width))
	out = append(out, truncate(" "+strings.Join(line2, ""), width))

	breakdown := " " + st.Faint.Render("in ") + model.FmtTokens(t.Input) +
		st.Faint.Render("  cache.r ") + model.FmtTokens(t.CacheRead) +
		st.Faint.Render("  cache.w ") + model.FmtTokens(t.CacheWrite) +
		st.Faint.Render("  out ") + model.FmtTokens(t.Output)
	out = append(out, truncate(breakdown, width))

	for len(out) < detailHeight {
		out = append(out, "")
	}
	return out[:detailHeight]
}
