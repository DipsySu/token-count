package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"token-count/internal/ui"
)

// Aliases are installed as symlinks, so the binary has to work out what it was
// called by. Getting this wrong makes an alias second-class: you run
// `tokenSummary --help` and it tells you about `tcount`.
func TestInvokedName(t *testing.T) {
	cases := []struct{ argv0, want string }{
		{"tcount", "tcount"},
		{"./tcount", "tcount"},
		{"/home/u/.local/bin/tcount", "tcount"},
		{"/home/u/.local/bin/token-count", "token-count"},
		{"/home/u/.local/bin/tokenSummary", "tokenSummary"},
		{"tsum", "tsum"},
		{`C:\tools\tcount.exe`, `C:\tools\tcount`}, // only POSIX separators are split
		{"tcount.exe", "tcount"},
		// Nothing usable to show falls back to the default rather than
		// printing an empty or nonsense name.
		{"", defaultName},
		{".", defaultName},
		{"..", defaultName},
		{"/", defaultName},
	}
	for _, c := range cases {
		if got := invokedName(c.argv0); got != c.want {
			t.Errorf("invokedName(%q) = %q, want %q", c.argv0, got, c.want)
		}
	}
}

// The invoked name has to reach every place the tool names itself, or an alias
// looks half-applied.
func TestUsageUsesInvokedName(t *testing.T) {
	for _, name := range []string{"tcount", "tokenSummary", "tsum"} {
		u := usage(name)
		if !strings.HasPrefix(u, name+" — ") {
			t.Errorf("%s: usage does not open with the invoked name:\n%s", name, u[:40])
		}
		// The worked examples must be runnable as written.
		if !strings.Contains(u, name+" week projects") {
			t.Errorf("%s: usage examples still use another name", name)
		}
		if name != "tcount" && strings.Contains(u, "tcount") {
			t.Errorf("%s: usage leaked the default name", name)
		}
		if strings.Contains(u, "NAME") {
			t.Errorf("%s: usage template placeholder was not substituted", name)
		}
	}
}

func TestParsePeriodAndView(t *testing.T) {
	if _, ok := parsePeriod("week"); !ok {
		t.Error("week should parse as a span")
	}
	if _, ok := parsePeriod("projects"); ok {
		t.Error("projects is not a span")
	}
	if v, ok := parseView("project"); !ok || string(v) != "project" {
		t.Errorf("project -> %v %v", v, ok)
	}
	if v, ok := parseView("heatmap"); !ok || string(v) != "heat" {
		t.Errorf("heatmap should alias to heat, got %v", v)
	}
	if _, ok := parseView("nope"); ok {
		t.Error("nope is not a view")
	}
}

// testZone has no daylight saving, so an end-to-end run cannot straddle a
// clock change, and pinning it keeps day boundaries independent of TZ.
var testZone = time.FixedZone("UTC+8", 8*3600)

// pinTime makes testZone the local zone and now the clock until the test ends.
func pinTime(t *testing.T, now time.Time) {
	t.Helper()
	prevLocal, prevClock := time.Local, clock
	time.Local = testZone
	clock = func() time.Time { return now }
	t.Cleanup(func() { time.Local, clock = prevLocal, prevClock })
}

// isolate points every agent at a fresh, empty log tree, so a run sees only
// the logs the test writes, and returns the Claude projects directory.
func isolate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"CODEX_HOME", "GROK_HOME", "GEMINI_DIR", "OPENCODE_DATA"} {
		t.Setenv(key, filepath.Join(root, "none"))
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	return filepath.Join(root, "claude", "projects")
}

// response is one assistant reply in a Claude Code session log.
type response struct {
	cwd   string
	at    time.Time
	input int64
}

