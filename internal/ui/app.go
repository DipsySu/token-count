package ui

import (
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"token-count/internal/aggregate"
	"token-count/internal/cache"
	"token-count/internal/model"
	"token-count/internal/source"
)

type Options struct {
	Period  aggregate.Period
	View    aggregate.View
	Agents  []string
	ByModel bool
	Since   *time.Time
	Until   *time.Time
	NoCache bool
	NoCat   bool
	// Name is what the binary was invoked as, so an installed alias is what
	// the UI calls itself rather than a hardcoded name.
	Name    string
	Version string
	// Now is the clock that decides what "today" is; nil means time.Now.
	// Tests pin it so their answers do not depend on the day they run.
	Now func() time.Time
}

// now reads the clock. Each computation reads it once and passes the instant
// down rather than reading again: two readings either side of midnight would
// describe two different days in one answer.
func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

type loadedMsg struct {
	records []model.Usage
	dropped int
	dupToks int64
	// issues are the files the load could not read completely. They replace
	// the previous load's, since a rescan reads every file again.
	issues []source.Issue
	err    error
}

type tickMsg time.Time

// clockMsg is the slow tick on which the TUI notices that the local date has
// changed while it stayed open.
type clockMsg time.Time

// clockEvery is how often the TUI looks at the date. Midnight is the only
// moment it waits for, so a look every half minute is plenty.
const clockEvery = 30 * time.Second

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// sortKey selects the column the table is ordered by.
type sortKey int

const (
	sortDefault sortKey = iota // chronological for time views, volume elsewhere
	sortTotal
	sortCalls
	sortPerCall
)

func (s sortKey) String() string {
	switch s {
	case sortTotal:
		return "total"
	case sortCalls:
		return "calls"
	case sortPerCall:
		return "per-call"
	default:
		return "default"
	}
}

type Model struct {
	opts    Options
	width   int
	height  int
	period  aggregate.Period
	view    aggregate.View
	agent   string
	byModel bool

	// records are the load's records. They are only ever replaced through
	// setRecords, which bumps gen, so that nothing worked out from one load
	// is served for the next.
	records []model.Usage
	gen     int
	ix      *searchIndex

	scoped   []model.Usage
	rows     []model.Bucket
	flat     []model.Bucket
	snapshot map[string]aggregate.Snap
	// peak is the busiest single day in the snapshot, the yardstick for the
	// mascot's mood and the card hues. Both are drawn on every frame, so
	// they are worked out with the snapshot rather than for each frame.
	peak int64
	mood catMood
	heat aggregate.Heat
	open map[string]bool

	// asOf is the instant the figures on screen were computed against, and
	// since/until the window it gave the table (nil leaves a side open).
	// Rendering reads these instead of the clock, so everything drawn
	// describes the same moment. first is the earliest record in that window
	// for the agent filter, zero when there is none: it names the start of a
	// window that has no start of its own.
	asOf         time.Time
	since, until *time.Time
	first        time.Time

	sort   sortKey
	search string
	// input is the search box; it has the keyboard while it is focused.
	input textinput.Model
	cat   bool

	cursor int
	offset int
	// kept is the last row selected before the table went empty, as a
	// search matching nothing leaves it. An empty table has no selection of
	// its own to carry, and without this the row came back at the top once
	// the search matched again.
	kept selection

	loading bool
	err     error
	help    bool
	detail  bool
	spin    int

	dropped int
	dupToks int64
	issues  []source.Issue

	memo *memo
}

