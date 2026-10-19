package aggregate

import (
	"fmt"
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

func Filter(records []model.Usage, since, until *time.Time) []model.Usage {
	if since == nil && until == nil {
		return records
	}
	out := make([]model.Usage, 0, len(records))
	for _, r := range records {
		d := r.Date()
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

func Day(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func StartOfISOWeek(t time.Time) time.Time {
	d := Day(t)
	wd := int(d.Weekday())
	if wd == 0 {
		wd = 7
	}
	return d.AddDate(0, 0, -(wd - 1))
}

func StartOfMonth(t time.Time) time.Time {
	d := Day(t)
	return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, d.Location())
}

// PeriodBounds is the window a period shows by default: enough history to see
// a trend without turning the table into the full archive.
func PeriodBounds(period Period, today time.Time) (since, until *time.Time) {
	today = Day(today)
	end := today
	until = &end
	switch period {
	case PeriodToday:
		s := today
		since = &s
	case PeriodDay:
		s := today.AddDate(0, 0, -13)
		since = &s
	case PeriodWeek:
		s := StartOfISOWeek(today).AddDate(0, 0, -7*7)
		since = &s
	case PeriodMonth:
		s := StartOfMonth(today).AddDate(0, -11, 0)
		since = &s
	default:
		since = nil
	}
	return since, until
}

// PrevBounds is the window immediately before the current one, used for the
// period-over-period comparison on the summary cards.
func PrevBounds(period Period, today time.Time) (since, until time.Time) {
	today = Day(today)
	switch period {
	case PeriodToday:
		d := today.AddDate(0, 0, -1)
		return d, d
	case PeriodWeek:
		s := StartOfISOWeek(today).AddDate(0, 0, -7)
		return s, s.AddDate(0, 0, 6)
	case PeriodMonth:
		s := StartOfMonth(today).AddDate(0, -1, 0)
		return s, s.AddDate(0, 1, -1)
	case PeriodYear:
		s := time.Date(today.Year()-1, 1, 1, 0, 0, 0, 0, today.Location())
		return s, time.Date(today.Year()-1, 12, 31, 0, 0, 0, 0, today.Location())
	default:
		d := today.AddDate(0, 0, -1)
		return d, d
	}
}

func BucketKey(period Period, t time.Time) (key, label string) {
	d := Day(t)
	switch period {
	case PeriodWeek:
		start := StartOfISOWeek(d)
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
	since, until = Day(since), Day(until)
	if until.Before(since) {
		return nil
	}
	days := int(until.Sub(since).Hours()/24) + 1
	if days <= 0 || days > 4000 {
		return nil
	}
	out := make([]int64, days)
	for _, r := range records {
		d := r.Date()
		if d.Before(since) || d.After(until) {
			continue
		}
		i := int(d.Sub(since).Hours() / 24)
		if i >= 0 && i < days {
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

// Snapshot computes the today/week/month/year cards, each with the previous
// period's total and a daily trend to draw.
func Snapshot(records []model.Usage, today time.Time) map[string]Snap {
	today = Day(today)
	weekStart := StartOfISOWeek(today)
	monthStart := StartOfMonth(today)
	yearStart := time.Date(today.Year(), 1, 1, 0, 0, 0, 0, today.Location())

	windows := []struct {
		key          string
		since, until time.Time
		trendFrom    time.Time
	}{
		{"today", today, today, today.AddDate(0, 0, -13)},
		{"week", weekStart, today, weekStart.AddDate(0, 0, -7)},
		{"month", monthStart, today, monthStart},
		{"year", yearStart, today, today.AddDate(0, 0, -89)},
	}
	out := make(map[string]Snap, len(windows)+1)
	for _, w := range windows {
		var cur model.Totals
		for _, r := range Filter(records, ptr(w.since), ptr(w.until)) {
			cur.AddUsage(r)
		}
		ps, pu := PrevBounds(Period(w.key), today)
		var prev model.Totals
		for _, r := range Filter(records, &ps, &pu) {
			prev.AddUsage(r)
		}
		out[w.key] = Snap{Totals: cur, Prev: prev, Trend: Series(records, w.trendFrom, today)}
	}
	var all model.Totals
	for _, r := range records {
		all.AddUsage(r)
	}
	out["all"] = Snap{Totals: all}
	return out
}

func ptr(t time.Time) *time.Time { return &t }

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