// claudeSession writes one Claude Code session log holding the responses.
// Claude records the working directory on every line, so one session can
// stand for any number of projects.
func claudeSession(t *testing.T, projects string, rs ...response) {
	t.Helper()
	dir := filepath.Join(projects, "-w")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i, r := range rs {
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"cwd":%q,"requestId":"req%d","message":{"id":"msg%d","model":"claude-test","usage":{"input_tokens":%d}}}`+"\n",
			r.at.Format(time.RFC3339), r.cwd, i, i, r.input)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// capture runs the command line and returns what it printed.
func capture(t *testing.T, args ...string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	read := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		read <- b
	}()
	runErr := run("tcount", args)
	w.Close()
	os.Stdout = prev
	out := <-read
	if runErr != nil {
		t.Fatalf("tcount %s: %v", strings.Join(args, " "), runErr)
	}
	return ansi.Strip(string(out))
}

func str(p *string) string {
	if p == nil {
		return "<none>"
	}
	return *p
}

// The review's case end to end: 10 tokens today and 100 three days earlier.
// `today hours --json` used to grid today while its rows and calls covered
// all history (heat.total 10, the rows 110), and every view counted every
// record ever loaded.
func TestRunTodayReportsDescribeToday(t *testing.T) {
	pinTime(t, time.Date(2026, 8, 25, 15, 0, 0, 0, testZone))
	projects := isolate(t)
	claudeSession(t, projects,
		response{"/w/today", time.Date(2026, 8, 25, 10, 0, 0, 0, testZone), 10},
		response{"/w/earlier", time.Date(2026, 8, 22, 10, 0, 0, 0, testZone), 100})

	for _, view := range []string{"time", "projects", "models", "hours"} {
		var rep ui.Report
		out := capture(t, "today", view, "--json", "--no-cache")
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("%s: %v\n%s", view, err, out)
		}
		var rows int64
		for _, r := range rep.Rows {
			rows += r.Total
		}
		if rows != 10 || rep.Calls != 1 || rep.Records != 1 {
			t.Errorf("%s: rows %d, calls %d, records %d; want 10, 1, 1", view, rows, rep.Calls, rep.Records)
		}
		if view == "hours" && (rep.Heat == nil || rep.Heat.Total != 10) {
			t.Errorf("%s: heat %+v, want a total of 10", view, rep.Heat)
		}
		if since, until := str(rep.Since), str(rep.Until); since != "2026-08-25" || until != "2026-08-25" || rep.UTCOffset != "+08:00" {
			t.Errorf("%s: window %s..%s at %q, want 2026-08-25..2026-08-25 at +08:00", view, since, until, rep.UTCOffset)
		}
	}
	if out := capture(t, "today", "hours", "--no-cache"); !strings.Contains(out, "total 10\n") {
		t.Errorf("the plain heatmap should total 10:\n%s", out)
	}
}

// `--plain` has to print every row, however many: it used to stop at a fixed
// screen height, printing 494 of 1,100 projects under a TOTAL of all 1,100.
func TestRunPlainPrintsEveryProject(t *testing.T) {
	pinTime(t, time.Date(2026, 8, 25, 15, 0, 0, 0, testZone))
	projects := isolate(t)
	const n = 1100
	rs := make([]response, n)
	for i := range rs {
		rs[i] = response{fmt.Sprintf("/w/p%04d", i), time.Date(2026, 8, 25, 10, 0, 0, 0, testZone), 2}
	}
	claudeSession(t, projects, rs...)
	out := capture(t, "projects", "--plain", "--no-cache")
	seen := map[string]bool{}
	children := 0
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) > 2 && strings.HasPrefix(f[1], "/w/p") {
			seen[f[1]] = true
		}
		if strings.Contains(ln, "└ claude") {
			children++
		}
	}
	if len(seen) != n || children != n {
		t.Errorf("printed %d projects and %d agent rows, want %d of each", len(seen), children, n)
	}
	if !regexp.MustCompile(`(?m)^ TOTAL\s+2,200\s`).MatchString(out) {
		t.Errorf("the TOTAL row should read 2,200")
	}
}
