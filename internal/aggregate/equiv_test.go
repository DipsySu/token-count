package aggregate

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"token-count/internal/model"
)

// testZones change their clocks in every way that trips date arithmetic: by
// an hour at 02:00 or 01:00 (New York, London), by half an hour (Lord Howe),
// at midnight skipping it (Santiago, Asunción, Havana) or repeating it
// (Havana, Amman until 2022), at midnight harmlessly (Beirut, Cairo), at odd
// offsets (Chatham, St John's), by a whole day (Apia skipped 2011-12-30), and
// not at all.
var testZones = []string{
	"UTC", "Asia/Taipei", "America/New_York", "Europe/London",
	"Australia/Lord_Howe", "America/Santiago", "America/Asuncion",
	"America/Havana", "Asia/Amman", "Asia/Beirut", "Africa/Cairo",
	"Pacific/Chatham", "America/St_Johns", "Pacific/Apia",
}

// The tests cover 2011 on. Before then Newfoundland turned its clocks back
// from 00:01 to 23:01 the evening before, the local date ran backwards, and
// no comparison of instants can reproduce a comparison of dates there.
var (
	testFrom = time.Date(2011, 1, 1, 0, 0, 0, 0, time.UTC)
	testTo   = time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
)

// transitions lists the instants at which the local clock changed between
// from and to.
//
// Zone data may list only the earlier changes and give a rule for the rest,
// as the slim data that time/tzdata embeds does, and that is what the tests
// run on where the system has no zoneinfo. For the last period of each year
// that it works out from such a rule, Go gives the end of the year as its
// end, whether or not the clock changes then, and in a leap year a day
// early, which can be t itself, so ZoneBounds would never move past it. No
// change falls in that last day: the walk resumes at the new year.
func transitions(from, to time.Time) []time.Time {
	var out []time.Time
	for t := from.In(time.Local); ; {
		_, end := t.ZoneBounds()
		if end.IsZero() {
			return out
		}
		if !end.After(t) {
			end = time.Date(t.UTC().Year()+1, 1, 1, 0, 0, 0, 0, time.UTC).In(time.Local)
		}
		if !end.Before(to) {
			return out
		}
		_, before := end.Add(-time.Nanosecond).Zone()
		if _, after := end.Zone(); after != before {
			out = append(out, end)
		}
		t = end
	}
}

// nearby are the offsets at which the tests probe around an instant of
// interest, from just either side of it to beyond any DST shift.
var nearby = []time.Duration{
	0, time.Nanosecond, -time.Nanosecond, 30 * time.Minute, -30 * time.Minute,
	time.Hour, -time.Hour, 2 * time.Hour, -2 * time.Hour,
}

func usageAt(ts time.Time, in int64) model.Usage {
	u, _ := model.Make(model.MakeOpts{Agent: "claude", Time: ts, Input: in, Output: 1})
	return u
}

// firstAt of a date is where that local day begins: the first instant whose
// local date is that date, or a later one where the day never happened.
func TestFirstAtFindsWhereEachDayBegins(t *testing.T) {
	for _, name := range testZones {
		t.Run(name, func(t *testing.T) {
			setLocal(t, name)
			for d := testFrom; d.Before(testTo); d = d.AddDate(0, 0, 1) {
				s := firstAt(d)
				if civil(s).Before(d) || !civil(s.Add(-time.Nanosecond)).Before(d) {
					t.Fatalf("%s starts at %s", d.Format(time.DateOnly), s)
				}
			}
		})
	}
}

// Every day that happened reads back as itself from the midnight that stands
// for it in the API, from the instant it really begins and from its last
// instant, so the helpers that return midnights accept their own results.
func TestDateOfReadsEveryDayBack(t *testing.T) {
	for _, name := range testZones {
		t.Run(name, func(t *testing.T) {
			setLocal(t, name)
			for d := testFrom; d.Before(testTo); d = d.AddDate(0, 0, 1) {
				start := firstAt(d)
				if !civil(start).Equal(d) {
					continue // the day never happened
				}
				last := firstAt(d.AddDate(0, 0, 1)).Add(-time.Nanosecond)
				for _, at := range []time.Time{midnight(d), start, last} {
					if got := dateOf(at); !got.Equal(d) {
						t.Fatalf("%s reads as %s, not %s", at, got.Format(time.DateOnly), d.Format(time.DateOnly))
					}
				}
			}
		})
	}
}