func RunTUI(opts Options) error {
	p := tea.NewProgram(New(opts), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

func New(opts Options) Model {
	if opts.Period == "" {
		opts.Period = aggregate.PeriodToday
	}
	if opts.View == "" {
		opts.View = aggregate.ViewTime
	}
	if len(opts.Agents) == 0 {
		opts.Agents = append([]string{}, model.Agents...)
	}
	if opts.Name == "" {
		opts.Name = "tcount"
	}
	return Model{
		opts:     opts,
		period:   opts.Period,
		view:     opts.View,
		byModel:  opts.ByModel,
		width:    96,
		height:   30,
		loading:  true,
		cat:      !opts.NoCat,
		open:     map[string]bool{},
		snapshot: map[string]aggregate.Snap{},
		input:    newSearchInput(),
		memo:     &memo{},
	}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.load(), tick(), clockTick()) }

func tick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func clockTick() tea.Cmd {
	return tea.Tick(clockEvery, func(t time.Time) tea.Msg { return clockMsg(t) })
}

func (m Model) load() tea.Cmd {
	opts := m.opts
	return func() tea.Msg {
		c := cache.New("", !opts.NoCache)
		recs, stats := source.CollectStats(opts.Agents, c, nil)
		_ = c.Save()
		return loadedMsg{records: recs, dropped: stats.Dropped, dupToks: stats.DroppedTokens, issues: stats.Issues}
	}
}

// setRecords replaces the records, as a load does. It bumps the generation,
// so that nothing worked out from the old records is served again, and
// indexes the new ones for searching.
func (m *Model) setRecords(recs []model.Usage) {
	m.records = recs
	m.gen++
	m.ix = newSearchIndex(recs)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.clamp()

	case tickMsg:
		if m.loading {
			m.spin++
			return m, tick()
		}

	case clockMsg:
		// The figures describe the day they were computed on. Once the local
		// date moves on, an open TUI would go on calling yesterday's figures
		// today's. A scan under way will bring them up to date when it ends.
		if !m.loading && m.gen > 0 && !aggregate.Day(m.opts.now()).Equal(aggregate.Day(m.asOf)) {
			m.refresh()
		}
		return m, clockTick()

	case loadedMsg:
		m.loading = false
		m.err = msg.err
		m.setRecords(msg.records)
		m.dropped = msg.dropped
		m.dupToks = msg.dupToks
		m.issues = msg.issues
		m.refresh()

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.cursor -= 3
			m.clamp()
		case tea.MouseButtonWheelDown:
			m.cursor += 3
			m.clamp()
		}

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c is the way out whatever has the keyboard. The search box and
	// the help overlay used to take it as an ordinary key, which left esc as
	// the only way out of them.
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if m.input.Focused() {
		return m.onSearchKey(msg)
	}

	key := msg.String()
	if m.help {
		if key == "q" || key == "esc" || key == "?" {
			m.help = false
		}
		return m, nil
	}

	switch key {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
	case "esc":
		switch {
		case m.search != "":
			m.setSearch("")
		case m.detail:
			m.detail = false
			m.clamp()
		}
	case "/":
		// The box opens on the search in force, to be edited rather than
		// typed again.
		m.input.SetValue(m.search)
		m.input.CursorEnd()
		// Focus changes m.input, so it must run before m is returned: in
		// one return statement the order of the two is unspecified.
		cmd := m.input.Focus()
		return m, cmd
	case "r":
		m.loading = true
		return m, tea.Batch(m.load(), tick())

	case "left", "h", "[":
		m.setPeriod(prevPeriod(m.period))
	case "right", "l", "]":
		m.setPeriod(nextPeriod(m.period))
	case "tab":
		m.cycleView(1)
	case "shift+tab":
		m.cycleView(-1)
	case "1":
		m.setPeriod(aggregate.PeriodToday)
	case "2":
		m.setPeriod(aggregate.PeriodDay)
	case "3":
		m.setPeriod(aggregate.PeriodWeek)
	case "4":
		m.setPeriod(aggregate.PeriodMonth)
	case "5":
		m.setPeriod(aggregate.PeriodYear)
	case "0":
		m.setPeriod(aggregate.PeriodAll)

	case "p":
		m.setView(aggregate.ViewProject)
	case "M":
		m.setView(aggregate.ViewModel)
	case "H":
		m.setView(aggregate.ViewHeat)
	case "t":
		m.setView(aggregate.ViewTime)

	case "a":
		m.cycleAgent(1)
	case "A":
		m.agent = ""
		m.refresh()
	case "m":
		m.byModel = !m.byModel
		m.refresh()
	case "s":
		m.sort = (m.sort + 1) % 4
		m.refresh()
	case "d":
		m.detail = !m.detail
		m.clamp()
	case "c":
		m.cat = !m.cat
		m.clamp()

	case "enter", " ":
		m.toggleOpen()
	case "E":
		m.setAllOpen(true)
	case "C":
		m.setAllOpen(false)

	case "down", "j":
		m.cursor++
		m.clamp()
	case "up", "k":
		m.cursor--
		m.clamp()
	case "pgdown", "ctrl+d":
		m.cursor += m.bodyRows()
		m.clamp()
	case "pgup", "ctrl+u":
		m.cursor -= m.bodyRows()
		m.clamp()
	case "g", "home":
		m.cursor = 0
		m.clamp()
	case "G", "end":
		m.cursor = len(m.flat) - 1
		m.clamp()
	}
	return m, nil
}

