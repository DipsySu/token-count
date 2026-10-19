package ui

import (
	"os"
	"strings"
	"testing"
)

// TestDumpLoading prints the loading screen for visual inspection. It also
// asserts the frames actually differ, which is the whole point of animating.
func TestDumpLoading(t *testing.T) {
	withFancy(t, true)
	m := New(Options{Version: "0.3.0"})
	m.width, m.height = 92, 12

	seen := map[string]bool{}
	for _, frame := range []int{0, 3, 6, 9, 12, 15, 18, 21} {
		m.spin = frame
		body := strings.Join(m.loadingBody(m.width, 5, newStyles()), "\n")
		seen[stripANSI(body)] = true
		if os.Getenv("DUMP") != "" {
			t.Logf("frame %d:\n%s\n", frame, body)
		}
	}
	if len(seen) < 3 {
		t.Fatalf("loading animation only produced %d distinct frames", len(seen))
	}
}

// TestDumpEmpty prints the empty state in each of its variants.
func TestDumpEmpty(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Model)
	}{
		{"no data", func(m *Model) {}},
		{"search miss", func(m *Model) { m.search = "zzz" }},
		{"agent filter", func(m *Model) { m.agent = "gemini" }},
	}
	withFancy(t, true)
	for _, c := range cases {
		m := newTestModel(t, 100, 24)
		m.records = nil
		c.setup(&m)
		m.recompute()
		body := strings.Join(m.emptyState(100, newStyles()), "\n")
		if strings.TrimSpace(stripANSI(body)) == "" {
			t.Fatalf("%s: empty state rendered nothing", c.name)
		}
		if os.Getenv("DUMP") != "" {
			t.Logf("%s:\n%s\n", c.name, body)
		}
	}
}

// TestDumpCat prints every mascot pose for visual inspection.
func TestDumpCat(t *testing.T) {
	if os.Getenv("DUMP") == "" {
		t.Skip("set DUMP=1 to print the poses")
	}
	for _, m := range []catMood{moodSleepy, moodCalm, moodBusy, moodWired} {
		t.Logf("=== %s\n%s", m.word(), strings.Join(catArt(m), "\n"))
	}
	for i := 0; i < 5; i++ {
		t.Logf("=== blink %d\n%s", i, strings.Join(catBlink(i), "\n"))
	}
}