// Around every clock change, firstAt agrees with a brute-force search for the
// first instant whose reading is at or past the one asked for. Readings and
// changes all fall on whole minutes, so stepping a minute at a time cannot
// miss it, and four hours either side of the change is beyond any reading
// probed.
func TestFirstAtAroundClockChanges(t *testing.T) {
	for _, name := range testZones {
		t.Run(name, func(t *testing.T) {
			setLocal(t, name)
			for _, change := range transitions(testFrom, testTo) {
				for m := -150; m <= 150; m += 15 {
					wall := reading(change).Add(time.Duration(m) * time.Minute)
					want := time.Time{}
					for u := change.Add(-4 * time.Hour); u.Before(change.Add(4 * time.Hour)); u = u.Add(time.Minute) {
						if !reading(u).Before(wall) {
							want = u
							break
						}
					}
					if got := firstAt(wall); !got.Equal(want) {
						t.Fatalf("clock first reads %s at %s, not %s", wall.Format(time.DateTime), want, got)
					}
				}
			}
		})
	}
}

// filterByDate is Filter as it was before it compared instants: it converts
// every record to the midnight of its local date, as Usage.Date does, and
// compares that with the bounds. Filter must agree with it everywhere.
func filterByDate(records []model.Usage, since, until *time.Time) []model.Usage {
	if since == nil && until == nil {
		return records
	}
	out := make([]model.Usage, 0, len(records))
	for _, r := range records {
		y, m, dd := r.Time.In(time.Local).Date()
		d := time.Date(y, m, dd, 0, 0, 0, 0, time.Local)
		if since != nil && d.Before(*since) {
			continue
		}
		if until != nil && d.After(*until) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Filter compares timestamps with the instants its bounds' dates begin and
// end, which must keep exactly what comparing each record's date did:
// records just either side of local midnights, of the instants days really
// begin and of clock changes, against bounds that are midnights (what every
// caller passes), the instants days begin, arbitrary instants, or nil.
func TestFilterMatchesDateComparison(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, name := range testZones {
		t.Run(name, func(t *testing.T) {
			setLocal(t, name)
			changes := transitions(testFrom, testTo)
			var days []time.Time
			for _, c := range changes {
				d := civil(c.Add(-time.Nanosecond))
				days = append(days, d.AddDate(0, 0, -1), d, d.AddDate(0, 0, 1))
			}
			for range 25 {
				days = append(days, testFrom.AddDate(0, 0, rng.IntN(daysBetween(testFrom, testTo))))
			}
			var recs []model.Usage
			for _, d := range days {
				ats := []time.Time{midnight(d)}
				if start := firstAt(d); !start.Equal(ats[0]) {
					ats = append(ats, start)
				}
				for _, at := range ats {
					for _, off := range nearby {
						recs = append(recs, usageAt(at.Add(off), 1))
					}
				}
			}
			for _, c := range changes {
				for _, off := range nearby {
					recs = append(recs, usageAt(c.Add(off), 1))
				}
			}
			bound := func() *time.Time {
				d := days[rng.IntN(len(days))]
				var b time.Time
				switch rng.IntN(6) {
				case 0:
					return nil
				case 1:
					b = midnight(d)
				case 2:
					b = midnight(d.AddDate(0, 0, rng.IntN(60)))
				case 3:
					b = firstAt(d)
				case 4:
					if len(changes) == 0 {
						return nil
					}
					b = changes[rng.IntN(len(changes))].Add(nearby[rng.IntN(len(nearby))])
				default:
					b = midnight(d).Add(time.Duration(rng.Int64N(int64(48*time.Hour))) - 24*time.Hour)
				}
				return &b
			}
			for range 300 {
				since, until := bound(), bound()
				got, want := Filter(recs, since, until), filterByDate(recs, since, until)
				if !slices.Equal(got, want) {
					t.Fatalf("since %v until %v: kept %d records, comparing dates kept %d",
						since, until, len(got), len(want))
				}
				// All the records come back as the slice passed in, fewer in
				// one of their own exact size.
				if len(got) == len(recs) && &got[0] != &recs[0] || len(got) < len(recs) && cap(got) != len(got) {
					t.Fatalf("since %v until %v: kept %d of %d records in a slice of capacity %d",
						since, until, len(got), len(recs), cap(got))
				}
			}
		})
	}
}

// clockFirstShows is firstAt found by brute force: the earliest instant at
// which the local clock shows wall, a reading held as a UTC time, or later.
// Within a zone period the clock runs evenly, so that instant is either where
// some period's clock shows wall exactly or where a period begins with the
// clock already past it. Sampling the offset hourly from 15 hours before wall
// to 15 hours after, further than any offset reaches, meets every period in
// reach, none being shorter than an hour, and halving the hour in which the
// offset changes finds the second the change happened.
func clockFirstShows(wall time.Time) time.Time {
	offsetAt := func(u time.Time) int {
		_, off := u.In(time.Local).Zone()
		return off
	}
	var candidates []time.Time
	u := wall.Truncate(time.Second).Add(-15 * time.Hour)
	off := offsetAt(u)
	for end := wall.Add(15 * time.Hour); u.Before(end); u = u.Add(time.Hour) {
		candidates = append(candidates, wall.Add(-time.Duration(off)*time.Second))
		next := offsetAt(u.Add(time.Hour))
		if next != off {
			a, b := u, u.Add(time.Hour) // the offset changes after a, by b
			for b.Sub(a) > time.Second {
				if m := a.Add(b.Sub(a) / 2).Truncate(time.Second); offsetAt(m) == off {
					a = m
				} else {
					b = m
				}
			}
			candidates = append(candidates, b)
		}
		off = next
	}
	var first time.Time
	for _, c := range candidates {
		if !reading(c).Before(wall) && (first.IsZero() || c.Before(first)) {
			first = c
		}
	}
	return first.In(time.Local)
}

// prevByCalendar is PrevBounds worked out from its description, without the
// package's helpers: from the previous period's first day at 00:00 to the
// same day and clock time as now has reached in the current period, or to
// the end of a previous month too short to have that day, each taken as the
// instant the clock first shows it.
func prevByCalendar(period Period, now time.Time) (start, end time.Time) {
	l := now.In(time.Local)
	y, m, d := l.Date()
	clock := time.Duration(l.Hour())*time.Hour + time.Duration(l.Minute())*time.Minute +
		time.Duration(l.Second())*time.Second + time.Duration(l.Nanosecond())
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	daysIn := func(y int, m time.Month) int { return day(y, m+1, 0).Day() }
	var from, cut time.Time
	switch period {
	case PeriodWeek:
		monday := d - (int(l.Weekday())+6)%7
		from, cut = day(y, m, monday-7), day(y, m, d-7).Add(clock)
	case PeriodMonth:
		from, cut = day(y, m-1, 1), day(y, m-1, d).Add(clock)
		if d > daysIn(y, m-1) {
			cut = day(y, m, 1)
		}
	case PeriodYear:
		from, cut = day(y-1, 1, 1), day(y-1, m, d).Add(clock)
		if d > daysIn(y-1, m) {
			cut = day(y-1, m+1, 1)
		}
	default:
		from = day(y, m, d-1)
		cut = from.Add(clock)
	}
	return clockFirstShows(from), clockFirstShows(cut)
}

// PrevBounds agrees with the calendar at random instants, around the ends of
// months and years, where months are too short for the day, and wherever the
// cut lands near a clock change: a day, a week, a month and a year after one,
// at clock times either side of it and within any hour it skipped or
// repeated.
func TestPrevBoundsMatchesCalendar(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for _, name := range testZones {
		t.Run(name, func(t *testing.T) {
			setLocal(t, name)
			local := func(w time.Time) time.Time {
				return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), time.Local)
			}
			var nows []time.Time
			for range 100 {
				nows = append(nows, testFrom.Add(time.Duration(rng.Int64N(int64(testTo.Sub(testFrom))))))
			}
			for m := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC); m.Year() < 2029; m = m.AddDate(0, 1, 0) {
				last := m.AddDate(0, 1, -1)
				for _, w := range []time.Time{
					m, m.Add(10 * time.Hour), last.Add(10 * time.Hour), last.Add(24*time.Hour - time.Nanosecond),
					m.AddDate(0, 0, 28).Add(10 * time.Hour), m.AddDate(0, 0, 29).Add(10 * time.Hour),
				} {
					nows = append(nows, local(w))
				}
			}
			for _, change := range transitions(testFrom, testTo) {
				for _, off := range []time.Duration{-90 * time.Minute, -30 * time.Minute, -time.Nanosecond, 0, 30 * time.Minute, 90 * time.Minute} {
					w := reading(change).Add(off)
					nows = append(nows, local(w.AddDate(0, 0, 1)), local(w.AddDate(0, 0, 7)),
						local(w.AddDate(0, 1, 0)), local(w.AddDate(1, 0, 0)))
				}
			}
			for _, now := range nows {
				if !dateOf(now).Equal(civil(now)) {
					continue // a midnight moved into the evening before; the calendar reads it as that evening
				}
				for _, p := range []Period{PeriodToday, PeriodWeek, PeriodMonth, PeriodYear} {
					gotStart, gotEnd := PrevBounds(p, now)
					wantStart, wantEnd := prevByCalendar(p, now)
					if !gotStart.Equal(wantStart) || !gotEnd.Equal(wantEnd) {
						t.Fatalf("%s at %s: [%s, %s), want [%s, %s)", p, now, gotStart, gotEnd, wantStart, wantEnd)
					}
				}
			}
		})
	}
}

