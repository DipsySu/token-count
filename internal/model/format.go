package model

import (
	"math"
	"strconv"
	"strings"
)

func FmtTokens(n int64) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	if n < 10_000 {
		return sign + Comma(n)
	}
	type unit struct {
		s   string
		div float64
	}
	for _, u := range []unit{
		{"T", 1e12},
		{"B", 1e9},
		{"M", 1e6},
		{"k", 1e3},
	} {
		if float64(n) >= u.div {
			v := float64(n) / u.div
			var body string
			switch {
			case v >= 100:
				body = strconv.FormatFloat(v, 'f', 0, 64) + u.s
			case v >= 10:
				body = strconv.FormatFloat(v, 'f', 1, 64) + u.s
			default:
				body = strconv.FormatFloat(v, 'f', 2, 64) + u.s
			}
			return sign + body
		}
	}
	return sign + Comma(n)
}

func Comma(n int64) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return sign + s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre == 0 {
		pre = 3
	}
	b.WriteString(s[:pre])
	for i := pre; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return sign + b.String()
}

// FmtPct renders a 0..1 ratio as a percentage, keeping a decimal only while
// the value is small enough for one to matter.
func FmtPct(f float64) string {
	v := f * 100
	switch {
	case v <= 0:
		return "0%"
	case v < 10:
		return strconv.FormatFloat(v, 'f', 1, 64) + "%"
	default:
		return strconv.FormatFloat(v, 'f', 0, 64) + "%"
	}
}

// Delta describes a change against a previous period.
type Delta struct {
	Pct   float64
	Up    bool
	Known bool
}

func Change(now, prev int64) Delta {
	if prev <= 0 {
		if now <= 0 {
			return Delta{}
		}
		return Delta{Known: false, Up: true}
	}
	d := (float64(now) - float64(prev)) / float64(prev)
	return Delta{Pct: d, Up: d >= 0, Known: true}
}

func (d Delta) String() string {
	if !d.Known {
		return "—"
	}
	arrow := "↓"
	v := -d.Pct
	if d.Up {
		arrow = "↑"
		v = d.Pct
	}
	return arrow + FmtPct(v)
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// Sparkline draws values oldest-to-newest. The scale starts at zero so bar
// heights stay comparable between two sparklines drawn from the same data.
func Sparkline(vals []int64) string {
	if len(vals) == 0 {
		return ""
	}
	var maxV int64
	for _, v := range vals {
		if v > maxV {
			maxV = v
		}
	}
	var b strings.Builder
	for _, v := range vals {
		if maxV <= 0 || v <= 0 {
			b.WriteRune('▁')
			continue
		}
		idx := int(float64(len(sparkRunes)-1) * float64(v) / float64(maxV))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkRunes) {
			idx = len(sparkRunes) - 1
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

var eighths = []rune(" ▏▎▍▌▋▊▉█")

// Bar renders n/max across width cells using partial blocks, so short rows
// stay distinguishable instead of all collapsing to one full cell.
func Bar(n, max int64, width int) string {
	if width <= 0 {
		return ""
	}
	if max <= 0 || n <= 0 {
		return strings.Repeat(" ", width)
	}
	ratio := float64(n) / float64(max)
	if ratio > 1 {
		ratio = 1
	}
	units := int(ratio*float64(width)*8 + 0.5)
	if units < 1 {
		units = 1
	}
	full := units / 8
	rem := units % 8
	if full > width {
		full, rem = width, 0
	}
	var b strings.Builder
	b.WriteString(strings.Repeat("█", full))
	pad := width - full
	if rem > 0 && pad > 0 {
		b.WriteRune(eighths[rem])
		pad--
	}
	b.WriteString(strings.Repeat(" ", pad))
	return b.String()
}

var heatRunes = []rune(" ·░▒▓█")

// HeatLevel places a value on the heatmap scale, 0..1.
//
// Usage per hour-of-week is heavily skewed: one peak can be hundreds of times
// the quietest active hour. Normalising the log against the *range* of active
// cells rather than against zero spreads the real data across the whole scale,
// so both the glyph and the colour stay informative. Dividing by log(max)
// alone would crush every active hour into the top fifth of the scale;
// dividing linearly would crush them all into the bottom.
//
// lo is the smallest non-zero cell and hi the largest.
func HeatLevel(n, lo, hi int64) float64 {
	if n <= 0 || hi <= 0 {
		return 0
	}
	if n >= hi {
		return 1
	}
	if lo <= 0 || lo >= hi {
		return math.Log(float64(n)+1) / math.Log(float64(hi)+1)
	}
	span := math.Log(float64(hi)) - math.Log(float64(lo))
	if span <= 0 {
		return 1
	}
	t := (math.Log(float64(n)) - math.Log(float64(lo))) / span
	switch {
	case t < 0:
		return 0
	case t > 1:
		return 1
	default:
		return t
	}
}

// HeatCell is the glyph for a cell, drawn from the same scale as its colour so
// the two can never disagree.
func HeatCell(n, lo, hi int64) string {
	if n <= 0 {
		return string(heatRunes[0])
	}
	step := int(float64(len(heatRunes)-1)*HeatLevel(n, lo, hi) + 0.5)
	if step < 1 {
		step = 1
	}
	if step >= len(heatRunes) {
		step = len(heatRunes) - 1
	}
	return string(heatRunes[step])
}
