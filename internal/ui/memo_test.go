package ui

import (
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

// cardsOf identifies the summary cards a model holds, so that a test can tell
// a reused computation from a new one.
func cardsOf(m Model) uintptr { return reflect.ValueOf(m.snapshot).Pointer() }

// The summary cards depend only on the records, the agent filter, the
// explicit dates and the minute, so a keystroke that changes none of them
// reuses the cards it already has. Every keystroke used to recompute them
// over the whole archive, search letters included.
func TestCardsAreReusedUntilTheirInputsChange(t *testing.T) {
	pinLocal(t)
	now := testNow
	m := New(Options{Period: aggregate.PeriodDay, Agents: model.Agents, Now: func() time.Time { return now }})
	m.width, m.height = 120, 30
	m = send(m, loadedMsg{records: sample(testNow)})
	cards := cardsOf(m)

	for _, k := range []tea.KeyMsg{runes("/"), runes("c"), runes("o"), key(tea.KeyBackspace), key(tea.KeyEnter),
		key(tea.KeyRight), key(tea.KeyTab), runes("s"), runes("m"), runes("E"), runes("j"), runes("d")} {
		if m = send(m, k); cardsOf(m) != cards {
			t.Errorf("%s recomputed the cards", k)
		}
	}
	// Seconds later, within the same minute, they still stand.
	now = now.Add(30 * time.Second)
	if m = send(m, runes("s")); cardsOf(m) != cards {
		t.Error("a recompute within the minute recomputed the cards")
	}

	for _, c := range []struct {
		what string
		do   func(m Model) Model
	}{
		{"a new minute", func(m Model) Model { now = now.Add(time.Minute); return send(m, runes("s")) }},
		{"the agent filter", func(m Model) Model { return send(m, runes("a")) }},
		{"clearing it", func(m Model) Model { return send(m, runes("A")) }},
		{"a rescan", func(m Model) Model { return send(m, loadedMsg{records: m.records}) }},
	} {
		m = c.do(m)
		if cardsOf(m) == cards {
			t.Errorf("%s did not recompute the cards", c.what)
		}
		cards = cardsOf(m)
	}
}

// A search narrows the records the agent filter and the window select, which
// are kept between keystrokes. Whatever they were kept from, a search must
// select what a scan of the current records would.
func TestSearchNeverServesStaleRecords(t *testing.T) {
	m := newTestModel(t, 120, 30)
	check := func(what string, m Model, recs []model.Usage) {
		t.Helper()
		in := aggregate.Filter(aggregate.FilterAgent(recs, m.agent), m.since, m.until)
		if want := bruteMatches(in, m.search); len(m.scoped) != want {
			t.Errorf("%s: %d records, a scan finds %d", what, len(m.scoped), want)
		}
	}
	// Only grok's records have a k in them.
	m = send(m, runes("/"), runes("k"))
	check("searching", m, m.records)
	m = send(m, key(tea.KeyEnter), runes("a"), runes("a"))
	check("filtering to codex", m, m.records)
	m = send(m, runes("A"), key(tea.KeyRight))
	check("a new span", m, m.records)

	// A rescan with other records: two of each agent's days are gone.
	var fewer []model.Usage
	for _, r := range m.records {
		if r.Date().After(aggregate.Day(testNow).AddDate(0, 0, -2)) {
			fewer = append(fewer, r)
		}
	}
	m = send(m, loadedMsg{records: fewer})
	check("a rescan", m, fewer)
	if len(m.scoped) == 0 || len(m.scoped) == bruteMatches(sample(testNow), m.search) {
		t.Errorf("the rescan's records do not show: %d records", len(m.scoped))
	}
}