// onSearchKey handles a key while the search box has the keyboard. Enter
// keeps the search and hands the keys back to the table; esc clears it. Every
// other key, letters included, is the box's: an edit, which applies at once,
// or a cursor movement.
func (m Model) onSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.input.Blur()
		if searchTerm(m.search) == "" {
			// Nothing but spaces matches everything; keeping it would leave
			// the box holding a search that the scope line cannot show.
			m.input.Reset()
			m.search = ""
		}
		return m, nil
	case tea.KeyEsc:
		m.input.Blur()
		m.input.Reset()
		m.setSearch("")
		return m, nil
	}
	if msg.Paste {
		msg.Runes = pasted(msg.Runes)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.setSearch(m.input.Value())
	return m, cmd
}

// setSearch applies a search, keeping the selected row where it can. An edit
// that changes only spaces at either end leaves the term matched the same,
// and the table as it is.
func (m *Model) setSearch(s string) {
	same := searchTerm(s) == searchTerm(m.search)
	m.search = s
	if !same {
		m.refresh()
	}
}

func (m *Model) setPeriod(p aggregate.Period) {
	m.period = p
	m.reset()
}

// views is the second axis of the tab strip, independent of the period.
func views() []aggregate.View {
	return []aggregate.View{
		aggregate.ViewTime,
		aggregate.ViewProject,
		aggregate.ViewModel,
		aggregate.ViewHeat,
	}
}

func (m *Model) cycleView(delta int) {
	order := views()
	idx := 0
	for i, v := range order {
		if v == m.view {
			idx = i
			break
		}
	}
	m.view = order[(idx+delta+len(order))%len(order)]
	m.reset()
}

func (m *Model) setView(v aggregate.View) {
	if m.view == v && v != aggregate.ViewTime {
		v = aggregate.ViewTime // pressing the same key again returns to time
	}
	m.view = v
	m.reset()
}

// reset recomputes for a new span or view. That is another table altogether,
// so the selection starts again at the top.
func (m *Model) reset() {
	m.cursor, m.offset = 0, 0
	m.kept = selection{}
	m.recompute()
}

// refresh recomputes the same table under changed filters, data, sort or
// date, keeping the selection on the row it was on.
func (m *Model) refresh() {
	sel := m.selection()
	if sel.ok {
		m.kept = sel
	} else {
		sel = m.kept
	}
	m.recompute()
	m.reselect(sel)
}

func (m *Model) cycleAgent(delta int) {
	names := append([]string{""}, m.opts.Agents...)
	idx := 0
	for i, n := range names {
		if n == m.agent {
			idx = i
			break
		}
	}
	m.agent = names[(idx+delta+len(names))%len(names)]
	m.refresh()
}

func (m *Model) toggleOpen() {
	if m.cursor < 0 || m.cursor >= len(m.flat) {
		return
	}
	row := m.flat[m.cursor]
	if len(row.Children) == 0 {
		return
	}
	m.open[row.Key] = !m.open[row.Key]
	m.flat = aggregate.Flatten(m.rows, m.open)
	m.clamp()
}

