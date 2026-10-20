package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"token-count/internal/model"
)

// windowText describes a window, in forms from the longest to the shortest,
// for a line that has to give up some of it.
type windowText struct {
	full    string // with how many days it is: "2026-10-06 → 2026-10-19 · 14 days"
	short   string // the dates alone
	compact string // the dates without their years
}

// describeWindow words the window [since, until] as the dates it admits. An
// open start is named by first, the earliest record in scope, which is where
// the figures really begin; with no record there is nothing to name. now
// decides which day is today.
func describeWindow(since, until *time.Time, first, now time.Time) windowText {
	from, to := windowDates(since, until)
	same := func(s string) windowText { return windowText{s, s, s} }
	switch {
	case from != "" && to != "":
		if from == to {
			label := "1 day"
			if from == now.In(time.Local).Format(dateLayout) {
				label = "today"
			}
			return windowText{from + " · " + label, from, monthDay(from)}
		}
		dates := from + " → " + to
		w := windowText{dates, dates, monthDay(from) + " → " + monthDay(to)}
		if n := daysFrom(from, to); n > 1 {
			w.full += " · " + plural(n, "day")
		}
		return w
	case to != "":
		if first.IsZero() {
			return windowText{"all history → " + to, "all history → " + to, "all history → " + monthDay(to)}
		}
		start := first.In(time.Local).Format(dateLayout)
		w := same("since " + start + " → " + to)
		w.compact = "since " + monthDay(start) + " → " + monthDay(to)
		return w
	case from != "":
		w := same("from " + from)
		w.compact = "from " + monthDay(from)
		return w
	default:
		return same("all history")
	}
}

// monthDay drops the year from a YYYY-MM-DD date.
func monthDay(date string) string {
	if len(date) == len(dateLayout) {
		return date[5:]
	}
	return date
}

// daysFrom counts the calendar dates from one YYYY-MM-DD date to another,
// both included.
func daysFrom(from, to string) int {
	a, err1 := time.Parse(dateLayout, from)
	b, err2 := time.Parse(dateLayout, to)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(b.Sub(a)/(24*time.Hour)) + 1
}

// utcOffset names the local offset at t, as the scope line and the plain
// header print it.
func utcOffset(t time.Time) string { return "UTC" + t.In(time.Local).Format("-07:00") }

// chip is one active filter on the scope line. rank orders them for when the
// line is too narrow: the lowest goes first.
type chip struct {
	text string
	rank int
}

// chips are the filters narrowing the table, in the order the scope line
// shows them. The search term outranks the rest, since it is the filter most
// likely to empty the table, then the agent, the split by model and the
// sort.
func (m Model) chips(st Styles) []chip {
	var out []chip
	if m.agent != "" {
		out = append(out, chip{agentStyle(m.agent).Render(m.agent), 2})
	}
	if m.byModel {
		out = append(out, chip{st.Muted.Render("by model"), 1})
	}
	if m.sort != sortDefault {
		out = append(out, chip{st.Muted.Render("↓" + m.sort.String()), 0})
	}
	if q := m.trimmedSearch(); q != "" {
		out = append(out, chip{st.Key.Render("/" + q), 3})
	}
	return out
}

// scopeLine says what the table covers: the window it shows, the offset its
// dates are local to and the filters narrowing it. While the search box is
// open, the box takes its place.
func (m Model) scopeLine(w int, st Styles) string {
	if m.input.Focused() {
		return m.searchLine(w, st)
	}
	at, since, until := m.asOf, m.since, m.until
	if at.IsZero() {
		// Before the first computation there is no window yet, so the scan
		// screen describes the one it is loading for.
		at = m.opts.now()
		since, until = window(m.opts, m.period, at)
	}
	return fitScope(w, st, describeWindow(since, until, m.first, at), utcOffset(at), m.chips(st))
}

// fitScope lays the scope line out in w cells. When it does not fit, the
// window gives way first: its day count, then the offset, then the years,
// then the window itself, since the tab bar still names the span. The
// filters say why the figures are what they are, which nothing else on
// screen does, so they go last, the lowest ranked first, and the search term
// is cut rather than dropped.
func fitScope(w int, st Styles, win windowText, offset string, chips []chip) string {
	sep := st.Faint.Render(" · ")
	line := func(head []string, chips []chip) string {
		parts := append([]string{}, head...)
		for _, c := range chips {
			parts = append(parts, c.text)
		}
		return " " + strings.Join(parts, sep)
	}
	for _, head := range [][]string{
		{st.Dim.Render(win.full), st.Faint.Render(offset)},
		{st.Dim.Render(win.short), st.Faint.Render(offset)},
		{st.Dim.Render(win.short)},
		{st.Dim.Render(win.compact)},
	} {
		if s := line(head, chips); ansi.StringWidth(s) <= w {
			return s
		}
	}
	if len(chips) == 0 {
		return truncate(line([]string{st.Dim.Render(win.compact)}, nil), w)
	}
	for {
		s := line(nil, chips)
		if ansi.StringWidth(s) <= w || len(chips) == 1 {
			return truncate(s, w)
		}
		lowest := 0
		for i, c := range chips {
			if c.rank < chips[lowest].rank {
				lowest = i
			}
		}
		chips = append(chips[:lowest:lowest], chips[lowest+1:]...)
	}
}

// searchMinWidth is the least room the search box is given before the hints
// beside it give way.
const searchMinWidth = 12

// searchLine is the scope line while the search box is open: the box, how
// many records match and how to leave. The hints give way to the term, so
// what is being typed stays in view at any width.
func (m Model) searchLine(w int, st Styles) string {
	prompt := " " + st.Key.Render("/") + " "
	count := plural(len(m.scoped), "record") + " match"
	if len(m.scoped) == 1 {
		count += "es"
	}
	need := max(ansi.StringWidth(m.input.Value())+1, searchMinWidth)
	for _, hint := range []string{count + " · enter keep · esc clear", count} {
		room := w - 3 - 3 - ansi.StringWidth(hint)
		if room >= need {
			return truncate(prompt+fitInput(m.input, room)+"   "+st.Faint.Render(hint), w)
		}
	}
	return truncate(prompt+fitInput(m.input, w-3), w)
}

// caption names what the cards below it cover, which is not what the table
// covers: they would otherwise read as the answer to the search typed just
// above them.
func (m Model) caption(w int, st Styles) string {
	parts := []string{"OVERVIEW", "all agents"}
	if m.agent != "" {
		parts[1] = m.agent
	}
	if m.opts.Since != nil || m.opts.Until != nil {
		parts = append(parts, "within --since/--until")
	}
	parts = append(parts, "not narrowed by span or search")
	return st.Faint.Render(truncate(" "+strings.Join(parts, " · "), w))
}

// agentSubset names the agents a load was limited to, or "" when it read all
// of them.
func agentSubset(agents []string) string {
	all := map[string]bool{}
	for _, a := range model.Agents {
		all[a] = true
	}
	for _, a := range agents {
		delete(all, a)
	}
	if len(agents) == 0 || len(all) == 0 {
		return ""
	}
	return strings.Join(agents, ",")
}