// snapshotByDate is Snapshot written the obvious way: a pass over the records
// for each figure, each record dated by converting it to local time, and the
// stretch of the previous period worked out by prevByCalendar.
func snapshotByDate(records []model.Usage, now time.Time) map[string]Snap {
	dateOn := func(t time.Time) time.Time {
		y, m, d := t.In(time.Local).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	dates := make([]time.Time, len(records))
	for i, r := range records {
		dates[i] = dateOn(r.Time)
	}
	today := dateOn(now)
	monday := today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	out := map[string]Snap{}
	for _, c := range []struct {
		key               string
		since, trendSince time.Time
	}{
		{"today", today, today.AddDate(0, 0, -13)},
		{"week", monday, monday.AddDate(0, 0, -7)},
		{"month", first, first},
		{"year", time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC), today.AddDate(0, 0, -89)},
	} {
		var s Snap
		prevFrom, prevTo := prevByCalendar(Period(c.key), now)
		for i, r := range records {
			if !dates[i].Before(c.since) && !dates[i].After(today) {
				s.Totals.AddUsage(r)
			}
			if !r.Time.Before(prevFrom) && r.Time.Before(prevTo) {
				s.Prev.AddUsage(r)
			}
		}
		for d := c.trendSince; !d.After(today); d = d.AddDate(0, 0, 1) {
			var sum int64
			for i, r := range records {
				if dates[i].Equal(d) {
					sum += r.Total()
				}
			}
			s.Trend = append(s.Trend, sum)
		}
		out[c.key] = s
	}
	var all model.Totals
	for _, r := range records {
		all.AddUsage(r)
	}
	out["all"] = Snap{Totals: all}
	return out
}

