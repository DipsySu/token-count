package ui

import (
	"github.com/charmbracelet/x/ansi"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

// Heights of the parts of a frame that layout weighs against each other.
const (
	cardsHeight = 5 // the overview caption and the four card lines
	// minBodyRows is how many table rows the optional chrome may not push
	// the table below, when it has that many.
	minBodyRows = 5
	// heatMin is the heat view without its title, rule and blank line: the
	// hour axis, the seven days and the peak. loadingMin is the plain scan
	// screen.
	heatMin    = 9
	loadingMin = 3
)

// Widths below which a part of the frame is not drawn at all.
const (
	cardsMinWidth = 60 // four cards of at least twelve cells
	catMinWidth   = 84 // the mascot beside them as well
)

// layout is what one frame draws besides the lines it always draws: the
// title, the tab bar, the scope line and the footer, and around a table its
// column header and total row. It is worked out in one place for the current
// size and state. Both bodyRows, which scrolling and paging go by, and
// compose, which draws the frame, read it, so the rows the cursor moves
// through are always the rows on screen.
type layout struct {
	cards  bool   // the overview caption and the four cards
	cat    bool   // the mascot beside the cards, and its mood on the tab bar
	spacer bool   // the blank line above the notice or the body
	rule   bool   // the rule under the table's column header
	notice string // the notice line, worded for the width; "" when there is none
	warn   bool   // the notice is the incomplete-read warning
	detail bool   // the detail panel
	// body is the lines left for the body: the table's rows, or the heat
	// view, the scan, an error or the explanation of an empty table.
	body int
}

// layout decides which optional chrome fits. It goes in a fixed order until
// the body has the room it needs: the cards, and the mascot that sits beside
// them, then the spacer and the rule, then the dedup notice, then the detail
// panel, which was asked for, and last the incomplete-read warning, which
// says the figures on screen are short.
func (m Model) layout() layout {
	fixed, need, ruled := m.frame()
	l := layout{
		// Before the first load there are no figures to show.
		cards:  m.gen > 0 && m.width >= cardsMinWidth,
		spacer: true,
		rule:   ruled,
		detail: m.detail && m.tabled(),
	}
	l.notice, l.warn = m.notice(m.width)
	free := func() int { return m.height - fixed - l.chrome() }
	for _, drop := range []func(){
		func() { l.cards = false },
		func() { l.spacer = false },
		func() { l.rule = false },
		func() {
			if !l.warn {
				l.notice = ""
			}
		},
		func() { l.detail = false },
		func() { l.notice, l.warn = "", false },
	} {
		if free() >= need {
			break
		}
		drop()
	}
	l.cat = l.cards && m.cat && fancyEnabled() && m.width >= catMinWidth
	l.body = max(free(), 0)
	return l
}

// chrome is how many lines the optional chrome takes.
func (l layout) chrome() int {
	n := 0
	if l.cards {
		n += cardsHeight
	}
	for _, on := range []bool{l.spacer, l.rule, l.notice != ""} {
		if on {
			n++
		}
	}
	if l.detail {
		n += detailHeight
	}
	return n
}

// frame is how many lines the current state always draws, how many its body
// needs before optional chrome gives way to it, and whether it has a column
// header with a rule under it. A table keeps its column header and total row
// and needs minBodyRows rows, or all of them when it has fewer; the heat view
// needs its whole grid.
func (m Model) frame() (fixed, need int, ruled bool) {
	fixed = 4 // the title, the tab bar, the scope line and the footer
	switch {
	case m.loading:
		return fixed, loadingMin, false
	case m.err != nil:
		return fixed, 1, false
	case m.view == aggregate.ViewHeat:
		return fixed, heatMin, false
	case len(m.flat) == 0:
		return fixed + 1, 2, true // the reason and the hint under the header
	default:
		return fixed + 2, min(minBodyRows, len(m.flat)), true
	}
}

// tabled reports whether the body is a table with rows in it, the only body
// the detail panel has anything to say about.
func (m Model) tabled() bool {
	return !m.loading && m.err == nil && m.view != aggregate.ViewHeat && len(m.flat) > 0
}

// notice words the notice line to fit the width. The incomplete-read warning
// outranks the dedup notice: it says the totals are short, where the other
// only explains why they are smaller than a naive count.
func (m Model) notice(w int) (text string, warn bool) {
	if m.loading {
		return "", false
	}
	if n := len(m.issues); n > 0 {
		// A log is a file or a directory that could not be listed.
		files := plural(n, "log")
		forms := []string{
			"⚠ " + files + " could not be read completely — totals are a lower bound (" + m.opts.Name + " --json lists them)",
			"⚠ " + files + " could not be read completely — totals are a lower bound",
			"⚠ " + files + " not fully read — totals are a lower bound",
			"⚠ " + files + " not fully read",
		}
		for _, f := range forms {
			if ansi.StringWidth(" "+f) <= w {
				return f, true
			}
		}
		return forms[len(forms)-1], true
	}
	if m.dropped > 0 {
		// Claude Code is not the only agent that repeats itself: Codex logs
		// copied between machines or resumed are deduplicated the same way.
		return "deduped " + plural(m.dropped, "repeated record") + " (" + model.FmtTokens(m.dupToks) +
			" tokens) — resumed sessions and copied logs replay history", false
	}
	return "", false
}

// plural counts a noun: "1 file", "2 files".
func plural(n int, noun string) string {
	s := model.Comma(int64(n)) + " " + noun
	if n != 1 {
		s += "s"
	}
	return s
}
