package aggregate

import (
	"fmt"
	"math/bits"
	"slices"
	"sort"
	"time"

	"token-count/internal/model"
)

type Period string

const (
	PeriodToday Period = "today"
	PeriodDay   Period = "day"
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
	PeriodYear  Period = "year"
	PeriodAll   Period = "all"
)

// View selects what the main table is grouped by.
type View string

const (
	ViewTime    View = "time"
	ViewProject View = "project"
	ViewModel   View = "model"
	ViewHeat    View = "heat"
)

// Filter keeps the records dated from since through until; a nil bound leaves
// that side open. A record's date is its local calendar date. since admits
// the dates whose midnight, the instant Day gives for them, is not before it,
// and until those whose midnight is not after it, so a bound that is itself a
// midnight admits its own date. When every record is kept, Filter returns
// records itself.
//
// Rather than convert every timestamp to its local date, which dominated the
// cost, Filter turns the bounds into the instants at which the first admitted
// date begins and the last one ends, and compares timestamps with those.
func Filter(records []model.Usage, since, until *time.Time) []model.Usage {
	if since == nil && until == nil {
		return records
	}
	var from, to stamp
	if since != nil {
		from = stampOf(firstAt(firstDateFrom(*since)))
	}
	if until != nil {
		to = stampOf(firstAt(lastDateTo(*until).AddDate(0, 0, 1)))
	}
	// With dates out of the way, what costs is reading the records, which are
	// large. Rather than read them all twice, once to count and once to copy,
	// one pass decides each and notes it in a bitset, and the copy reads only
	// the records kept, 64 at once where a word keeps them all. The result is
	// still exactly as long as it needs to be: a recent window keeps a small
	// share of the archive, and a slice as long as the input is megabytes.
	kept := make([]uint64, (len(records)+63)/64)
	n := 0
	for i := range records {
		t := stampOf(records[i].Time)
		if (since == nil || !t.before(from)) && (until == nil || t.before(to)) {
			kept[i/64] |= 1 << (i % 64)
			n++
		}
	}
	if n == len(records) {
		return records
	}
	out := make([]model.Usage, 0, n)
	for w, word := range kept {
		i := w * 64
		if word == ^uint64(0) {
			out = append(out, records[i:i+64]...)
			continue
		}
		for ; word != 0; word &= word - 1 {
			out = append(out, records[i+bits.TrailingZeros64(word)])
		}
	}
	return out
}