// The single pass must agree with the obvious computation at all hours,
// around clock changes, and with records sitting on every boundary.
func TestSnapshotMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for _, name := range testZones {
		t.Run(name, func(t *testing.T) {
			setLocal(t, name)
			var nows []time.Time
			for _, s := range []string{
				"2026-10-19 10:00:00", // a Monday
				"2026-10-25 23:59:59.999999999",
				"2026-03-01 00:00:00",
				"2026-03-31 13:30:00",
				"2028-02-29 12:00:00",
				"2027-01-01 00:30:00",
				"2026-12-31 23:00:00",
			} {
				now, _ := time.ParseInLocation("2006-01-02 15:04:05.999999999", s, time.Local)
				nows = append(nows, now)
			}
			for _, c := range transitions(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), testTo) {
				// At the change, the next morning, and a week on.
				for _, off := range []time.Duration{0, -time.Nanosecond, 90 * time.Minute, 25 * time.Hour, 7*24*time.Hour + 30*time.Minute} {
					nows = append(nows, c.Add(off))
				}
			}
			for range 8 {
				nows = append(nows, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rng.Int64N(int64(5*365*24*time.Hour)))))
			}
			for _, now := range nows {
				if !dateOf(now).Equal(civil(now)) {
					continue // a midnight moved into the evening before; the reference reads it as that evening
				}
				recs := snapshotRecords(rng, now)
				if got, want := Snapshot(recs, now), snapshotByDate(recs, now); !reflect.DeepEqual(got, want) {
					for key := range want {
						if !reflect.DeepEqual(got[key], want[key]) {
							t.Fatalf("at %s, %s card:\n got %+v\nwant %+v", now, key, got[key], want[key])
						}
					}
					t.Fatalf("at %s: got %+v", now, got)
				}
			}
		})
	}
}

