package ui

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"token-count/internal/aggregate"
)

// sgrAt matches one complete SGR sequence at the start of a string; termenv
// styles emit nothing else, and close every style with ESC[0m.
var sgrAt = regexp.MustCompile("^\x1b\\[([0-9;:]*)m")

// styleLeak says why printing s would disturb whatever is printed after it:
// an escape sequence cut off before its final byte, or a style that is still
// set at the end. It returns "" for a well-formed string.
func styleLeak(s string) string {
	open := false
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		m := sgrAt.FindStringSubmatch(s[i:])
		if m == nil {
			return fmt.Sprintf("broken escape sequence at byte %d", i)
		}
		open = m[1] != "" && m[1] != "0"
		i += len(m[0]) - 1
	}
	if open {
		return "a style is still set at the end"
	}
	return ""
}

// styledSamples are the kinds of styled text the layout helpers are handed:
// padded tabs, per-glyph gradients, nested styles and double-width text.
func styledSamples() []string {
	st := newStyles()
	return []string{
		st.TabOn.Render("today") + st.Tab.Render("day") + st.Tab.Render("week"),
		gradientText("tcount usage", rampTitle()),
		gradientBold("1.11B", rampVolume()),
		st.Faint.Render("  └ ") + agentStyle("claude").Render("claude"),
		st.Key.Render("中文路径") + st.Muted.Render(" mixed width"),
	}
}

// A styled string must cut to exactly the text its plain form would, within
// the width, and never leave a style open. Measuring with escape bytes counted
// as cells used to cut the coloured tab bar down to "today", and a cut inside
// a styled run dropped the reset so the colour bled into the next cell.
func TestTruncateIsANSIAware(t *testing.T) {
	withFancy(t, true)
	for _, styled := range styledSamples() {
		plain := stripANSI(styled)
		for w := 0; w <= lipgloss.Width(plain)+1; w++ {
			got := truncate(styled, w)
			if vis, want := stripANSI(got), truncate(plain, w); vis != want {
				t.Errorf("truncate(%q, %d) shows %q, want %q", plain, w, vis, want)
			}
			if gw := lipgloss.Width(got); gw > w {
				t.Errorf("truncate(%q, %d) is %d wide", plain, w, gw)
			}
			if leak := styleLeak(got); leak != "" {
				t.Errorf("truncate(%q, %d): %s: %q", plain, w, leak, got)
			}
		}
	}
	// The plain reference itself: a prefix plus an ellipsis, as wide as allowed.
	if got := truncate("中文路径 mixed width", 6); got != "中文…" {
		t.Errorf("plain truncate = %q, want %q", got, "中文…")
	}
}

// truncateLeft keeps the tail, measured the same ANSI-aware way, and never
// overshoots the width when a double-width character straddles the cut.
func TestTruncateLeftIsANSIAware(t *testing.T) {
	withFancy(t, true)
	cases := []struct {
		s     string
		width int
		want  string
	}{
		{"/home/u/projects/token_count", 12, "…token_count"},
		{"/home/u/项目/中文目录", 12, "…目/中文目录"},
		// 目 straddles the cut at 11, so it goes too: one cell short beats
		// one cell over.
		{"/home/u/项目/中文目录", 11, "…/中文目录"},
		{"/home/u/项目/中文目录", 1, "…"},
	}
	for _, c := range cases {
		if got := truncateLeft(c.s, c.width); got != c.want {
			t.Errorf("truncateLeft(%q, %d) = %q, want %q", c.s, c.width, got, c.want)
		}
	}
	for _, styled := range append(styledSamples(), agentStyle("codex").Render("/home/u/项目/中文目录")) {
		plain := stripANSI(styled)
		for w := 1; w <= lipgloss.Width(plain)+1; w++ {
			got := truncateLeft(styled, w)
			if vis, want := stripANSI(got), truncateLeft(plain, w); vis != want {
				t.Errorf("truncateLeft(%q, %d) shows %q, want %q", plain, w, vis, want)
			}
			if gw := lipgloss.Width(got); gw > w {
				t.Errorf("truncateLeft(%q, %d) is %d wide", plain, w, gw)
			}
			if leak := styleLeak(got); leak != "" {
				t.Errorf("truncateLeft(%q, %d): %s", plain, w, leak)
			}
		}
	}
}

