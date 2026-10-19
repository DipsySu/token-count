package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"token-count/internal/aggregate"
	"token-count/internal/cache"
	"token-count/internal/model"
	"token-count/internal/source"
)

func ShouldTUI(plain, asJSON bool) bool {
	if plain || asJSON {
		return false
	}
	if os.Getenv("NO_TUI") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

type Report struct {
	Period  string               `json:"period"`
	View    string               `json:"view"`
	Heat    *ReportHeat          `json:"heat,omitempty"`
	Date    string               `json:"date,omitempty"`
	Since   *string              `json:"since,omitempty"`
	Until   *string              `json:"until,omitempty"`
	Rows    []ReportRow          `json:"rows"`
	Snap    map[string]ReportSum `json:"snapshot,omitempty"`
	Records int                  `json:"records"`
	Calls   int64                `json:"calls"`
	Deduped ReportDedup          `json:"deduped"`
}

// ReportHeat is the hour-of-week grid, emitted only for the heat view. Rows
// run Monday..Sunday, columns hour 0..23.
type ReportHeat struct {
	Cells [7][24]int64 `json:"cells"`
	Min   int64        `json:"min"`
	Max   int64        `json:"max"`
	Total int64        `json:"total"`
}

type ReportDedup struct {
	Dropped       int   `json:"dropped_records"`
	DroppedTokens int64 `json:"dropped_tokens"`
}

type ReportSum struct {
	Input      int64   `json:"input"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	Output     int64   `json:"output"`
	Reasoning  int64   `json:"reasoning"`
	Total      int64   `json:"total"`
	Calls      int64   `json:"calls"`
	CacheHit   float64 `json:"cache_hit"`
}

type ReportRow struct {
	Key        string      `json:"key"`
	Label      string      `json:"label"`
	Agent      string      `json:"agent,omitempty"`
	Model      string      `json:"model,omitempty"`
	Project    string      `json:"project,omitempty"`
	Input      int64       `json:"input"`
	CacheRead  int64       `json:"cache_read"`
	CacheWrite int64       `json:"cache_write"`
	Output     int64       `json:"output"`
	Reasoning  int64       `json:"reasoning"`
	Total      int64       `json:"total"`
	Calls      int64       `json:"calls"`
	Children   []ReportRow `json:"children,omitempty"`
}

// Load parses every session file for the requested agents and returns the
// deduplicated records alongside what deduplication removed.
func Load(opts Options) ([]model.Usage, source.Stats, error) {
	c := cache.New("", !opts.NoCache)
	recs, stats := source.CollectStats(opts.Agents, c, nil)
	err := c.Save()
	return recs, stats, err
}

// BuildBuckets applies the same scoping and grouping the TUI uses, so the two
// front ends can never disagree about what a period means.
func BuildBuckets(records []model.Usage, opts Options) ([]model.Bucket, map[string]aggregate.Snap) {
	today := aggregate.Day(time.Now())
	base := records
	if opts.Since != nil || opts.Until != nil {
		base = aggregate.Filter(base, opts.Since, opts.Until)
	}
	snap := aggregate.Snapshot(base, today)

	recs := base
	view := opts.View
	if view == "" {
		view = aggregate.ViewTime
	}
	if opts.Since == nil && opts.Until == nil && view != aggregate.ViewHeat {
		if since, until := aggregate.PeriodBounds(opts.Period, today); since != nil || until != nil {
			recs = aggregate.Filter(recs, since, until)
		}
	}

	var buckets []model.Bucket
	switch view {
	case aggregate.ViewProject:
		buckets = aggregate.GroupProject(recs)
	case aggregate.ViewModel:
		buckets = aggregate.GroupModel(recs)
	default:
		if opts.Period == aggregate.PeriodToday {
			buckets = aggregate.GroupAgent(recs, opts.ByModel)
		} else {
			p := opts.Period
			if p == aggregate.PeriodAll {
				p = aggregate.PeriodDay
			}
			buckets = aggregate.GroupTime(recs, p, opts.ByModel)
		}
	}
	return buckets, snap
}

func toReportSum(t model.Totals) ReportSum {
	return ReportSum{
		Input:      t.Input,
		CacheRead:  t.CacheRead,
		CacheWrite: t.CacheWrite,
		Output:     t.Output,
		Reasoning:  t.Reasoning,
		Total:      t.Total(),
		Calls:      t.Calls,
		CacheHit:   t.CacheHit(),
	}
}

func toReportRow(b model.Bucket) ReportRow {
	r := ReportRow{
		Key:        b.Key,
		Label:      b.Label,
		Agent:      b.Agent,
		Model:      b.Model,
		Project:    b.Project,
		Input:      b.Totals.Input,
		CacheRead:  b.Totals.CacheRead,
		CacheWrite: b.Totals.CacheWrite,
		Output:     b.Totals.Output,
		Reasoning:  b.Totals.Reasoning,
		Total:      b.Totals.Total(),
		Calls:      b.Totals.Calls,
	}
	for _, c := range b.Children {
		r.Children = append(r.Children, toReportRow(c))
	}
	return r
}

func WriteJSON(w io.Writer, opts Options, records []model.Usage, buckets []model.Bucket, snap map[string]aggregate.Snap, stats source.Stats) error {
	rows := make([]ReportRow, 0, len(buckets))
	var calls int64
	for _, b := range buckets {
		rows = append(rows, toReportRow(b))
		calls += b.Totals.Calls
	}
	snapOut := make(map[string]ReportSum, len(snap))
	for k, v := range snap {
		snapOut[k] = toReportSum(v.Totals)
	}
	var sinceS, untilS *string
	if opts.Since != nil {
		s := opts.Since.Format("2006-01-02")
		sinceS = &s
	}
	if opts.Until != nil {
		s := opts.Until.Format("2006-01-02")
		untilS = &s
	}
	view := string(opts.View)
	if view == "" {
		view = string(aggregate.ViewTime)
	}
	// The heat view's answer is the grid, not the rows, so say so rather than
	// labelling day-grouped rows as a heatmap.
	var heat *ReportHeat
	if opts.View == aggregate.ViewHeat {
		h := aggregate.Heatmap(scopeForHeat(records, opts))
		heat = &ReportHeat{Cells: h.Cells, Min: h.Min, Max: h.Max, Total: h.Total}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(Report{
		Period:  string(opts.Period),
		View:    view,
		Heat:    heat,
		Date:    time.Now().Format("2006-01-02"),
		Since:   sinceS,
		Until:   untilS,
		Rows:    rows,
		Snap:    snapOut,
		Records: len(records),
		Calls:   calls,
		Deduped: ReportDedup{Dropped: stats.Dropped, DroppedTokens: stats.DroppedTokens},
	})
}

// WritePlain prints the same table the TUI shows, without the chrome, by
// driving the shared renderer with every group expanded.
func WritePlain(w io.Writer, opts Options, buckets []model.Bucket, snap map[string]aggregate.Snap, stats source.Stats) {
	st := newStyles()
	width := 100
	if term.IsTerminal(int(os.Stdout.Fd())) {
		if got, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && got > 40 {
			width = got
		}
	}

	view := opts.View
	if view == "" {
		view = aggregate.ViewTime
	}
	fmt.Fprintln(w, st.Title.Render(displayName(opts.Name))+
		st.Muted.Render("  ·  "+string(opts.Period)+"  ·  "+string(view)+"  ·  "+time.Now().Format("2006-01-02")))
	fmt.Fprintln(w)

	var parts []string
	for _, key := range []string{"today", "week", "month", "year"} {
		t := snap[key].Totals
		parts = append(parts, st.Faint.Render(strings.ToUpper(key)+" ")+st.Total.Render(model.FmtTokens(t.Total())))
	}
	fmt.Fprintln(w, " "+strings.Join(parts, "    "))
	fmt.Fprintln(w)

	open := map[string]bool{}
	for _, b := range buckets {
		if len(b.Children) > 0 {
			open[b.Key] = true
		}
	}
	m := Model{
		opts:   opts,
		period: opts.Period,
		view:   view,
		rows:   buckets,
		flat:   aggregate.Flatten(buckets, open),
		open:   open,
		width:  width,
		height: 1000,
		cursor: -1,
	}
	for _, line := range m.tableView(width, st) {
		if strings.TrimSpace(stripANSI(line)) == "" {
			continue
		}
		fmt.Fprintln(w, line)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, st.Faint.Render(" total = input + cache.r + cache.w + output   ·   calls = upstream API requests"))
	if stats.Dropped > 0 {
		fmt.Fprintln(w, st.Faint.Render(fmt.Sprintf(
			" deduped %s replayed records (%s tokens) that agents rewrote into resumed sessions",
			model.Comma(int64(stats.Dropped)), model.FmtTokens(stats.DroppedTokens))))
	}
}

// stripANSI removes escape sequences so blank-line detection is not fooled by
// styling on an otherwise empty row.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// scopeForHeat narrows records the same way the TUI does before building the
// grid, so both front ends describe the same window.
func scopeForHeat(records []model.Usage, opts Options) []model.Usage {
	if opts.Since != nil || opts.Until != nil {
		return aggregate.Filter(records, opts.Since, opts.Until)
	}
	if since, until := aggregate.PeriodBounds(opts.Period, time.Now()); since != nil || until != nil {
		return aggregate.Filter(records, since, until)
	}
	return records
}

func WriteHeat(w io.Writer, records []model.Usage, opts Options) {
	st := newStyles()
	width := 100
	if term.IsTerminal(int(os.Stdout.Fd())) {
		if got, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && got > 40 {
			width = got
		}
	}
	m := Model{heat: aggregate.Heatmap(scopeForHeat(records, opts)), width: width, height: 40}
	for _, line := range m.heatView(width, st) {
		fmt.Fprintln(w, line)
	}
}

// displayName falls back to the default when no invoked name was threaded
// through, which happens in tests and library use.
func displayName(name string) string {
	if name == "" {
		return "tcount"
	}
	return name
}

func WriteSources(w io.Writer, name string, asJSON bool) error {
	infos := source.Infos()
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(infos)
	}
	st := newStyles()
	fmt.Fprintln(w, st.Title.Render(displayName(name))+st.Muted.Render("  data sources"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, st.Header.Render(fmt.Sprintf("  %-10s %6s  %-8s %s", "AGENT", "FILES", "STATE", "PATH")))

	// An agent can have several roots; the name is printed once and the rest
	// are indented under it, so a multi-root setup reads as one entry.
	prev := ""
	for _, info := range infos {
		state := st.Faint.Render(padRight("missing", 8))
		files := "—"
		switch {
		case info.Root == "":
			state = st.Faint.Render(padRight("no path", 8))
		case info.Exists && info.Files > 0:
			state = st.Ok.Render(padRight("ready", 8))
			files = fmt.Sprintf("%d", info.Files)
		case info.Exists:
			state = st.Warn.Render(padRight("empty", 8))
			files = "0"
		}
		label := agentStyle(info.Agent).Render(padRight(info.Agent, 10))
		if info.Agent == prev {
			label = padRight("", 10)
		}
		prev = info.Agent
		fmt.Fprintf(w, "  %s %s  %s %s\n", label, padLeft(files, 6), state, st.Dim.Render(info.Root))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, st.Faint.Render(" usage is reconstructed from local session files, not from vendor invoices"))
	fmt.Fprintln(w, st.Faint.Render(" set CLAUDE_CONFIG_DIR (comma-separated) to scan more than one location"))
	return nil
}
