package ui

import (
	"strings"
	"testing"
	"time"

	"token-count/internal/aggregate"
	"token-count/internal/model"
	"token-count/internal/source"
)

// Chrome goes in a fixed order as the terminal gets shorter: the mascot with
// the cards, then the spacer and the rule, then the dedup notice, then the
// detail panel, and last the warning that the totals are short. What stays
// always leaves the table its rows, and the frame is exactly the terminal's
// height.
func TestChromeGivesWayInOrder(t *testing.T) {
	withFancy(t, true)
	for _, warn := range []bool{false, true} {
		for _, w := range []int{60, 84, 120} {
			for h := minHeight; h <= 45; h++ {
				m := newTestModel(t, w, h)
				m.dropped, m.dupToks, m.detail = 3, 1000, true
				if warn {
					m.issues = []source.Issue{{Agent: "codex", Path: "/w/x.jsonl", Err: "read: input/output error"}}
				}
				m.setAllOpen(true)
				l := m.layout()
				// Each piece of chrome is kept only if everything dropped
				// after it is kept too. The warning outranks the dedup
				// notice, so only one of them is ever in the running.
				type piece struct {
					name string
					on   bool
				}
				order := []piece{{"cards", l.cards}, {"spacer", l.spacer}, {"rule", l.rule}}
				if !warn {
					order = append(order, piece{"dedup notice", l.notice != ""})
				}
				order = append(order, piece{"detail", l.detail})
				if warn {
					order = append(order, piece{"warning", l.warn})
				}
				for i := 1; i < len(order); i++ {
					if order[i-1].on && !order[i].on {
						t.Errorf("warn %v %dx%d: %s is drawn without %s", warn, w, h, order[i-1].name, order[i].name)
					}
				}
				if l.cat && !l.cards {
					t.Errorf("warn %v %dx%d: the mascot is drawn without the cards", warn, w, h)
				}
				if l.cards || l.spacer || l.rule || l.notice != "" || l.detail {
					if l.body < min(minBodyRows, len(m.flat)) {
						t.Errorf("warn %v %dx%d: chrome kept while the table has %d rows", warn, w, h, l.body)
					}
				}
				if got := len(strings.Split(m.View(), "\n")); got != h {
					t.Errorf("warn %v %dx%d: %d lines", warn, w, h, got)
				}
			}
		}
	}
}

// An open TUI moves on to the new day at midnight. Its figures used to stay
// those of the day it was opened on: the today card went on counting
// yesterday, and the day span went on ending on it.
func TestMidnightRollsTheFiguresOver(t *testing.T) {
	pinLocal(t)
	now := time.Date(2026, 8, 25, 23, 59, 30, 0, testZone)
	m := New(Options{Period: aggregate.PeriodDay, Agents: model.Agents, Now: func() time.Time { return now }})
	m.width, m.height = 120, 30
	m = send(m, loadedMsg{records: sample(testNow)})
	if got := m.snapshot["today"].Totals.Records; got != 3 {
		t.Fatalf("the today card counts %d records before midnight, want 3", got)
	}
	before := m.asOf

	// Within the day the slow tick leaves the figures alone.
	now = now.Add(20 * time.Second)
	next, cmd := m.Update(clockMsg(now))
	if m = next.(Model); !m.asOf.Equal(before) {
		t.Errorf("a tick within the day recomputed the figures")
	}
	if cmd == nil {
		t.Fatal("the slow tick was not scheduled again")
	}

	now = now.Add(time.Minute)
	next, cmd = m.Update(clockMsg(now))
	m = next.(Model)
	if !m.asOf.Equal(now) {
		t.Fatalf("after midnight the figures are still as of %v", m.asOf)
	}
	if got := m.snapshot["today"].Totals.Records; got != 0 {
		t.Errorf("the today card counts %d records on a day without any", got)
	}
	if got := line(m, 2); !strings.HasPrefix(got, " 2026-08-13 → 2026-08-26 · 14 days") {
		t.Errorf("the scope line still describes the old day: %q", got)
	}
	if cmd == nil {
		t.Fatal("the slow tick was not scheduled again after midnight")
	}

	// While a scan runs, its result will bring the figures up to date.
	m.loading = true
	now = now.Add(24 * time.Hour)
	if next, _ = m.Update(clockMsg(now)); next.(Model).asOf.Equal(now) {
		t.Errorf("the slow tick recomputed in the middle of a scan")
	}
}
