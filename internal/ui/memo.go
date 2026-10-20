package ui

import (
	"time"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

// memo holds what recompute derives that changes only when its inputs do, so
// that a keystroke redoes only the work it affects. One more letter in the
// search box used to filter and lowercase the whole archive and recompute
// every summary card. Over 100,000 records the memo saves little time, about
// 51ms down to 48ms a keystroke, but most of the garbage: about 41MB
// allocated a keystroke down to 10MB.
//
// Bubble Tea copies the model on every update, and the copies share one
// memo. Each entry carries the inputs it was computed from and is used only
// when they match, so a copy is never handed the answer to another question;
// at worst two copies take turns recomputing.
type memo struct {
	cardsKey cardsKey
	cards    *cardFigures
	baseKey  baseKey
	base     *base
}

// cardFigures are the summary cards and the yardsticks drawn from them.
type cardFigures struct {
	snap map[string]aggregate.Snap
	peak int64
	mood catMood
}

// cardsKey is everything the cards depend on. They ignore the span and the
// search, and they are computed to the minute, which is as precise as the
// title bar dates them, so a burst of keystrokes shares one computation.
type cardsKey struct {
	gen          int
	agent        string
	since, until bound // the explicit --since/--until dates
	minute       int64 // Unix minutes
}

// base is the records that the agent filter and the window select. It is
// what a search narrows, and neither of them changes while one is typed.
type base struct {
	recs []model.Usage
	// ids is each record's entry in the search index, worked out the first
	// time the base is searched.
	ids   []int32
	first time.Time // the earliest record, zero when there is none
}

type baseKey struct {
	gen          int
	agent        string
	since, until bound
}

// bound is an optional window bound in a form a cache key can compare.
type bound struct {
	nanos int64
	set   bool
}

func boundOf(t *time.Time) bound {
	if t == nil {
		return bound{}
	}
	return bound{nanos: t.UnixNano(), set: true}
}

// figures are the summary cards for the records, the agent filter and the
// explicit dates, as of the minute of asOf. They are computed as of the start
// of that minute, so that they are the same whichever moment in it asked.
func (m *Model) figures() *cardFigures {
	minute := m.asOf.Truncate(time.Minute)
	k := cardsKey{
		gen:    m.gen,
		agent:  m.agent,
		since:  boundOf(m.opts.Since),
		until:  boundOf(m.opts.Until),
		minute: minute.Unix() / 60,
	}
	if m.memo.cards != nil && m.memo.cardsKey == k {
		return m.memo.cards
	}
	// The cards ignore the span, so that they stay a stable reference while
	// the table below changes shape, but not explicit dates.
	recs := aggregate.FilterAgent(m.records, m.agent)
	if m.opts.Since != nil || m.opts.Until != nil {
		recs = aggregate.Filter(recs, m.opts.Since, m.opts.Until)
	}
	snap := aggregate.Snapshot(recs, minute)
	c := &cardFigures{snap: snap, peak: peakDay(snap)}
	c.mood = moodFor(snap["today"].Totals.Total(), c.peak)
	m.memo.cardsKey, m.memo.cards = k, c
	return c
}

// peakDay is the largest single-day total in the snapshot's trends.
func peakDay(snap map[string]aggregate.Snap) int64 {
	var peak int64
	for _, key := range []string{"year", "today"} {
		for _, v := range snap[key].Trend {
			peak = max(peak, v)
		}
	}
	return peak
}

// scopeBase is the records the agent filter and the window select.
func (m *Model) scopeBase() *base {
	k := baseKey{gen: m.gen, agent: m.agent, since: boundOf(m.since), until: boundOf(m.until)}
	if m.memo.base != nil && m.memo.baseKey == k {
		return m.memo.base
	}
	b := &base{recs: aggregate.Filter(aggregate.FilterAgent(m.records, m.agent), m.since, m.until)}
	for i := range b.recs {
		if t := b.recs[i].Time; b.first.IsZero() || t.Before(b.first) {
			b.first = t
		}
	}
	m.memo.baseKey, m.memo.base = k, b
	return b
}

// index is the search index of the records. Records arrive through
// setRecords, which builds it; a model put together any other way gets one
// here.
func (m *Model) index() *searchIndex {
	if m.ix == nil {
		m.ix = newSearchIndex(m.records)
	}
	return m.ix
}

// narrow applies the search to the base. Each combination of agent, model and
// project is tested once, and a record is kept when its combination matched.
func (m *Model) narrow(b *base) []model.Usage {
	q := searchTerm(m.search)
	if q == "" {
		return b.recs
	}
	ix := m.index()
	if b.ids == nil {
		b.ids = make([]int32, len(b.recs))
		for i := range b.recs {
			b.ids[i] = ix.id(&b.recs[i])
		}
	}
	hit := ix.matching(q)
	n := 0
	for _, id := range b.ids {
		if hit[id] {
			n++
		}
	}
	if n == len(b.recs) {
		return b.recs
	}
	out := make([]model.Usage, 0, n)
	for i, id := range b.ids {
		if hit[id] {
			out = append(out, b.recs[i])
		}
	}
	return out
}
