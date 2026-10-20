package ui

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"token-count/internal/aggregate"
	"token-count/internal/model"
)

// benchRecords is n records spread over the 400 days before now, across every
// agent, a dozen models and forty projects, half of them under the home
// directory, so that searches and groupings meet the variety a real archive
// has. The generator is seeded: every run measures the same data.
func benchRecords(n int, now time.Time) []model.Usage {
	rng := rand.New(rand.NewSource(1))
	home, _ := os.UserHomeDir()
	models := []string{
		"claude-opus-4-1-20250805", "claude-sonnet-4-5-20250929", "claude-haiku-4-5-20251001",
		"gpt-5-codex", "gpt-5", "gpt-5-mini",
		"grok-code-fast-1", "grok-4",
		"gemini-2.5-pro", "gemini-2.5-flash",
		"anthropic/claude-sonnet-4-5", "openai/gpt-5",
	}
	projects := make([]string, 40)
	for i := range projects {
		if i%2 == 0 {
			projects[i] = filepath.Join(home, "work", fmt.Sprintf("project-%02d", i))
		} else {
			projects[i] = fmt.Sprintf("/srv/build/service-%02d", i)
		}
	}
	out := make([]model.Usage, 0, n)
	for len(out) < n {
		at := now.Add(-time.Duration(rng.Int63n(int64(400 * 24 * time.Hour))))
		u, ok := model.Make(model.MakeOpts{
			Agent:     model.Agents[rng.Intn(len(model.Agents))],
			Time:      at,
			Model:     models[rng.Intn(len(models))],
			Project:   projects[rng.Intn(len(projects))],
			Input:     rng.Int63n(5_000),
			CacheRead: rng.Int63n(200_000),
			Output:    rng.Int63n(3_000),
			Reasoning: rng.Int63n(500),
		})
		if ok {
			out = append(out, u)
		}
	}
	return out
}

// benchModel is a TUI that has loaded n records, sized 120x40 on the pinned
// clock, showing all history by day.
func benchModel(b *testing.B, n int) Model {
	b.Helper()
	pinLocal(b)
	m := New(Options{Period: aggregate.PeriodAll, Agents: model.Agents, Version: "bench", Now: testClock})
	m.width, m.height = 120, 40
	return send(m, loadedMsg{records: benchRecords(n, testNow)})
}

// BenchmarkSearchKeystroke is the cost of one more letter in the search box
// over 100,000 records: everything the screen recomputes before it can be
// drawn again. Each iteration types the same letter into the same state.
func BenchmarkSearchKeystroke(b *testing.B) {
	m := send(benchModel(b, 100_000), runes("/"), runes("c"), runes("l"))
	letter := runes("a")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next, _ := m.Update(letter)
		_ = next
	}
}

// BenchmarkView is the cost of drawing one full 120x40 frame in colour.
func BenchmarkView(b *testing.B) {
	withFancy(b, true)
	m := benchModel(b, 100_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}