// Padding and the left/right join measure visible cells, so a styled cell
// lands exactly on its width, even when a double-width character straddles
// the cut and truncation alone would leave it a cell short.
func TestPaddingMeasuresVisibleCells(t *testing.T) {
	withFancy(t, true)
	for _, styled := range styledSamples() {
		plain := stripANSI(styled)
		for w := 0; w <= lipgloss.Width(plain)+4; w++ {
			for name, got := range map[string]string{
				"padRight": padRight(styled, w),
				"padLeft":  padLeft(styled, w),
				"joinLR":   joinLR(styled, newStyles().Muted.Render("right"), w),
			} {
				if gw := lipgloss.Width(got); gw > w || (name != "joinLR" && gw != w) {
					t.Errorf("%s(%q, %d) is %d wide", name, plain, w, gw)
				}
				if leak := styleLeak(got); leak != "" {
					t.Errorf("%s(%q, %d): %s", name, plain, w, leak)
				}
			}
		}
	}
}

// At 60 columns with colour on, the tab bar must keep every span tab and the
// start of the views, not just "today".
func TestTabBarSurvivesNarrowColourTerminal(t *testing.T) {
	withFancy(t, true)
	m := newTestModel(t, 60, 30)
	bar := m.tabBar(60, newStyles())
	vis := stripANSI(bar)
	for _, want := range []string{" today ", " day ", " week ", " month ", " year ", " all ", " time "} {
		if !strings.Contains(vis, want) {
			t.Errorf("60-column tab bar lost %q: %q", want, vis)
		}
	}
	if w := lipgloss.Width(bar); w > 60 {
		t.Errorf("tab bar is %d wide", w)
	}
	if leak := styleLeak(bar); leak != "" {
		t.Errorf("tab bar: %s", leak)
	}
}

// screen is one state of the TUI, named for the failure message.
type screen struct {
	what string
	m    Model
}

// screens is every state the screen can be in at one size: each view, with
// and without the detail panel, the search and help overlays, the scan, an
// empty result for each reason the table can give, the dedup notice and an
// error.
func screens(t *testing.T, w, h int) []screen {
	t.Helper()
	var out []screen
	add := func(what string, edit func(m *Model)) {
		m := newTestModel(t, w, h)
		edit(&m)
		m.recompute()
		out = append(out, screen{what, m})
	}
	for _, view := range views() {
		add(string(view), func(m *Model) { m.view = view })
		// The detail panel only has content where a row is selected.
		if view == aggregate.ViewTime || view == aggregate.ViewProject {
			add(string(view)+" with detail", func(m *Model) { m.view, m.detail = view, true })
		}
	}
	add("searching", func(m *Model) { m.seeking, m.search = true, "claude" })
	add("help", func(m *Model) { m.help = true })
	add("scanning", func(m *Model) { m.loading, m.spin = true, 4 })
	add("no records", func(m *Model) { m.records = nil })
	add("no records, hours", func(m *Model) { m.records, m.view = nil, aggregate.ViewHeat })
	add("search miss", func(m *Model) { m.search = "no-such-project-anywhere-in-the-logs-" + strings.Repeat("x", 48) })
	add("agent without usage", func(m *Model) { m.agent = "gemini" })
	add("dates too narrow", func(m *Model) {
		future := testNow.AddDate(1, 0, 0)
		m.opts.Since = &future
	})
	add("dedup notice", func(m *Model) { m.dropped, m.dupToks = 1_234_567, 98_765_432_100 })
	add("error", func(m *Model) {
		m.err = errors.New("open /home/u/.claude/projects/-home-u-a-long-project/session.jsonl: permission denied")
	})
	return out
}

// checkEveryScreenFits renders every state at every width from 30 columns,
// the narrowest the layout is drawn for, to 200, and requires the height to
// be exact, each line to fit and no line to leak its styling into the next.
// Every state is drawn at a tall height; the states short selects are drawn
// at the shortest height too, which has no room for the cards and only the
// unboxed help.
func checkEveryScreenFits(t *testing.T, short func(what string) bool) {
	t.Helper()
	// Every width, because the layout has breakpoints all through the range;
	// -short samples it for a quick edit-test loop.
	step := 1
	if testing.Short() {
		step = 7
	}
	for w := minWidth; w <= 200; w += step {
		for _, h := range []int{minHeight, 30} {
			for _, s := range screens(t, w, h) {
				if h == minHeight && !short(s.what) {
					continue
				}
				lines := strings.Split(s.m.View(), "\n")
				if len(lines) != h {
					t.Fatalf("%dx%d %s: %d lines", w, h, s.what, len(lines))
				}
				for i, ln := range lines {
					if got := lipgloss.Width(ln); got > w {
						t.Fatalf("%dx%d %s: line %d is %d wide: %q", w, h, s.what, i, got, stripANSI(ln))
					}
					if leak := styleLeak(ln); leak != "" {
						t.Fatalf("%dx%d %s: line %d: %s: %q", w, h, s.what, i, leak, ln)
					}
				}
			}
		}
	}
}

