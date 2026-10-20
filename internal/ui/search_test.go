package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

// key is a press of a named key.
func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// runes is typed text, which the terminal delivers as one key message: an
// input method commits a whole word at once.
func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// paste is text that arrives as a bracketed paste.
func paste(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true}
}

// send delivers each message to the model in turn, as the program would.
func send(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

// quits reports whether cmd is the command that ends the program.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// Search terms are text, however they arrive. Typed text used to be kept only
// when its key name was one byte long, so Chinese, pasted text and anything
// else that arrives several runes at a time was dropped without a word, and
// backspace cut the last byte off a character.
func TestSearchTakesCJKPastesAndWholeCharacters(t *testing.T) {
	m := send(newTestModel(t, 120, 30), runes("/"), runes("中文"))
	if m.search != "中文" {
		t.Fatalf("typing 中文 searched for %q", m.search)
	}
	m = send(m, key(tea.KeyBackspace))
	if m.search != "中" {
		t.Fatalf("backspace left %q, want 中", m.search)
	}
	m = send(m, key(tea.KeyBackspace), key(tea.KeyBackspace))
	if m.search != "" {
		t.Fatalf("backspacing past the start left %q", m.search)
	}
	// A paste inserts its text, with tabs and line breaks as spaces; a
	// Windows line end is one break, not two.
	m = send(m, paste("co\tde\nx\r\ny"))
	if m.search != "co de x y" {
		t.Fatalf("pasting searched for %q, want %q", m.search, "co de x y")
	}
	m = send(m, key(tea.KeyCtrlU), paste("codex"))
	if m.search != "codex" || len(m.scoped) != 4 {
		t.Fatalf("pasted codex: search %q over %d records, want codex over 4", m.search, len(m.scoped))
	}
}

// ctrl+c quits from every state. The search box and the help overlay used to
// read it as an ordinary key, so the only way out of them was esc.
func TestCtrlCQuitsFromAnyState(t *testing.T) {
	states := []struct {
		name string
		into func(m Model) Model
	}{
		{"table", func(m Model) Model { return m }},
		{"search box", func(m Model) Model { return send(m, runes("/"), runes("cl")) }},
		{"empty search box", func(m Model) Model { return send(m, runes("/")) }},
		{"kept search", func(m Model) Model { return send(m, runes("/"), runes("cl"), key(tea.KeyEnter)) }},
		{"help", func(m Model) Model { return send(m, runes("?")) }},
		{"detail", func(m Model) Model { return send(m, runes("d")) }},
		{"loading", func(m Model) Model { m.loading = true; return m }},
		{"rescanning", func(m Model) Model { return send(m, runes("r")) }},
		{"too small", func(m Model) Model { return send(m, tea.WindowSizeMsg{Width: 20, Height: 5}) }},
	}
	for _, s := range states {
		m := s.into(newTestModel(t, 120, 30))
		if _, cmd := m.Update(key(tea.KeyCtrlC)); !quits(cmd) {
			t.Errorf("%s: ctrl+c does not quit", s.name)
		}
	}
}

// The help overlay closes on q, esc and ?, without quitting.
func TestHelpClosesOnQEscAndQuestionMark(t *testing.T) {
	for _, k := range []tea.KeyMsg{runes("q"), key(tea.KeyEsc), runes("?")} {
		m := send(newTestModel(t, 120, 30), runes("?"))
		if !m.help {
			t.Fatal("? does not open the help")
		}
		next, cmd := m.Update(k)
		if m = next.(Model); m.help || quits(cmd) {
			t.Errorf("%s: help open %v, quit %v; want it closed and the program running", k, m.help, quits(cmd))
		}
	}
}

// While the search box has the keyboard, letters are text, never commands.
func TestSearchBoxTakesLettersAsText(t *testing.T) {
	m := send(newTestModel(t, 120, 30), runes("/"))
	for _, r := range "qasmdEC?rtpMHjk/0 " {
		next, cmd := m.Update(runes(string(r)))
		m = next.(Model)
		if quits(cmd) {
			t.Fatalf("%q quit the program", r)
		}
	}
	if m.search != "qasmdEC?rtpMHjk/0 " {
		t.Errorf("typed text searched for %q", m.search)
	}
	if m.agent != "" || m.sort != sortDefault || m.byModel || m.detail || m.help ||
		m.loading || m.view != aggregate.ViewTime || m.period != aggregate.PeriodDay {
		t.Errorf("a letter ran as a command: agent %q sort %s by-model %v detail %v help %v loading %v view %s span %s",
			m.agent, m.sort, m.byModel, m.detail, m.help, m.loading, m.view, m.period)
	}
}

// Every edit applies as it is typed; enter keeps the search and hands the
// keys back to the table; / reopens the box on the same term; esc clears it.
func TestSearchAppliesLiveEnterKeepsEscClears(t *testing.T) {
	m := send(newTestModel(t, 120, 30), runes("/"))
	for _, r := range "codex" {
		m = send(m, runes(string(r)))
		// Each prefix selects what a brute-force scan does.
		if got, want := len(m.scoped), bruteMatches(m.records, m.search); got != want {
			t.Fatalf("after %q: %d records, a scan finds %d", m.search, got, want)
		}
	}
	m = send(m, key(tea.KeyEnter))
	if m.search != "codex" || len(m.scoped) != 4 {
		t.Fatalf("enter: search %q over %d records, want codex over 4", m.search, len(m.scoped))
	}
	// The keys are commands again, and the search still holds.
	if m = send(m, runes("s")); m.sort != sortTotal || m.search != "codex" {
		t.Fatalf("after enter, s gave sort %s and search %q", m.sort, m.search)
	}
	// / brings the term back for editing rather than starting over.
	if m = send(m, runes("/"), runes("x")); m.search != "codexx" {
		t.Fatalf("reopening the search box gave %q, want codexx", m.search)
	}
	m = send(m, key(tea.KeyEsc))
	if m.search != "" || len(m.scoped) != 12 {
		t.Fatalf("esc: search %q over %d records, want none over 12", m.search, len(m.scoped))
	}
	if m = send(m, runes("s")); m.sort != sortCalls {
		t.Fatalf("after esc, s did not cycle the sort: %s", m.sort)
	}
	// Esc in a box opened on an existing search clears that search too.
	m = send(m, runes("/"), runes("grok"), key(tea.KeyEnter), runes("/"), key(tea.KeyEsc))
	if m.search != "" {
		t.Fatalf("esc left the search %q", m.search)
	}
}

// bruteMatches counts the records a search for q selects, by the definition
// the table promises: agent, model or project, as stored or as shown.
func bruteMatches(recs []model.Usage, q string) int {
	q = strings.ToLower(strings.TrimSpace(q))
	n := 0
	for _, r := range recs {
		for _, field := range []string{r.Agent, r.Model, model.ShortModel(r.Model), r.Project, model.ShortProject(r.Project)} {
			if strings.Contains(strings.ToLower(field), q) {
				n++
				break
			}
		}
	}
	return n
}

// What the table shows can be typed back: a project as its ~/ form, a model
// as its short name, a missing one as (unknown), in any case.
func TestSearchMatchesWhatTheTableShows(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	pinLocal(t)
	mk := func(agent, mdl, project string) model.Usage {
		u, ok := model.Make(model.MakeOpts{Agent: agent, Time: testNow.Add(-time.Hour), Model: mdl, Project: project, Input: 10})
		if !ok {
			t.Fatal("record rejected")
		}
		return u
	}
	recs := []model.Usage{
		mk("claude", "anthropic/claude-sonnet-4-5-20250929", "/home/u/work/token_count"),
		mk("codex", "gpt-5-codex", "/home/u/work/site"),
		mk("grok", "grok-code-fast-1", "/srv/build"),
		mk("gemini", "", ""),
	}
	m := New(Options{Period: aggregate.PeriodToday, View: aggregate.ViewProject, Agents: model.Agents, Now: testClock})
	m.width, m.height = 120, 30
	m = send(m, loadedMsg{records: recs})
	for _, c := range []struct {
		q    string
		want []string // agents of the records it selects
	}{
		{"~/work", []string{"claude", "codex"}},
		{"~/WORK/TOKEN", []string{"claude"}},
		{"/home/u/work/site", []string{"codex"}},
		{"CLAUDE-SONNET-4-5", []string{"claude"}},
		{"anthropic", []string{"claude"}},
		{"Grok", []string{"grok"}},
		{"(unknown)", []string{"gemini"}},
		{"  codex  ", []string{"codex"}},
		{"~/srv", nil},
	} {
		got := send(m, runes("/"), key(tea.KeyCtrlU), paste(c.q), key(tea.KeyEnter))
		var agents []string
		for _, r := range got.scoped {
			agents = append(agents, r.Agent)
		}
		if strings.Join(agents, ",") != strings.Join(c.want, ",") {
			t.Errorf("searching %q selects %v, want %v", c.q, agents, c.want)
		}
	}
}

// While the search box is open the footer says how to quit, which is the one
// thing on it that matters on a narrow terminal: the explanation gives way
// first. At 40 columns it was cut mid-word and took ctrl+c with it.
func TestSearchFooterKeepsCtrlC(t *testing.T) {
	for _, w := range []int{30, 40, 52, 120} {
		m := send(newTestModel(t, w, 30), runes("/"))
		lines := strings.Split(stripANSI(m.View()), "\n")
		foot := lines[len(lines)-1]
		if !strings.Contains(foot, "ctrl+c quit") || strings.Contains(foot, "…") {
			t.Errorf("%d columns: footer %q, want ctrl+c quit, uncut", w, foot)
		}
		if w >= 52 && !strings.Contains(foot, "type an agent, model or project") {
			t.Errorf("%d columns: footer %q lost the explanation", w, foot)
		}
	}
}