// snapshotRecords scatters records over the two years before now and the
// day after, and puts more just either side of every instant at which a
// Snapshot figure starts or stops, and of every clock change in range.
func snapshotRecords(rng *rand.Rand, now time.Time) []model.Usage {
	var recs []model.Usage
	from := now.AddDate(-2, 0, -5)
	span := int64(now.AddDate(0, 0, 2).Sub(from))
	for range 600 {
		recs = append(recs, usageAt(from.Add(time.Duration(rng.Int64N(span))), 1+rng.Int64N(1000)))
	}
	today := civil(now)
	var edges []time.Time
	for _, d := range []time.Time{
		today, today.AddDate(0, 0, 1), today.AddDate(0, 0, -1), today.AddDate(0, 0, -7),
		today.AddDate(0, 0, -13), today.AddDate(0, 0, -89), weekStart(today),
		weekStart(today).AddDate(0, 0, -7), weekStart(today).AddDate(0, 0, -14),
		monthStart(today), monthStart(today).AddDate(0, -1, 0),
		time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(today.Year()-1, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		edges = append(edges, midnight(d), firstAt(d))
	}
	for _, p := range []Period{PeriodToday, PeriodWeek, PeriodMonth, PeriodYear} {
		start, end := PrevBounds(p, now)
		refStart, refEnd := prevByCalendar(p, now)
		edges = append(edges, start, end, refStart, refEnd)
	}
	edges = append(edges, transitions(from, now.AddDate(0, 0, 2))...)
	for _, e := range edges {
		for _, off := range nearby {
			recs = append(recs, usageAt(e.Add(off), 1+rng.Int64N(1000)))
		}
	}
	rng.Shuffle(len(recs), func(i, j int) { recs[i], recs[j] = recs[j], recs[i] })
	return recs
}

// Snapshot and PrevBounds read a midnight that time.Date moved into the
// evening before as the start of its own day, as the other helpers do.
func TestSnapshotTakesAMovedMidnightAsStartOfDay(t *testing.T) {
	setLocal(t, "America/Santiago") // 2026-09-05 24:00 became 09-06 01:00
	sep6 := time.Date(2026, 9, 6, 0, 0, 0, 0, time.Local)
	recs := []model.Usage{rec("2026-09-05", "claude", 10), rec("2026-09-06", "claude", 20)}
	snap := Snapshot(recs, sep6)
	if got := snap["today"].Totals.Input; got != 20 {
		t.Fatalf("today %d, want the 09-06 record", got)
	}
	if start, end := PrevBounds(PeriodToday, sep6); !start.Equal(end) {
		t.Fatalf("at the start of the day nothing of yesterday has passed: [%s, %s)", start, end)
	}
	if got := fmt.Sprint(snap["today"].Trend[12:]); got != "[11 21]" {
		t.Fatalf("trend ends %s", got)
	}
}