// With colour on, every line of every screen fits the terminal at any width,
// and none leaks its styling into the next line. The scan, the empty states
// and the heatmap's summary used to run past a narrow terminal.
func TestColourViewFitsEveryWidth(t *testing.T) {
	withFancy(t, true)
	// At the short height colour changes only the unboxed help, and the
	// gradients make every render slow, so the rest is drawn short only in
	// the monochrome sweep.
	checkEveryScreenFits(t, func(what string) bool { return what == "help" || what == "time" })
}

// Without colour the mascot is gone, and several screens take their plainer
// branch, which has to fit just the same.
func TestMonochromeViewFitsEveryWidth(t *testing.T) {
	withFancy(t, false)
	checkEveryScreenFits(t, func(string) bool { return true })
}

// cellStyle is the part of a printed cell's style these tests look at.
type cellStyle struct{ bold, background bool }

// eachCell walks s and reports every printed rune with the style in force
// where it is printed, following the SGR sequences termenv emits.
func eachCell(t *testing.T, s string, visit func(r rune, st cellStyle)) {
	t.Helper()
	var st cellStyle
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			r, size := utf8.DecodeRuneInString(s[i:])
			visit(r, st)
			i += size
			continue
		}
		m := sgrAt.FindStringSubmatch(s[i:])
		if m == nil {
			t.Fatalf("broken escape sequence at byte %d of %q", i, s)
		}
		params := strings.Split(m[1], ";")
		for j := 0; j < len(params); j++ {
			switch p := params[j]; p {
			case "", "0":
				st = cellStyle{}
			case "1":
				st.bold = true
			case "22":
				st.bold = false
			case "49":
				st.background = false
			case "38", "48":
				// An extended colour: 5;n or 2;r;g;b.
				st.background = st.background || p == "48"
				if j+1 < len(params) && params[j+1] == "5" {
					j += 2
				} else {
					j += 4
				}
			default:
				if n, err := strconv.Atoi(p); err == nil && (n >= 40 && n <= 47 || n >= 100 && n <= 107) {
					st.background = true
				}
			}
		}
		i += len(m[0])
	}
}

// The highlight has to run the whole width of the selected row. It used to
// wrap the row once, and the reset that closes each coloured cell also
// closed the highlight, so only the cells before the first coloured one were
// lit.
func TestSelectionLightsTheWholeRow(t *testing.T) {
	withFancy(t, true)
	for _, view := range []aggregate.View{aggregate.ViewTime, aggregate.ViewProject, aggregate.ViewModel} {
		for _, byModel := range []bool{false, true} {
			m := newTestModel(t, 120, 40)
			m.period, m.view, m.byModel = aggregate.PeriodToday, view, byModel
			m.recompute()
			m.setAllOpen(true)
			for m.cursor = 0; m.cursor < len(m.flat); m.cursor++ {
				row := m.tableView(120, newStyles())[2+m.cursor]
				var unlit strings.Builder
				eachCell(t, row, func(r rune, st cellStyle) {
					if !st.background {
						unlit.WriteRune(r)
					}
				})
				if unlit.Len() > 0 {
					t.Errorf("%s by-model %v row %d: %q is not lit in %q",
						view, byModel, m.cursor, unlit.String(), stripANSI(row))
				}
				if got := lipgloss.Width(row); got != 120 {
					t.Errorf("%s row %d is %d wide, want the full 120", view, m.cursor, got)
				}
			}
		}
	}
}

// The TOTAL row is bold throughout. The cache hit rate in the bar's slot has
// its own colour, and its reset used to end the row's bold for every figure
// after it.
func TestTotalRowStaysBold(t *testing.T) {
	withFancy(t, true)
	for _, fancy := range []bool{true, false} {
		fancyOn = fancy
		m := newTestModel(t, 160, 30)
		lines := m.tableView(160, newStyles())
		total := lines[len(lines)-1]
		var plain strings.Builder
		eachCell(t, total, func(r rune, st cellStyle) {
			if r != ' ' && !st.bold {
				plain.WriteRune(r)
			}
		})
		if plain.Len() > 0 {
			t.Errorf("fancy %v: %q is not bold in %q", fancy, plain.String(), stripANSI(total))
		}
	}
}
