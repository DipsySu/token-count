package ui

import (
	"sort"
	"strings"
	"time"

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
}

type loadedMsg struct {
	records []model.Usage
	dropped int
	dupToks int64
	err     error
}

type tickMsg time.Time

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

	records  []model.Usage
	scoped   []model.Usage
	rows     []model.Bucket
	flat     []model.Bucket
	snapshot map[string]aggregate.Snap
	heat     aggregate.Heat
	open     map[string]bool

	sort    sortKey
	search  string
	seeking bool
	cat     bool

	cursor  int
	offset  int
	loading bool
	err     error
	help    bool
	detail  bool
	spin    int

	dropped int
	dupToks int64
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
	}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.load(), tick()) }

func tick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) load() tea.Cmd {
	opts := m.opts
	return func() tea.Msg {
		c := cache.New("", !opts.NoCache)
		recs, stats := source.CollectStats(opts.Agents, c, nil)
		_ = c.Save()
		return loadedMsg{records: recs, dropped: stats.Dropped, dupToks: stats.DroppedTokens}
	}
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

	case loadedMsg:
		m.loading = false
		m.err = msg.err
		m.records = msg.records
		m.dropped = msg.dropped
		m.dupToks = msg.dupToks
		m.recompute()

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
	key := msg.String()

	if m.seeking {
		switch key {
		case "esc":
			m.seeking, m.search = false, ""
			m.recompute()
		case "enter":
			m.seeking = false
		case "backspace":
			if m.search != "" {
				m.search = m.search[:len(m.search)-1]
				m.recompute()
			}
		default:
			if len(key) == 1 {
				m.search += key
				m.recompute()
			}
		}
		return m, nil
	}

	if m.help {
		if key == "q" || key == "esc" || key == "?" {
			m.help = false
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
	case "esc":
		switch {
		case m.search != "":
			m.search = ""
			m.recompute()
		case m.detail:
			m.detail = false
		}
	case "/":
		m.seeking = true
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
		m.reset()
	case "m":
		m.byModel = !m.byModel
		m.reset()
	case "s":
		m.sort = (m.sort + 1) % 4
		m.reset()
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

func (m *Model) reset() {
	m.cursor, m.offset = 0, 0
	m.recompute()
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
	m.reset()
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

func (m *Model) setAllOpen(v bool) {
	for _, r := range m.rows {
		if len(r.Children) > 0 {
			m.open[r.Key] = v
		}
	}
	m.flat = aggregate.Flatten(m.rows, m.open)
	m.clamp()
}

// scope narrows the full record set to what the current period, agent filter,
// explicit date flags and search term select.
func (m *Model) scope() []model.Usage {
	recs := aggregate.FilterAgent(m.records, m.agent)
	if m.opts.Since != nil || m.opts.Until != nil {
		recs = aggregate.Filter(recs, m.opts.Since, m.opts.Until)
	} else if since, until := aggregate.PeriodBounds(m.period, time.Now()); since != nil || until != nil {
		recs = aggregate.Filter(recs, since, until)
	}
	if q := strings.ToLower(strings.TrimSpace(m.search)); q != "" {
		out := recs[:0:0]
		for _, r := range recs {
			if strings.Contains(strings.ToLower(r.Agent), q) ||
				strings.Contains(strings.ToLower(r.Model), q) ||
				strings.Contains(strings.ToLower(r.Project), q) {
				out = append(out, r)
			}
		}
		recs = out
	}
	return recs
}

func (m *Model) recompute() {
	// Cards always describe the unfiltered-by-period picture so they stay a
	// stable reference while the table below changes shape.
	base := aggregate.FilterAgent(m.records, m.agent)
	if m.opts.Since != nil || m.opts.Until != nil {
		base = aggregate.Filter(base, m.opts.Since, m.opts.Until)
	}
	m.snapshot = aggregate.Snapshot(base, time.Now())

	m.scoped = m.scope()

	switch m.view {
	case aggregate.ViewProject:
		m.rows = aggregate.GroupProject(m.scoped)
	case aggregate.ViewModel:
		m.rows = aggregate.GroupModel(m.scoped)
	case aggregate.ViewHeat:
		m.heat = aggregate.Heatmap(m.scoped)
		m.rows = nil
	default:
		if m.period == aggregate.PeriodToday {
			m.rows = aggregate.GroupAgent(m.scoped, m.byModel)
		} else {
			p := m.period
			if p == aggregate.PeriodAll {
				p = aggregate.PeriodDay
			}
			m.rows = aggregate.GroupTime(m.scoped, p, m.byModel)
		}
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

// bodyRows is how many table lines fit, given everything else on screen.
func (m *Model) bodyRows() int {
	n := m.height - m.chromeHeight()
	if n < 3 {
		n = 3
	}
	return n
}

// chromeHeight counts every line the table does not get: header, tabs, cards,
// column titles, rule, total row and footer.
func (m *Model) chromeHeight() int {
	h := 2 // title + tabs
	if m.showCards() {
		h += 5 // blank + label + value + spark + delta
	}
	h += 3 // blank, column header, rule
	h += 2 // total row + footer
	if m.detail {
		h += detailHeight
	}
	if m.notice() != "" {
		h++
	}
	return h
}

func (m *Model) showCards() bool { return m.height >= 22 && m.width >= 60 }

// notice surfaces the deduplication result, which explains why these totals
// are lower than a naive count of the session logs.
func (m Model) notice() string {
	if m.loading || m.dropped == 0 {
		return ""
	}
	return "deduped " + model.Comma(int64(m.dropped)) + " replayed records (" +
		model.FmtTokens(m.dupToks) + " tokens) — Claude Code rewrites history on resume"
}

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