// setAllOpen expands or collapses every row. A selected breakdown row that
// collapses away hands the selection to its parent.
func (m *Model) setAllOpen(v bool) {
	sel := m.selection()
	for _, r := range m.rows {
		if len(r.Children) > 0 {
			m.open[r.Key] = v
		}
	}
	m.flat = aggregate.Flatten(m.rows, m.open)
	m.reselect(sel)
}

// selection identifies the selected row by its key and its parent's, so that
// it can be found again once the rows have been rebuilt around it. Keys are
// unique among top-level rows, and a breakdown row's key is unique under its
// parent.
type selection struct {
	key, parent string // parent is "" for a top-level row
	ok          bool   // a row was selected at all
	index       int    // where it was
	screen      int    // how far below the first visible row it was
}

func (m *Model) selection() selection {
	sel := selection{index: m.cursor, screen: m.cursor - m.offset}
	if m.cursor < 0 || m.cursor >= len(m.flat) {
		return sel
	}
	sel.ok, sel.key = true, m.flat[m.cursor].Key
	if m.flat[m.cursor].Depth > 0 {
		for i := m.cursor - 1; i >= 0; i-- {
			if m.flat[i].Depth == 0 {
				sel.parent = m.flat[i].Key
				break
			}
		}
	}
	return sel
}

// reselect moves the cursor to the row sel names: the row itself while it is
// still listed, else its parent, else whatever now sits where it was. The row
// keeps its place on screen as far as the scroll range allows, so that a sort
// or a filter does not make the table jump under the reader, and clamp keeps
// it visible.
func (m *Model) reselect(sel selection) {
	at := sel.index
	if sel.ok {
		found, parent := -1, -1
		top := "" // the key of the latest top-level row
		for i, b := range m.flat {
			under := top
			if b.Depth == 0 {
				under, top = "", b.Key
				if parent < 0 && sel.parent != "" && b.Key == sel.parent {
					parent = i
				}
			}
			if b.Key == sel.key && under == sel.parent {
				found = i
				break
			}
		}
		switch {
		case found >= 0:
			at = found
		case parent >= 0:
			at = parent
		}
	}
	m.cursor = at
	m.offset = at - sel.screen
	m.clamp()
}

// window is the date range a request covers: the explicit --since/--until
// dates when either is given, otherwise the span's default bounds as of now.
// It is the only definition of scope. The TUI and every export narrow through
// it, so the rows, the heat grid, the call count and the record count can
// never describe different windows.
func window(opts Options, period aggregate.Period, now time.Time) (since, until *time.Time) {
	if opts.Since != nil || opts.Until != nil {
		return opts.Since, opts.Until
	}
	return aggregate.PeriodBounds(period, now)
}

// dateLayout is how a calendar date is printed everywhere.
const dateLayout = "2006-01-02"

// windowDates names the first and last calendar dates a window admits, as
// YYYY-MM-DD; an open side is "". Anything that prints a window should use
// it, because formatting a bound names the wrong day in some zones. A bound
// is the instant time.Date gives for a local midnight, and where a DST change
// skips midnight time.Date moves it into the evening before: Santiago's
// clocks jump from 00:00 to 01:00 on 6 September 2026, so the bound standing
// for the 6th is 23:00 on the 5th.
//
// The dates are the ones aggregate.Filter admits: since admits the dates
// whose midnight is not before it, and until those whose midnight is not
// after it.
func windowDates(since, until *time.Time) (first, last string) {
	if since != nil {
		d := civilDate(*since)
		for localMidnight(d).Before(*since) {
			d = d.AddDate(0, 0, 1)
		}
		first = d.Format(dateLayout)
	}
	if until != nil {
		// The bound can stand for the day after its own, when that day's
		// midnight was moved back into this one.
		d := civilDate(*until).AddDate(0, 0, 1)
		for localMidnight(d).After(*until) {
			d = d.AddDate(0, 0, -1)
		}
		last = d.Format(dateLayout)
	}
	return first, last
}

