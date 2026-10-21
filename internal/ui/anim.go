package ui

import (
	"math"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// barAnimDur is how long a view-switch bar ease-in lasts. Numbers show at
// their full value from the first frame; only the bar length moves.
const barAnimDur = 300 * time.Millisecond

// animTick is the frame rate while a bar ease-in (or the scan spinner) runs.
// Matching the existing spinner keeps one ticker path for both.
const animTick = 90 * time.Millisecond

// noAnimEnv is set when the operator asked to keep fancy colours but skip the
// new scan progress and bar ease-in animations.
func noAnimEnv() bool {
	v := os.Getenv("TCOUNT_NO_ANIM")
	return v != "" && v != "0"
}

// animEnabled reports whether the new scan progress bar and bar ease-in may
// run. They need an interactive fancy terminal: --json and pipes never reach
// the TUI, NO_COLOR / TCOUNT_NO_FANCY turn fancy off, and --no-anim /
// TCOUNT_NO_ANIM is an additive switch that leaves gradients alone but stops
// these animations and their ticks.
func (m Model) animEnabled() bool {
	if m.opts.NoAnim || noAnimEnv() {
		return false
	}
	return fancyEnabled()
}

// frameNow is the instant the current frame is drawn for. Tests pin frameAt
// so golden dumps are deterministic; the live TUI leaves it zero and reads
// the wall clock.
func (m Model) frameNow() time.Time {
	if !m.frameAt.IsZero() {
		return m.frameAt
	}
	return time.Now()
}

// barFrac is how far the table bars have eased in, 0..1. Settled (1) is also
// what a non-animated render uses, so a finished ease matches --no-anim.
func (m Model) barFrac() float64 {
	if !m.animEnabled() || m.barAnimAt.IsZero() {
		return 1
	}
	elapsed := m.frameNow().Sub(m.barAnimAt)
	if elapsed >= barAnimDur {
		return 1
	}
	if elapsed <= 0 {
		return 0
	}
	return easeOutCubic(float64(elapsed) / float64(barAnimDur))
}

// barsAnimating is true while a view-switch ease-in still has frames to draw.
func (m Model) barsAnimating() bool {
	if !m.animEnabled() || m.barAnimAt.IsZero() {
		return false
	}
	return m.frameNow().Sub(m.barAnimAt) < barAnimDur
}

func easeOutCubic(t float64) float64 {
	t = clamp01(t)
	u := 1 - t
	return 1 - u*u*u
}

// startBarAnim begins a ~300ms bar ease-in when animations are on. Calling it
// from reset means a view or span change eases in; filter refreshes do not.
func (m *Model) startBarAnim() {
	if !m.animEnabled() {
		m.barAnimAt = time.Time{}
		return
	}
	at := m.frameNow()
	if at.IsZero() {
		at = time.Now()
	}
	m.barAnimAt = at
}

// scanFrac is files-read / files-total across every agent that has reported.
func (m Model) scanFrac() float64 {
	if m.scanTotal <= 0 {
		return 0
	}
	return clamp01(float64(m.scanDone) / float64(m.scanTotal))
}

// applyProgress folds one agent's progress report into the running totals.
// Agents scan in parallel, so each agent keeps its own done/total and the
// bar shows the sum.
func (m *Model) applyProgress(agent string, done, total int) {
	if m.scanProg == nil {
		m.scanProg = map[string][2]int{}
	}
	m.scanProg[agent] = [2]int{done, total}
	m.scanAgent = agent
	var d, t int
	for _, v := range m.scanProg {
		d += v[0]
		t += v[1]
	}
	m.scanDone, m.scanTotal = d, t
}

// scanProgressLine is the gradient bar + "agent  done/total" used on the scan
// screen when animations are on.
func (m Model) scanProgressLine(width int, st Styles) string {
	barW := width - 4
	if barW > 48 {
		barW = 48
	}
	if barW < 8 {
		barW = max(width-2, 0)
	}
	frac := m.scanFrac()
	bar := progressTrack(frac, barW, st)
	label := "scanning"
	if m.scanAgent != "" {
		label = m.scanAgent
	}
	meta := label
	if m.scanTotal > 0 {
		meta = label + "  " + itoa(m.scanDone) + "/" + itoa(m.scanTotal)
	}
	line := "  " + bar
	if lipgloss.Width(line)+1+lipgloss.Width(meta) <= width {
		line += " " + st.Faint.Render(meta)
	}
	return truncate(line, width)
}

// progressTrack draws a gradient-filled bar of width cells at frac full.
func progressTrack(frac float64, width int, st Styles) string {
	if width <= 0 {
		return ""
	}
	frac = clamp01(frac)
	filled := int(math.Round(frac * float64(width)))
	if filled > width {
		filled = width
	}
	body := strings.Repeat("█", filled)
	return gradientBar(body, width-filled, rampVolume(), st.Track)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
