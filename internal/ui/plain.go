package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
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

// Report is the --json output. Since and Until are the first and last local
// calendar dates that the rows, the heat grid, Records and Calls all cover,
// whether the window came from explicit dates or from the span; a side with
// no bound is omitted. UTCOffset is the local offset at Date, when the report
// was made. A window that crosses a daylight-saving change began under a
// different offset, so it is not the offset of every date in the window.
type Report struct {
	Period    string               `json:"period"`
	View      string               `json:"view"`
	Heat      *ReportHeat          `json:"heat,omitempty"`
	Date      string               `json:"date,omitempty"`
	Since     *string              `json:"since,omitempty"`
	Until     *string              `json:"until,omitempty"`
	UTCOffset string               `json:"utc_offset"`
	Rows      []ReportRow          `json:"rows"`
	Snap      map[string]ReportSum `json:"snapshot,omitempty"`
	Records   int                  `json:"records"`
	Calls     int64                `json:"calls"`
	Deduped   ReportDedup          `json:"deduped"`
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

// Export is one non-interactive answer: the window a request covers, the
// records inside it and the rows they group into, all computed from a single
// reading of the clock. Every printed or encoded figure is derived from it, so
// the header date, the rows, the heat grid and the counts always agree.
type Export struct {
	Now     time.Time
	Since   *time.Time // the effective window; nil leaves that side open
	Until   *time.Time
	Records []model.Usage // only the records inside the window
	// Rows and Snap are left empty by ScopeExport; BuildExport fills them.
	Rows []model.Bucket
	Snap map[string]aggregate.Snap
}

// ScopeExport fixes what an export covers, from one reading of the clock: the
// window and the records inside it. That is all the heatmap prints, so it
// stops there rather than paying for rows and summary cards nobody sees.
func ScopeExport(records []model.Usage, opts Options) Export {
	now := opts.now()
	since, until := window(opts, opts.Period, now)
	return Export{
		Now:     now,
		Since:   since,
		Until:   until,
		Records: aggregate.Filter(records, since, until),
	}
}

// BuildExport completes ScopeExport with the rows and the summary cards that
// the table and the JSON report print. It applies the same window and
// grouping the TUI uses, so the two front ends can never disagree about what
// a period means.
func BuildExport(records []model.Usage, opts Options) Export {
	ex := ScopeExport(records, opts)
	ex.Rows = group(ex.Records, opts.View, opts.Period, opts.ByModel)

	// The cards ignore the span, exactly as in the TUI, but not explicit dates.
	base := records
	if opts.Since != nil || opts.Until != nil {
		base = aggregate.Filter(base, opts.Since, opts.Until)
	}
	ex.Snap = aggregate.Snapshot(base, ex.Now)
	return ex
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

// WriteJSON encodes the --json report. It needs the rows and the summary
// cards, so ex has to come from BuildExport.
func WriteJSON(w io.Writer, opts Options, ex Export, stats source.Stats) error {
	rows := make([]ReportRow, 0, len(ex.Rows))
	var calls int64
	for _, b := range ex.Rows {
		rows = append(rows, toReportRow(b))
		calls += b.Totals.Calls
	}
	snapOut := make(map[string]ReportSum, len(ex.Snap))
	for k, v := range ex.Snap {
		snapOut[k] = toReportSum(v.Totals)
	}
	view := string(opts.View)
	if view == "" {
		view = string(aggregate.ViewTime)
	}
	// The heat view's answer is the grid, not the rows, so say so rather than
	// labelling day-grouped rows as a heatmap.
	var heat *ReportHeat
	if opts.View == aggregate.ViewHeat {
		h := aggregate.Heatmap(ex.Records)
		heat = &ReportHeat{Cells: h.Cells, Min: h.Min, Max: h.Max, Total: h.Total}
	}
	now := ex.Now.In(time.Local)
	since, until := windowDates(ex.Since, ex.Until)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(Report{
		Period:    string(opts.Period),
		View:      view,
		Heat:      heat,
		Date:      now.Format("2006-01-02"),
		Since:     optional(since),
		Until:     optional(until),
		UTCOffset: now.Format("-07:00"),
		Rows:      rows,
		Snap:      snapOut,
		Records:   len(ex.Records),
		Calls:     calls,
		Deduped:   ReportDedup{Dropped: stats.Dropped, DroppedTokens: stats.DroppedTokens},
	})
}

// optional is a field that is left out of the report when it is empty, as an
// open side of the window is.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// exportWidth is the table width for a text export: the terminal's when
// stdout is one, otherwise a fixed width that suits a file or a pipe. It is a
// variable so the tests can pin it, since a test binary run by hand prints to
// a terminal.
var exportWidth = func() int {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		if got, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && got > 40 {
			return got
		}
	}
	return 100
}

// WritePlain prints the same table the TUI shows, without the chrome, by
// driving the shared renderer with every group expanded and every row drawn.
// Like WriteJSON, it needs an export from BuildExport.
func WritePlain(w io.Writer, opts Options, ex Export, stats source.Stats) {
	st := newStyles()
	width := exportWidth()

	view := opts.View
	if view == "" {
		view = aggregate.ViewTime
	}
	fmt.Fprintln(w, st.Title.Render(displayName(opts.Name))+
		st.Muted.Render("  ·  "+string(opts.Period)+"  ·  "+string(view)+"  ·  "+ex.Now.In(time.Local).Format("2006-01-02")))
	fmt.Fprintln(w)

	var parts []string
	for _, key := range []string{"today", "week", "month", "year"} {
		t := ex.Snap[key].Totals
		parts = append(parts, st.Faint.Render(strings.ToUpper(key)+" ")+st.Total.Render(model.FmtTokens(t.Total())))
	}
	fmt.Fprintln(w, " "+strings.Join(parts, "    "))
	fmt.Fprintln(w)

	open := map[string]bool{}
	for _, b := range ex.Rows {
		if len(b.Children) > 0 {
			open[b.Key] = true
		}
	}
	m := Model{
		opts:    opts,
		period:  opts.Period,
		view:    view,
		byModel: opts.ByModel,
		rows:    ex.Rows,
		flat:    aggregate.Flatten(ex.Rows, open),
		open:    open,
		width:   width,
		cursor:  -1,
	}
	for _, line := range m.renderTable(width, st, 0, len(m.flat)) {
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
// styling on an otherwise empty row. It parses them properly rather than
// skipping to the next 'm', which would swallow text after any sequence that
// does not end in one.
func stripANSI(s string) string { return ansi.Strip(s) }

// WriteHeat prints the hour-of-week grid for the export's window, the same
// records its rows and counts are drawn from. It reads only the records, so
// an export from ScopeExport is enough.
func WriteHeat(w io.Writer, ex Export) {
	st := newStyles()
	width := exportWidth()
	m := Model{heat: aggregate.Heatmap(ex.Records), width: width}
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