// civilDate is t's local calendar date, held as midnight UTC, where stepping
// a day is exact.
func civilDate(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// localMidnight is the instant that stands for a calendar date wherever the
// aggregate package takes one: what time.Date gives for its local midnight.
func localMidnight(date time.Time) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
}

// group arranges records that are already in scope into the table's rows.
// The heat view's answer is its grid, so it falls through to the time
// grouping: a report still carries rows for it whose totals match the grid.
func group(recs []model.Usage, view aggregate.View, period aggregate.Period, byModel bool) []model.Bucket {
	switch view {
	case aggregate.ViewProject:
		return aggregate.GroupProject(recs)
	case aggregate.ViewModel:
		return aggregate.GroupModel(recs)
	}
	switch period {
	case aggregate.PeriodToday:
		return aggregate.GroupAgent(recs, byModel)
	case aggregate.PeriodAll:
		period = aggregate.PeriodDay
	}
	return aggregate.GroupTime(recs, period, byModel)
}

func (m *Model) recompute() {
	if m.memo == nil {
		m.memo = &memo{}
	}
	// The clock is read once, and the cards, the window and the title bar
	// all use that one instant even if the recompute runs across midnight.
	m.asOf = m.opts.now()
	m.since, m.until = window(m.opts, m.period, m.asOf)

	c := m.figures()
	m.snapshot, m.peak, m.mood = c.snap, c.peak, c.mood

	b := m.scopeBase()
	m.first = b.first
	m.scoped = m.narrow(b)

	if m.view == aggregate.ViewHeat {
		m.heat = aggregate.Heatmap(m.scoped)
		m.rows = nil
	} else {
		m.rows = group(m.scoped, m.view, m.period, m.byModel)
	}
	m.applySort()
	m.flat = aggregate.Flatten(m.rows, m.open)
	m.clamp()
}

func (m *Model) applySort() {
	if m.sort == sortDefault || len(m.rows) == 0 {
		return
	}
	less := func(a, b model.Bucket) bool {
		switch m.sort {
		case sortCalls:
			return a.Totals.Calls > b.Totals.Calls
		case sortPerCall:
			return a.Totals.PerCall() > b.Totals.PerCall()
		default:
			return a.Totals.Total() > b.Totals.Total()
		}
	}
	sort.SliceStable(m.rows, func(i, j int) bool { return less(m.rows[i], m.rows[j]) })
	for i := range m.rows {
		kids := m.rows[i].Children
		sort.SliceStable(kids, func(a, b int) bool { return less(kids[a], kids[b]) })
	}
}

// bodyRows is how many table rows fit, given everything else on screen.
func (m *Model) bodyRows() int { return max(m.layout().body, 1) }

func (m *Model) clamp() {
	n := len(m.flat)
	if n == 0 {
		m.cursor, m.offset = 0, 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	vis := m.bodyRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+vis {
		m.offset = m.cursor - vis + 1
	}
	if m.offset > n-vis {
		m.offset = n - vis
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func prevPeriod(p aggregate.Period) aggregate.Period {
	order := periods()
	for i, x := range order {
		if x == p {
			return order[(i-1+len(order))%len(order)]
		}
	}
	return aggregate.PeriodToday
}

func nextPeriod(p aggregate.Period) aggregate.Period {
	order := periods()
	for i, x := range order {
		if x == p {
			return order[(i+1)%len(order)]
		}
	}
	return aggregate.PeriodDay
}

func periods() []aggregate.Period {
	return []aggregate.Period{
		aggregate.PeriodToday,
		aggregate.PeriodDay,
		aggregate.PeriodWeek,
		aggregate.PeriodMonth,
		aggregate.PeriodYear,
		aggregate.PeriodAll,
	}
}

func agentLabel(name string) string {
	if name == "" {
		return "all"
	}
	return name
}

// trimmedSearch is the search as the scope line and the empty table quote
// it: without the spaces at either end, which do not count.
func (m Model) trimmedSearch() string { return strings.TrimSpace(m.search) }