// firstDateFrom is the earliest date whose midnight is not before t. No date
// before t's own can be: its midnight reads an earlier day than t does.
func firstDateFrom(t time.Time) time.Time {
	d := civil(t)
	for midnight(d).Before(t) {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// lastDateTo is the latest date whose midnight is not after t. It can be the
// day after t's own, where that day's midnight was skipped, or the day
// before, where t falls in the first of two passes through midnight and
// time.Date chose the second.
func lastDateTo(t time.Time) time.Time {
	d := civil(t)
	for !midnight(d.AddDate(0, 0, 1)).After(t) {
		d = d.AddDate(0, 0, 1)
	}
	for midnight(d).After(t) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func FilterAgent(records []model.Usage, agent string) []model.Usage {
	if agent == "" {
		return records
	}
	out := make([]model.Usage, 0, len(records))
	for _, r := range records {
		if r.Agent == agent {
			out = append(out, r)
		}
	}
	return out
}

// A calendar date crosses this package's API as the instant time.Date gives
// for its local midnight, which is also how Usage.Date and the --since and
// --until flags stand for one. Inside, a date is held as midnight UTC, where
// stepping and counting days is exact: a local day lasts 23 or 25 hours
// across a DST change, so dividing a local duration by 24 hours merges or
// splits days. Timestamps are compared with the instant a local day really
// begins, which time.Date does not always give: where a DST change skips
// midnight it moves that midnight by the time skipped, back into the evening
// before west of Greenwich (east of it, on to the instant the day does
// begin), and where one repeats midnight it may pick the second of the two.
// That a range of dates is a range of instants relies on the local date never
// running backwards, which has held in every zone since 2010, when
// Newfoundland last turned its clocks back from 00:01 to 23:01 the evening
// before.

// civil is t's local calendar date, held as midnight UTC.
func civil(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// dateOf is the date t stands for: its local date, except that a midnight
// time.Date moved into the evening before stands for its own day, so the
// helpers that return midnights accept their own results. Where a whole day
// was skipped, as Apia skipped 2011-12-30, time.Date gives that day's
// midnight and the one before as the same instant, which stands for the day
// that happened.
func dateOf(t time.Time) time.Time {
	d := civil(t)
	if next := d.AddDate(0, 0, 1); midnight(next).Equal(t) && !midnight(d).Equal(t) {
		return next
	}
	return d
}

// midnight is the instant that stands for date in the API.
func midnight(date time.Time) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
}

// reading is what the local wall clock shows at t, held as a UTC time.
func reading(t time.Time) time.Time {
	l := t.In(time.Local)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
}

// firstAt is the first instant at which the local clock shows wall, a reading
// held as a UTC time, or later; for a midnight that is the instant the day
// begins. time.Date only promises an instant that shows wall in one of the
// offsets around a DST change: when the change skips the reading it may
// answer from before the change with an earlier one, and when the change
// repeats the reading it may pick the second time round.
func firstAt(wall time.Time) time.Time {
	t := time.Date(wall.Year(), wall.Month(), wall.Day(),
		wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), time.Local)
	if reading(t).Before(wall) {
		// The reading was skipped: the clock first passes it when it jumps.
		if _, end := t.ZoneBounds(); !end.IsZero() {
			t = end
		}
	}
	// Within one zone period the clock runs evenly, so the period's first
	// instant at or past wall is where it shows wall exactly, or the period's
	// start if it began past wall. If the clock had already got there before
	// the period began, the answer lies in an earlier period.
	for {
		start, _ := t.ZoneBounds()
		_, offset := t.Zone()
		first := wall.Add(-time.Duration(offset) * time.Second)
		if first.Before(start) {
			first = start
		}
		if start.IsZero() || reading(start.Add(-time.Nanosecond)).Before(wall) {
			return first.In(time.Local)
		}
		t = start.Add(-time.Nanosecond)
	}
}

// stamp is an instant reduced to what ordering needs. The loops that run over
// every record compare stamps rather than time.Time values, whose Before the
// compiler does not inline.
type stamp struct {
	sec  int64
	nsec int32
}

func stampOf(t time.Time) stamp { return stamp{t.Unix(), int32(t.Nanosecond())} }

func (s stamp) before(o stamp) bool {
	return s.sec < o.sec || s.sec == o.sec && s.nsec < o.nsec
}

// daysBetween counts the calendar days from one date to another.
func daysBetween(from, to time.Time) int {
	return int(to.Sub(from) / (24 * time.Hour))
}

// weekStart is the Monday of date's ISO week.
func weekStart(date time.Time) time.Time {
	return date.AddDate(0, 0, -(int(date.Weekday())+6)%7)
}

func monthStart(date time.Time) time.Time {
	return time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Day is the local midnight standing for t's date.
func Day(t time.Time) time.Time {
	return midnight(dateOf(t))
}

func StartOfISOWeek(t time.Time) time.Time {
	return midnight(weekStart(dateOf(t)))
}

func StartOfMonth(t time.Time) time.Time {
	return midnight(monthStart(dateOf(t)))
}

// PeriodBounds is the window a period shows by default: enough history to see
// a trend without turning the table into the full archive.
func PeriodBounds(period Period, today time.Time) (since, until *time.Time) {
	d := dateOf(today)
	end := midnight(d)
	until = &end
	switch period {
	case PeriodToday:
		s := end
		since = &s
	case PeriodDay:
		s := midnight(d.AddDate(0, 0, -13))
		since = &s
	case PeriodWeek:
		s := midnight(weekStart(d).AddDate(0, 0, -7*7))
		since = &s
	case PeriodMonth:
		s := midnight(monthStart(d).AddDate(0, -11, 0))
		since = &s
	default:
		since = nil
	}
	return since, until
}

// PrevBounds is what a summary card compares the period so far with: as much
// of the previous period as had passed of it by the same point, as the
// instants [start, end). For today that is yesterday from midnight up to
// now's clock time; for week, last week from Monday up to the same weekday and
// time; for month, last month from the 1st up to the same day and time, or to
// its end when it has no such day (on Mar 31 all of February counts); for
// year, last year from Jan 1 up to the same date and time, Feb 29 likewise
// counting all of last February. Anything else compares like today. Set
// against the whole of the previous period, steady usage would look like a
// collapse every Monday morning and on every 1st.
//
// The cut steps back by the calendar and keeps the clock time, rather than
// stepping back a fixed duration, so a DST change in between cannot move it.
func PrevBounds(period Period, now time.Time) (start, end time.Time) {
	today := dateOf(now)
	// A midnight that time.Date moved into the evening before still means the
	// start of its day.
	clock := max(reading(now).Sub(today), 0)
	var from, cut time.Time
	switch period {
	case PeriodWeek:
		from = weekStart(today).AddDate(0, 0, -7)
		cut = today.AddDate(0, 0, -7).Add(clock)
	case PeriodMonth:
		from = monthStart(today).AddDate(0, -1, 0)
		cut = sameDay(from, today.Day(), clock)
	case PeriodYear:
		from = time.Date(today.Year()-1, 1, 1, 0, 0, 0, 0, time.UTC)
		cut = sameDay(monthStart(today).AddDate(-1, 0, 0), today.Day(), clock)
	default:
		from = today.AddDate(0, 0, -1)
		cut = from.Add(clock)
	}
	return firstAt(from), firstAt(cut)
}

// sameDay is the given day and clock time in the month starting at first,
// or, when that month is too short to have the day, the end of the month.
func sameDay(first time.Time, day int, clock time.Duration) time.Time {
	d := first.AddDate(0, 0, day-1)
	if d.Month() != first.Month() {
		return first.AddDate(0, 1, 0)
	}
	return d.Add(clock)
}

func BucketKey(period Period, t time.Time) (key, label string) {
	d := civil(t)
	switch period {
	case PeriodWeek:
		start := weekStart(d)
		end := start.AddDate(0, 0, 6)
		y, w := d.ISOWeek()
		key = fmt.Sprintf("%04d-W%02d", y, w)
		label = fmt.Sprintf("%s  %s–%s", key, start.Format("01/02"), end.Format("01/02"))
		return key, label
	case PeriodMonth:
		key = d.Format("2006-01")
		return key, d.Format("2006-01") + "  " + d.Format("Jan")
	case PeriodYear:
		key = d.Format("2006")
		return key, key
	default:
		key = d.Format("2006-01-02")
		return key, d.Format("01-02") + "  " + d.Format("Mon")
	}
}

// GroupTime builds one parent row per time bucket with a child row per agent,
// so a period's total reads at a glance and the split stays one keypress away.
func GroupTime(records []model.Usage, period Period, byModel bool) []model.Bucket {
	type child struct{ agent, mdl string }
	parents := map[string]int{}
	var out []model.Bucket
	kids := map[string]map[child]int{}

	for _, r := range records {
		key, label := BucketKey(period, r.Time)
		pi, ok := parents[key]
		if !ok {
			pi = len(out)
			parents[key] = pi
			out = append(out, model.Bucket{Key: key, Label: label})
			kids[key] = map[child]int{}
		}
		out[pi].Totals.AddUsage(r)

		ck := child{agent: r.Agent}
		if byModel {
			ck.mdl = r.Model
		}
		ci, ok := kids[key][ck]
		if !ok {
			ci = len(out[pi].Children)
			kids[key][ck] = ci
			childKey := key + "/" + ck.agent
			if ck.mdl != "" {
				childKey += "/" + ck.mdl
			}
			out[pi].Children = append(out[pi].Children, model.Bucket{
				Key:   childKey,
				Label: ck.agent,
				Agent: ck.agent,
				Model: ck.mdl,
				Depth: 1,
			})
		}
		out[pi].Children[ci].Totals.AddUsage(r)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Key > out[j].Key })
	for i := range out {
		sortBucketsByTotal(out[i].Children)
	}
	return out
}

// GroupAgent is the flat agent breakdown used for a single day.
func GroupAgent(records []model.Usage, byModel bool) []model.Bucket {
	type ident struct{ agent, mdl string }
	seen := map[ident]int{}
	var out []model.Bucket
	for _, r := range records {
		id := ident{agent: r.Agent}
		if byModel {
			id.mdl = r.Model
		}
		i, ok := seen[id]
		if !ok {
			i = len(out)
			seen[id] = i
			key := id.agent
			if id.mdl != "" {
				key += "/" + id.mdl
			}
			out = append(out, model.Bucket{
				Key:   key,
				Label: id.agent,
				Agent: id.agent,
				Model: id.mdl,
			})
		}
		out[i].Totals.AddUsage(r)
	}
	sortBucketsByTotal(out)
	return out
}

// GroupProject ranks working directories, each expanding into its agents.
func GroupProject(records []model.Usage) []model.Bucket {
	parents := map[string]int{}
	kids := map[string]map[string]int{}
	var out []model.Bucket
	for _, r := range records {
		key := r.Project
		if key == "" {
			key = "(unknown)"
		}
		pi, ok := parents[key]
		if !ok {
			pi = len(out)
			parents[key] = pi
			out = append(out, model.Bucket{Key: key, Label: model.ShortProject(key), Project: key})
			kids[key] = map[string]int{}
		}
		out[pi].Totals.AddUsage(r)
		ci, ok := kids[key][r.Agent]
		if !ok {
			ci = len(out[pi].Children)
			kids[key][r.Agent] = ci
			out[pi].Children = append(out[pi].Children, model.Bucket{
				Key:     key + "/" + r.Agent,
				Label:   r.Agent,
				Agent:   r.Agent,
				Project: key,
				Depth:   1,
			})
		}
		out[pi].Children[ci].Totals.AddUsage(r)
	}
	sortBucketsByTotal(out)
	for i := range out {
		sortBucketsByTotal(out[i].Children)
	}
	return out
}

// GroupModel ranks models, each expanding into the agents that invoked it.
func GroupModel(records []model.Usage) []model.Bucket {
	parents := map[string]int{}
	kids := map[string]map[string]int{}
	var out []model.Bucket
	for _, r := range records {
		key := r.Model
		if key == "" {
			key = "(unknown)"
		}
		pi, ok := parents[key]
		if !ok {
			pi = len(out)
			parents[key] = pi
			out = append(out, model.Bucket{Key: key, Label: model.ShortModel(key), Model: key})
			kids[key] = map[string]int{}
		}
		out[pi].Totals.AddUsage(r)
		ci, ok := kids[key][r.Agent]
		if !ok {
			ci = len(out[pi].Children)
			kids[key][r.Agent] = ci
			out[pi].Children = append(out[pi].Children, model.Bucket{
				Key:   key + "/" + r.Agent,
				Label: r.Agent,
				Agent: r.Agent,
				Model: key,
				Depth: 1,
			})
		}
		out[pi].Children[ci].Totals.AddUsage(r)
	}
	sortBucketsByTotal(out)
	for i := range out {
		sortBucketsByTotal(out[i].Children)
	}
	return out
}

func sortBucketsByTotal(bs []model.Bucket) {
	sort.SliceStable(bs, func(i, j int) bool {
		if bs[i].Totals.Total() != bs[j].Totals.Total() {
			return bs[i].Totals.Total() > bs[j].Totals.Total()
		}
		return bs[i].Label < bs[j].Label
	})
}

// Flatten expands parent rows into a single list, including the children of
// rows the caller marked open.
func Flatten(bs []model.Bucket, open map[string]bool) []model.Bucket {
	out := make([]model.Bucket, 0, len(bs))
	for _, b := range bs {
		out = append(out, b)
		if len(b.Children) > 0 && open[b.Key] {
			out = append(out, b.Children...)
		}
	}
	return out
}

// Heat is a 7x24 grid of weekday by hour-of-day volume. Min is the smallest
// non-zero cell; together with Max it gives the shading a range to normalise
// against, which matters because usage is heavily skewed towards a few hours.
type Heat struct {
	Cells [7][24]int64
	Min   int64
	Max   int64
	Total int64
}

func Heatmap(records []model.Usage) Heat {
	var h Heat
	for _, r := range records {
		t := r.Time.In(time.Local)
		wd := int(t.Weekday()) // Sunday=0
		wd = (wd + 6) % 7      // Monday=0
		h.Cells[wd][t.Hour()] += r.Total()
		h.Total += r.Total()
	}
	for d := range h.Cells {
		for _, v := range h.Cells[d] {
			if v <= 0 {
				continue
			}
			if v > h.Max {
				h.Max = v
			}
			if h.Min == 0 || v < h.Min {
				h.Min = v
			}
		}
	}
	return h
}

// Series is a per-day total covering an inclusive date range, with zero-filled
// gaps so a sparkline keeps its horizontal scale honest.
func Series(records []model.Usage, since, until time.Time) []int64 {
	from := dateOf(since)
	days := daysBetween(from, dateOf(until)) + 1
	if days <= 0 || days > 4000 {
		return nil
	}
	out := make([]int64, days)
	for _, r := range records {
		if i := daysBetween(from, civil(r.Time)); i >= 0 && i < days {
			out[i] += r.Total()
		}
	}
	return out
}

// Snap is the headline figure for one summary card.
type Snap struct {
	Totals model.Totals
	Prev   model.Totals
	Trend  []int64
}

// trendDays is the longest trend a card draws, the year card's; every other
// card's trend is its most recent days.
const trendDays = 90

// Snapshot computes the today/week/month/year cards at the instant now. A
// card's Totals cover its calendar span through today, so they agree with the
// table for the same span; Prev is the stretch of the previous period that
// PrevBounds describes; Trend is the daily series the card draws.
//
// The TUI recomputes the cards on every keystroke, so this is one pass over
// the records that compares timestamps with instants worked out beforehand
// and allocates nothing per record.
func Snapshot(records []model.Usage, now time.Time) map[string]Snap {
	today := dateOf(now)
	trendStart := today.AddDate(0, 0, -(trendDays - 1))
	cards := [...]struct {
		key               string
		since, trendSince time.Time // dates; the span and its trend run through today
		from              stamp     // when the span begins
		prevFrom, prevTo  stamp
		totals, prev      model.Totals
	}{
		{key: "today", since: today, trendSince: today.AddDate(0, 0, -13)},
		{key: "week", since: weekStart(today), trendSince: weekStart(today).AddDate(0, 0, -7)},
		{key: "month", since: monthStart(today), trendSince: monthStart(today)},
		{key: "year", since: time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC), trendSince: trendStart},
	}
	// A record outside [earliest, end) counts towards all and nothing else.
	end := stampOf(firstAt(today.AddDate(0, 0, 1))) // when today, and so every span, ends
	trendFrom := stampOf(firstAt(trendStart))
	earliest := trendFrom
	for i := range cards {
		c := &cards[i]
		prevFrom, prevTo := PrevBounds(Period(c.key), now)
		c.from, c.prevFrom, c.prevTo = stampOf(firstAt(c.since)), stampOf(prevFrom), stampOf(prevTo)
		if c.from.before(earliest) {
			earliest = c.from
		}
		if c.prevFrom.before(earliest) {
			earliest = c.prevFrom
		}
	}

	var all model.Totals
	var daily [trendDays]int64 // oldest first, today last
	for i := range records {
		r := &records[i]
		all.AddUsage(*r)
		t := stampOf(r.Time)
		if t.before(earliest) || !t.before(end) {
			continue
		}
		for j := range cards {
			c := &cards[j]
			if !t.before(c.from) { // and before end, where every span stops
				c.totals.AddUsage(*r)
			}
			if !t.before(c.prevFrom) && t.before(c.prevTo) {
				c.prev.AddUsage(*r)
			}
		}
		// Only the trends need a record's local date, and only within the
		// days they cover.
		if !t.before(trendFrom) {
			if d := daysBetween(trendStart, civil(r.Time)); d >= 0 && d < trendDays {
				daily[d] += r.Total()
			}
		}
	}

	out := make(map[string]Snap, len(cards)+1)
	for _, c := range cards {
		days := daysBetween(c.trendSince, today) + 1
		out[c.key] = Snap{Totals: c.totals, Prev: c.prev, Trend: slices.Clone(daily[trendDays-days:])}
	}
	out["all"] = Snap{Totals: all}
	return out
}

func Sum(buckets []model.Bucket) model.Totals {
	var t model.Totals
	for _, b := range buckets {
		if b.Depth > 0 {
			continue // children are already counted in their parent
		}
		t.Merge(b.Totals)
	}
	return t
}
