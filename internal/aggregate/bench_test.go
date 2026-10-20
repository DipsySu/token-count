package aggregate

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"token-count/internal/model"
)

// benchZones are the zones the benchmarks run in: the one these logs are
// usually read in, and one with DST, where converting a timestamp to its
// local date is dearer because it can fall outside the cached zone period.
var benchZones = []struct{ name, zone string }{
	{"Taipei", "Asia/Taipei"},
	{"NewYork", "America/New_York"},
}

// benchNow is a fixed Monday afternoon, so every run sees the same windows.
func benchNow() time.Time {
	return time.Date(2026, 10, 19, 15, 4, 5, 0, time.Local)
}

// benchModels are a few models per agent, named the way their logs name them,
// vendor prefixes and date suffixes included.
var benchModels = map[string][]string{
	"claude":   {"claude-opus-5-5", "claude-fable-5", "claude-haiku-4-5-20251001"},
	"codex":    {"gpt-5.6-sol", "gpt-5.5-codex"},
	"grok":     {"grok-4.6", "xai/grok-code-fast-1"},
	"gemini":   {"gemini-3-pro", "gemini-3-flash"},
	"opencode": {"anthropic/claude-fable-5", "openai/gpt-5.6-sol"},
}

// benchRecords builds n records over the 400 days before now the way the
// collectors return them: sessions of calls from one agent and model in one
// of 30 projects under the home directory, each session in time order and
// starting at a random hour, the sessions in no order at all.
func benchRecords(now time.Time, n int) []model.Usage {
	rng := rand.New(rand.NewPCG(1, 2))
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/home/u"
	}
	out := make([]model.Usage, 0, n)
	for len(out) < n {
		agent := model.Agents[rng.IntN(len(model.Agents))]
		models := benchModels[agent]
		mdl := models[rng.IntN(len(models))]
		project := filepath.Join(home, "src", "project-"+strconv.Itoa(rng.IntN(30)))
		y, m, d := now.AddDate(0, 0, -rng.IntN(400)).Date()
		t := time.Date(y, m, d, 7+rng.IntN(16), rng.IntN(60), rng.IntN(60), rng.IntN(1e9), time.Local)
		for range 20 + rng.IntN(180) {
			if len(out) == n || !t.Before(now) {
				break
			}
			u, _ := model.Make(model.MakeOpts{
				Agent:      agent,
				Time:       t,
				Model:      mdl,
				Project:    project,
				Input:      1 + rng.Int64N(5_000),
				CacheRead:  rng.Int64N(60_000),
				CacheWrite: rng.Int64N(2_000),
				Output:     1 + rng.Int64N(3_000),
			})
			out = append(out, u)
			t = t.Add(time.Duration(5+rng.IntN(300)) * time.Second)
		}
	}
	return out
}

// The TUI calls Snapshot on every keystroke, so what it allocates must not
// grow with the archive.
func TestSnapshotAllocationsDoNotGrowWithRecords(t *testing.T) {
	now := benchNow()
	small, large := benchRecords(now, 1_000), benchRecords(now, 100_000)
	allocs := func(recs []model.Usage) float64 {
		return testing.AllocsPerRun(5, func() { Snapshot(recs, now) })
	}
	if a, b := allocs(small), allocs(large); a != b {
		t.Fatalf("%v allocations for 1k records, %v for 100k", a, b)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	Snapshot(large, now)
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 64<<10 {
		t.Fatalf("%d bytes allocated for 100k records", n)
	}
}

func BenchmarkSnapshot(b *testing.B) {
	for _, z := range benchZones {
		b.Run(z.name, func(b *testing.B) {
			setLocal(b, z.zone)
			now := benchNow()
			recs := benchRecords(now, 100_000)
			b.ReportAllocs()
			for b.Loop() {
				Snapshot(recs, now)
			}
		})
	}
}

// BenchmarkFilter scopes the records to the default window of two periods:
// day keeps the last 14 days, a small slice of the archive, and month keeps
// the last 12 months, nearly all of it.
func BenchmarkFilter(b *testing.B) {
	for _, z := range benchZones {
		b.Run(z.name, func(b *testing.B) {
			setLocal(b, z.zone)
			now := benchNow()
			recs := benchRecords(now, 100_000)
			for _, p := range []Period{PeriodDay, PeriodMonth} {
				since, until := PeriodBounds(p, now)
				b.Run(string(p), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						Filter(recs, since, until)
					}
				})
			}
		})
	}
}

// benchSpans are the spans the TUI groups, each over its default window; all
// covers every record and, as in the TUI, groups them by day.
var benchSpans = []Period{PeriodDay, PeriodWeek, PeriodMonth, PeriodAll}

// benchGroup runs one sub-benchmark per span on the records of 100k that the
// span's window keeps, which is what the TUI regroups on every keystroke.
func benchGroup(b *testing.B, group func(recs []model.Usage, period Period)) {
	now := benchNow()
	recs := benchRecords(now, 100_000)
	for _, p := range benchSpans {
		since, until := PeriodBounds(p, now)
		scoped := Filter(recs, since, until)
		by := p
		if p == PeriodAll {
			by = PeriodDay
		}
		b.Run(string(p), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				group(scoped, by)
			}
		})
	}
}

// GroupTime reads every record's local date, which costs more where the zone
// has DST, so it runs in both benchmark zones.
func BenchmarkGroupTime(b *testing.B) {
	for _, z := range benchZones {
		b.Run(z.name, func(b *testing.B) {
			setLocal(b, z.zone)
			benchGroup(b, func(recs []model.Usage, p Period) { GroupTime(recs, p, false) })
		})
	}
}

func BenchmarkGroupProject(b *testing.B) {
	benchGroup(b, func(recs []model.Usage, _ Period) { GroupProject(recs) })
}

func BenchmarkGroupModel(b *testing.B) {
	benchGroup(b, func(recs []model.Usage, _ Period) { GroupModel(recs) })
}

func BenchmarkGroupAgent(b *testing.B) {
	benchGroup(b, func(recs []model.Usage, _ Period) { GroupAgent(recs, false) })
}

// BenchmarkFilterAgent selects one agent of five, as the agent filter does,
// over every record.
func BenchmarkFilterAgent(b *testing.B) {
	recs := benchRecords(benchNow(), 100_000)
	b.ReportAllocs()
	for b.Loop() {
		FilterAgent(recs, "claude")
	}
}
