package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"token-count/internal/aggregate"
	"token-count/internal/model"
	"token-count/internal/source"
)

// logTree points every agent at a fresh, empty log tree, so that a load sees
// only the logs a test writes, and returns OpenCode's message directory.
// OpenCode keeps one message per JSON file, so a test can break any one of
// them, and a broken file is a read failure whoever runs the suite, root
// included.
func logTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GROK_HOME", "GEMINI_DIR"} {
		t.Setenv(key, filepath.Join(root, "none"))
	}
	t.Setenv("OPENCODE_DATA", filepath.Join(root, "opencode"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	dir := filepath.Join(root, "opencode", "storage", "message", "ses_1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeMessage writes one OpenCode assistant message with the given id, an
// hour before testNow.
func writeMessage(t *testing.T, dir, file, id string) {
	t.Helper()
	body := fmt.Sprintf(`{"id":%q,"role":"assistant","modelID":"m1","path":{"cwd":"/w/p"},`+
		`"tokens":{"input":100,"output":10},"time":{"created":%d}}`, id, testNow.Add(-time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeBroken writes a message file cut off after its first byte, as one
// caught while being rewritten is.
func writeBroken(t *testing.T, dir, file string) string {
	t.Helper()
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// loaded is a TUI that has run its load against the log tree, as the program
// runs it, sized w x h.
func loaded(t *testing.T, w, h int) Model {
	t.Helper()
	m := New(Options{Period: aggregate.PeriodDay, Agents: model.Agents, NoCache: true, Name: "tcount", Now: testClock})
	m.width, m.height = w, h
	return send(m, m.load()())
}

// When some logs could not be read, the totals on screen are short, and the
// TUI says so where it put the dedup notice, which the warning outranks: the
// notice only explains why the totals are smaller than a naive count. A
// rescan replaces what the last load reported. The notice also used to name
// Claude Code as the only agent whose history is replayed, which Codex logs
// copied between machines contradict.
func TestTUIWarnsThatTotalsAreALowerBound(t *testing.T) {
	withFancy(t, true)
	pinLocal(t)
	dir := logTree(t)
	// The same message in two files is one replayed record.
	writeMessage(t, dir, "msg_a.json", "msg_1")
	writeMessage(t, dir, "msg_b.json", "msg_1")
	writeBroken(t, dir, "msg_c.json")
	writeBroken(t, dir, "msg_d.json")

	const warning = "⚠ 2 files could not be read completely — totals are a lower bound (tcount --json lists them)"
	m := loaded(t, 120, 30)
	body := stripANSI(m.View())
	if !strings.Contains(body, warning) {
		t.Fatalf("no warning that the totals are short:\n%s", body)
	}
	if strings.Contains(body, "deduped") {
		t.Errorf("the dedup notice is drawn beside the warning that outranks it:\n%s", body)
	}
	if want := newStyles().Warn.Render(" " + warning); !strings.Contains(m.View(), want) {
		t.Errorf("the warning is not drawn in the warning style")
	}

	// Once the files read cleanly, the next scan's notice is the dedup one.
	writeMessage(t, dir, "msg_c.json", "msg_3")
	writeMessage(t, dir, "msg_d.json", "msg_4")
	m = send(m, m.load()())
	body = stripANSI(m.View())
	if strings.Contains(body, "⚠") {
		t.Errorf("a rescan that read everything still warns:\n%s", body)
	}
	if !strings.Contains(body, "deduped 1 repeated record") {
		t.Errorf("no dedup notice after a clean rescan:\n%s", body)
	}
	if strings.Contains(body, "Claude Code") {
		t.Errorf("the dedup notice blames Claude Code alone:\n%s", body)
	}
}

// issuesFor makes n issues, one per broken OpenCode file.
func issuesFor(n int) []source.Issue {
	out := make([]source.Issue, n)
	for i := range out {
		out[i] = source.Issue{
			Agent: "opencode",
			Path:  fmt.Sprintf("/w/logs/msg_%d.json", i),
			Err:   "malformed JSON: unexpected end of JSON input",
		}
	}
	return out
}

// --plain names the files it could not read, up to five, and says how many
// more there are, after the table they make short.
func TestPlainWarnsAboutIncompleteReads(t *testing.T) {
	pinLocal(t)
	recs := sample(testNow)
	opts := Options{Period: aggregate.PeriodDay, Now: testClock}
	for _, n := range []int{0, 1, 5, 7} {
		var buf bytes.Buffer
		WritePlain(&buf, opts, BuildExport(recs, opts), source.Stats{Issues: issuesFor(n)})
		out := stripANSI(buf.String())
		if n == 0 {
			if strings.Contains(out, "⚠") {
				t.Errorf("a clean load warns:\n%s", out)
			}
			continue
		}
		head := fmt.Sprintf("⚠ %d file", n)
		at := strings.Index(out, head)
		if at < 0 || at < strings.Index(out, "TOTAL") {
			t.Fatalf("%d issues: no warning after the table:\n%s", n, out)
		}
		if !strings.Contains(out, "could not be read completely — totals are a lower bound") {
			t.Errorf("%d issues: the warning does not say the totals are short:\n%s", n, out)
		}
		for i := 0; i < n; i++ {
			line := fmt.Sprintf("opencode  /w/logs/msg_%d.json: malformed JSON: unexpected end of JSON input", i)
			if listed := strings.Contains(out, line); listed != (i < 5) {
				t.Errorf("%d issues: issue %d listed %v, want %v:\n%s", n, i, listed, i < 5, out)
			}
		}
		more := fmt.Sprintf("… and %d more (see --json)", n-5)
		if strings.Contains(out, more) != (n > 5) {
			t.Errorf("%d issues: the count of the rest is wrong:\n%s", n, out)
		}
	}
}

// --json says whether the totals are complete, always, and lists the files
// that make them short when there are any.
func TestJSONReportsCompleteness(t *testing.T) {
	pinLocal(t)
	recs := sample(testNow)
	opts := Options{Period: aggregate.PeriodDay, Now: testClock}
	decode := func(stats source.Stats) map[string]any {
		t.Helper()
		var buf bytes.Buffer
		if err := WriteJSON(&buf, opts, BuildExport(recs, opts), stats); err != nil {
			t.Fatal(err)
		}
		var rep map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
			t.Fatalf("decoding %s: %v", buf.String(), err)
		}
		return rep
	}

	rep := decode(source.Stats{})
	if rep["complete"] != true {
		t.Errorf("a clean load reports complete = %v, want true", rep["complete"])
	}
	if _, ok := rep["issues"]; ok {
		t.Errorf("a clean load lists issues: %v", rep["issues"])
	}

	rep = decode(source.Stats{Issues: issuesFor(2)})
	if rep["complete"] != false {
		t.Errorf("an incomplete load reports complete = %v, want false", rep["complete"])
	}
	issues, _ := rep["issues"].([]any)
	if len(issues) != 2 {
		t.Fatalf("issues %v, want two", rep["issues"])
	}
	first, _ := issues[0].(map[string]any)
	if first["agent"] != "opencode" || first["path"] != "/w/logs/msg_0.json" ||
		first["error"] != "malformed JSON: unexpected end of JSON input" {
		t.Errorf("first issue %v", first)
	}
}
