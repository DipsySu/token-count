package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var Agents = []string{"claude", "codex", "grok", "gemini", "opencode"}

// Usage is one billable API exchange. Calls is how many upstream requests the
// record stands for: agents that log per response report 1, agents that log a
// whole turn report the turn's request count, so Calls means the same thing
// everywhere.
type Usage struct {
	Agent      string    `json:"agent"`
	Time       time.Time `json:"ts"`
	Model      string    `json:"model"`
	Project    string    `json:"project,omitempty"`
	Input      int64     `json:"input"`
	CacheRead  int64     `json:"cache_read"`
	CacheWrite int64     `json:"cache_write"`
	Output     int64     `json:"output"`
	Reasoning  int64     `json:"reasoning"`
	Calls      int64     `json:"calls"`
	// Dedup identifies the upstream response. Agents that replay history into
	// new session files (Claude Code on resume/fork) write the same exchange
	// many times; records sharing a key are counted once.
	Dedup  string `json:"-"`
	Source string `json:"source,omitempty"`
}

func (u Usage) Total() int64 {
	return u.Input + u.CacheRead + u.CacheWrite + u.Output
}

func (u Usage) Date() time.Time {
	y, m, d := u.Time.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

type Totals struct {
	Input      int64 `json:"input"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
	Calls      int64 `json:"calls"`
	Records    int64 `json:"records"`
}

func (t Totals) Total() int64 {
	return t.Input + t.CacheRead + t.CacheWrite + t.Output
}

// Fresh is the volume that actually had to be read fresh, i.e. everything the
// cache did not serve.
func (t Totals) Fresh() int64 { return t.Input + t.CacheWrite + t.Output }

// CacheHit is the share of prompt volume served from cache, 0..1.
func (t Totals) CacheHit() float64 {
	prompt := t.Input + t.CacheRead + t.CacheWrite
	if prompt <= 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(prompt)
}

// PerCall is the average total volume of one upstream request.
func (t Totals) PerCall() int64 {
	if t.Calls <= 0 {
		return 0
	}
	return t.Total() / t.Calls
}

// ReasonShare is reasoning as a fraction of output, 0..1.
func (t Totals) ReasonShare() float64 {
	if t.Output <= 0 {
		return 0
	}
	return float64(t.Reasoning) / float64(t.Output)
}

func (t *Totals) AddUsage(u Usage) {
	t.Input += u.Input
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
	t.Output += u.Output
	t.Reasoning += u.Reasoning
	t.Calls += callsOf(u)
	t.Records++
}

func (t *Totals) Merge(o Totals) {
	t.Input += o.Input
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.Output += o.Output
	t.Reasoning += o.Reasoning
	t.Calls += o.Calls
	t.Records += o.Records
}

func callsOf(u Usage) int64 {
	if u.Calls > 0 {
		return u.Calls
	}
	return 1
}

// Bucket is one row of a grouped report. Children carry the breakdown that a
// row expands into; Depth is the indent level used when rows are flattened.
type Bucket struct {
	Key      string
	Label    string
	Agent    string
	Model    string
	Project  string
	Totals   Totals
	Children []Bucket
	Depth    int
}

type SourceInfo struct {
	Agent  string `json:"agent"`
	Root   string `json:"root"`
	Files  int    `json:"files"`
	Exists bool   `json:"exists"`
	Note   string `json:"note"`
}

type MakeOpts struct {
	Agent                   string
	Time                    time.Time
	Model                   string
	Project                 string
	Input                   int64
	CacheRead               int64
	CacheWrite              int64
	Output                  int64
	Reasoning               int64
	Calls                   int64
	InputIncludesCache      bool
	OutputIncludesReasoning bool
	Dedup                   string
	Source                  string
}

func Make(o MakeOpts) (Usage, bool) {
	if o.Time.IsZero() {
		return Usage{}, false
	}
	in := max64(0, o.Input)
	cr := max64(0, o.CacheRead)
	cw := max64(0, o.CacheWrite)
	out := max64(0, o.Output)
	rs := max64(0, o.Reasoning)
	if o.InputIncludesCache {
		if in >= cr {
			in -= cr
		} else {
			// The provider reported a cache read larger than the input it is
			// supposedly part of. Trust the cache figure and drop the input
			// rather than double-counting the overlap.
			in = 0
		}
	}
	if !o.OutputIncludesReasoning {
		out += rs
	}
	if in+cr+cw+out == 0 {
		return Usage{}, false
	}
	calls := o.Calls
	if calls <= 0 {
		calls = 1
	}
	return Usage{
		Agent:      o.Agent,
		Time:       o.Time.In(time.Local),
		Model:      o.Model,
		Project:    o.Project,
		Input:      in,
		CacheRead:  cr,
		CacheWrite: cw,
		Output:     out,
		Reasoning:  rs,
		Calls:      calls,
		Dedup:      o.Dedup,
		Source:     o.Source,
	}, true
}

// ShortProject abbreviates the home directory to ~ and otherwise leaves the
// path intact. Dropping leading segments here would merge genuinely different
// directories -- /home/me/x/proj and /Users/me/x/proj are not the same place --
// so any further shortening is left to the renderer, which elides from the
// left and keeps the distinguishing tail.
func ShortProject(p string) string {
	if p == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + filepath.ToSlash(p[len(home):])
	}
	return p
}

// ShortModel drops vendor prefixes and date suffixes so model names fit a
// column without losing the part that distinguishes them.
func ShortModel(m string) string {
	if m == "" {
		return ""
	}
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	// claude-haiku-4-5-20251001 -> claude-haiku-4-5
	if i := strings.LastIndex(m, "-"); i > 0 && len(m)-i == 9 {
		if _, err := strconv.Atoi(m[i+1:]); err == nil {
			m = m[:i]
		}
	}
	return m
}

func ParseTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		if x.IsZero() {
			return time.Time{}, false
		}
		return x.In(time.Local), true
	case string:
		return parseTimeString(x)
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return time.Time{}, false
		}
		return parseUnix(f)
	case float64:
		return parseUnix(x)
	case int64:
		return parseUnix(float64(x))
	case int:
		return parseUnix(float64(x))
	case json.RawMessage:
		return ParseRawTime(x)
	default:
		return time.Time{}, false
	}
}

func ParseRawTime(raw json.RawMessage) (time.Time, bool) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return parseTimeString(s)
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return parseUnix(f)
	}
	return time.Time{}, false
}

func parseTimeString(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil && !strings.ContainsAny(s, "-:T") {
		return parseUnix(n)
	}
	zoned := hasZone(s)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		t, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		if !zoned {
			// A naive stamp carries no offset; time.Parse defaults it to UTC,
			// which is what these agents actually write.
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
		}
		return t.In(time.Local), true
	}
	return time.Time{}, false
}

func hasZone(s string) bool {
	if strings.HasSuffix(s, "Z") || strings.HasSuffix(s, "z") {
		return true
	}
	// An offset shows up after the time part, e.g. 2026-08-25T10:00:00+08:00.
	if i := strings.IndexByte(s, 'T'); i >= 0 {
		return strings.ContainsAny(s[i:], "+-")
	}
	return false
}

func parseUnix(sec float64) (time.Time, bool) {
	if sec <= 0 {
		return time.Time{}, false
	}
	switch {
	case sec > 1e17: // nanoseconds
		sec /= 1e9
	case sec > 1e14: // microseconds
		sec /= 1e6
	case sec > 1e11: // milliseconds
		sec /= 1e3
	}
	whole := int64(sec)
	nsec := int64((sec - float64(whole)) * 1e9)
	return time.Unix(whole, nsec).In(time.Local), true
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
