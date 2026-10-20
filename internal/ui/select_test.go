package ui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

// selected is the key of the selected row, or "" when there is none.
func selected(m Model) string {
	if m.cursor < 0 || m.cursor >= len(m.flat) {
		return ""
	}
	return m.flat[m.cursor].Key
}

// down moves the selection n rows down.
func down(m Model, n int) Model {
	for i := 0; i < n; i++ {
		m = send(m, runes("j"))
	}
	return m
}

// assertSelected fails unless the row with the given key is selected and on
// screen.
func assertSelected(t *testing.T, m Model, after, want string) {
	t.Helper()
	if got := selected(m); got != want {
		t.Errorf("after %s the selection is %q, want %q", after, got, want)
	}
	if m.cursor < m.offset || m.cursor >= m.offset+m.bodyRows() {
		t.Errorf("after %s the selection is off screen: row %d, rows %d..%d", after, m.cursor, m.offset, m.offset+m.bodyRows()-1)
	}
}

// A recompute that keeps the table's shape keeps the selection on its row,
// or on the row's parent when the row itself is gone. A sort, a filter, a
// rescan or expanding every row used to send the cursor back to the top, or
// leave it on whatever row ended up at the same index.
func TestSelectionFollowsItsRow(t *testing.T) {
	// The sample's days are listed newest first, each opening into grok,
	// codex and claude; the older a day, the larger its total.
	m := send(newTestModel(t, 120, 40), runes("E"))
	m = down(m, 6)
	assertSelected(t, m, "moving down", "2026-08-24/codex")

	// Sorting by total puts the oldest day first.
	assertSelected(t, send(m, runes("s")), "sorting", "2026-08-24/codex")
	// With only claude listed the codex row is gone, and its day takes the
	// selection, which it keeps when every agent comes back.
	claude := send(m, runes("a"))
	assertSelected(t, claude, "filtering to claude", "2026-08-24")
	assertSelected(t, send(claude, runes("A")), "clearing the filter", "2026-08-24")
	// Collapsing hides the row; its day takes the selection.
	assertSelected(t, send(m, runes("C")), "collapsing", "2026-08-24")
	// Split by model, the agent rows are other rows.
	assertSelected(t, send(m, runes("m")), "splitting by model", "2026-08-24")
	// Each letter of a search applies at once; the codex row goes at the
	// first letter of grok, and the day keeps the selection while it lasts.
	searched := send(m, runes("/"), runes("g"), runes("r"), runes("o"), runes("k"))
	assertSelected(t, searched, "searching for grok", "2026-08-24")
	assertSelected(t, send(searched, key(tea.KeyEsc)), "clearing the search", "2026-08-24")

	// Expanding every row keeps a top-level selection, which moves down
	// past the rows opened above it.
	m = send(m, runes("C"))
	assertSelected(t, send(m, runes("E")), "expanding", "2026-08-24")

	// A rescan whose records lost today's day moves the row up.
	var older []model.Usage
	for _, r := range m.records {
		if !r.Date().Equal(aggregate.Day(testNow)) {
			older = append(older, r)
		}
	}
	assertSelected(t, send(m, loadedMsg{records: older}), "a rescan", "2026-08-24")
}

// A selection deep in a long table stays on screen when a sort moves its row
// elsewhere.
func TestSelectionStaysOnScreen(t *testing.T) {
	pinLocal(t)
	var recs []model.Usage
	for i := 0; i < 30; i++ {
		// Totals rise with i; calls rank the projects in another order.
		u, ok := model.Make(model.MakeOpts{
			Agent:   "claude",
			Time:    testNow.Add(-time.Hour),
			Project: fmt.Sprintf("/w/p%02d", i),
			Input:   int64(100 * (i + 1)),
			Calls:   int64((i+13)%30 + 1),
		})
		if !ok {
			t.Fatal("record rejected")
		}
		recs = append(recs, u)
	}
	m := New(Options{Period: aggregate.PeriodAll, View: aggregate.ViewProject, Agents: model.Agents, Now: testClock})
	m.width, m.height = 120, 20
	m = send(m, loadedMsg{records: recs}, runes("G"))
	assertSelected(t, m, "going to the end", "/w/p00")
	m = send(m, runes("s"), runes("s"))
	if m.sort != sortCalls {
		t.Fatalf("sort is %s", m.sort)
	}
	assertSelected(t, m, "sorting by calls", "/w/p00")
	if m.cursor == 0 || m.cursor == len(m.flat)-1 {
		t.Fatalf("the fixture does not move the row to the middle: row %d of %d", m.cursor, len(m.flat))
	}
}

// A new span or view is a different table, so the selection starts again at
// the top.
func TestSpanAndViewChangesStartAtTheTop(t *testing.T) {
	for _, k := range []tea.KeyMsg{key(tea.KeyRight), key(tea.KeyLeft), key(tea.KeyTab), runes("p"), runes("2")} {
		m := down(send(newTestModel(t, 120, 40), runes("E")), 5)
		if m = send(m, k); m.cursor != 0 || m.offset != 0 {
			t.Errorf("%s left the selection at row %d, offset %d", k, m.cursor, m.offset)
		}
	}
}
