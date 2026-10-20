package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"token-count/internal/aggregate"
)

var (
	// headerLine is the table's column header, whatever its name column.
	headerLine = regexp.MustCompile(`^\s+(AGENT|DAY|WEEK|MONTH|YEAR|PROJECT|MODEL)\s+(MODEL\s+)?TOTAL\b`)
	// totalLine is the table's total row.
	totalLine = regexp.MustCompile(`^ (RANGE )?TOTAL\s`)
)

// tableRows counts the table rows on screen: the lines between the column
// header, with its rule, and the total row. It reports -1 when the frame has
// no complete table.
func tableRows(body string) int {
	head, total := -1, -1
	lines := strings.Split(stripANSI(body), "\n")
	for i, ln := range lines {
		switch {
		case head < 0 && headerLine.MatchString(ln):
			head = i
		case head >= 0 && totalLine.MatchString(ln):
			total = i
		}
	}
	if head < 0 || total < 0 {
		return -1
	}
	n := 0
	for _, ln := range lines[head+1 : total] {
		if strings.Trim(ln, "─ ") != "" {
			n++
		}
	}
	return n
}

// The optional chrome gives way to the table: on a short terminal the table
// keeps five rows, or all of them when it has fewer, as long as dropping
// chrome can make room. Only the title, the tab bar, the scope line, the
// column header, the total row and the footer always stay. The table used to
// get three rows on a short terminal while a blank line and the rule stayed.
func TestTableKeepsItsRowsBeforeChrome(t *testing.T) {
	for _, fancy := range []bool{false, true} {
		withFancy(t, fancy)
		for _, w := range []int{30, 60, 84, 120} {
			for h := minHeight; h <= 40; h++ {
				for _, open := range []bool{false, true} {
					m := newTestModel(t, w, h)
					m = send(m, loadedMsg{records: m.records, dropped: 3, dupToks: 1000})
					m.detail = true
					if open {
						m.setAllOpen(true)
					}
					body := m.View()
					want := min(5, min(len(m.flat), h-6))
					if got := tableRows(body); got < want {
						t.Fatalf("fancy %v %dx%d, %d rows: %d on screen, want at least %d:\n%s",
							fancy, w, h, len(m.flat), got, want, stripANSI(body))
					}
				}
			}
		}
	}
}

// Every frame is exactly the terminal's height, and no line is wider than
// it, at every height where the chrome changes and a few above, in every
// state that changes which chrome is drawn, with colour on and off. The
// widths are the breakpoints of the tab bar, the cards and the mascot either
// side; colour, which is slow to draw, is checked at fewer of them.
func TestFramesFitAtEveryHeight(t *testing.T) {
	var heights []int
	for h := minHeight; h <= 30; h++ {
		heights = append(heights, h)
	}
	heights = append(heights, 36, 45)
	for _, c := range []struct {
		fancy  bool
		widths []int
	}{
		{false, []int{30, 31, 44, 56, 57, 59, 60, 71, 72, 83, 84, 120}},
		{true, []int{30, 57, 60, 84, 120}},
	} {
		withFancy(t, c.fancy)
		for _, w := range c.widths {
			for _, h := range heights {
				if testing.Short() && h%3 != 0 {
					continue
				}
				for _, s := range screens(t, w, h) {
					lines := strings.Split(s.m.View(), "\n")
					if len(lines) != h {
						t.Fatalf("fancy %v %dx%d %s: %d lines", c.fancy, w, h, s.what, len(lines))
					}
					for i, ln := range lines {
						if got := lipgloss.Width(ln); got > w {
							t.Fatalf("fancy %v %dx%d %s: line %d is %d wide: %q", c.fancy, w, h, s.what, i, got, stripANSI(ln))
						}
						if leak := styleLeak(ln); leak != "" {
							t.Fatalf("fancy %v %dx%d %s: line %d: %s", c.fancy, w, h, s.what, i, leak)
						}
					}
				}
			}
		}
	}
}

// The heat view keeps the whole grid while chrome can make room for it.
func TestHeatGridKeepsItsDays(t *testing.T) {
	for h := minHeight; h <= 40; h++ {
		m := newTestModel(t, 120, h)
		m.view = aggregate.ViewHeat
		m.recompute()
		body := stripANSI(m.View())
		days := 0
		for _, d := range []string{" Mon ", " Tue ", " Wed ", " Thu ", " Fri ", " Sat ", " Sun "} {
			if strings.Contains(body, d) {
				days++
			}
		}
		if want := min(7, h-5); days < want {
			t.Errorf("120x%d: %d days of the grid on screen, want %d:\n%s", h, days, want, body)
		}
	}
}
