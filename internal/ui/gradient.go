package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// ramp is a list of colour stops that a gradient interpolates between.
type ramp []colorful.Color

// mustRamp builds a ramp from hex stops. Any unparsable stop is skipped so a
// typo degrades to a shorter ramp rather than a panic at render time.
func mustRamp(hexes ...string) ramp {
	out := make(ramp, 0, len(hexes))
	for _, h := range hexes {
		if c, err := colorful.Hex(h); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// at samples the ramp at t in [0,1], blending in Luv so the perceived
// brightness moves evenly instead of muddying through the middle.
func (r ramp) at(t float64) colorful.Color {
	switch {
	case len(r) == 0:
		return colorful.Color{}
	case len(r) == 1 || t <= 0:
		return r[0]
	case t >= 1:
		return r[len(r)-1]
	}
	scaled := t * float64(len(r)-1)
	i := int(scaled)
	if i >= len(r)-1 {
		return r[len(r)-1]
	}
	return r[i].BlendLuv(r[i+1], scaled-float64(i)).Clamped()
}

// gradientText colours each rune along the ramp. Whitespace is emitted bare:
// colouring a space costs an escape sequence and shows nothing.
func gradientText(s string, r ramp) string {
	if s == "" || len(r) == 0 || !fancyEnabled() {
		return s
	}
	runes := []rune(s)
	// Count only the visible cells so the ramp spans the glyphs rather than
	// being stretched by leading or trailing padding.
	var visible int
	for _, ch := range runes {
		if ch != ' ' {
			visible++
		}
	}
	if visible == 0 {
		return s
	}

	var b strings.Builder
	b.Grow(len(s) * 12)
	seen := 0
	for _, ch := range runes {
		if ch == ' ' {
			b.WriteRune(ch)
			continue
		}
		t := 0.0
		if visible > 1 {
			t = float64(seen) / float64(visible-1)
		}
		b.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(r.at(t).Hex())).
			Render(string(ch)))
		seen++
	}
	return b.String()
}

// gradientBold is gradientText with every glyph bold, for headline figures.
func gradientBold(s string, r ramp) string {
	if s == "" || len(r) == 0 || !fancyEnabled() {
		return lipgloss.NewStyle().Bold(true).Render(s)
	}
	runes := []rune(s)
	var visible int
	for _, ch := range runes {
		if ch != ' ' {
			visible++
		}
	}
	if visible == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) * 14)
	seen := 0
	for _, ch := range runes {
		if ch == ' ' {
			b.WriteRune(ch)
			continue
		}
		t := 0.0
		if visible > 1 {
			t = float64(seen) / float64(visible-1)
		}
		b.WriteString(lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(r.at(t).Hex())).
			Render(string(ch)))
		seen++
	}
	return b.String()
}

// gradientBar draws a bar whose colour ramp is sampled across the *whole*
// column, not just the filled part. A short bar therefore only reaches the
// cool end of the ramp while a full one runs all the way to the hot end, so
// hue and length carry the same reading instead of contradicting each other.
func gradientBar(filled string, trackWidth int, r ramp, track lipgloss.Style) string {
	runes := []rune(filled)
	total := len(runes) + trackWidth
	body := filled
	if fancyEnabled() && len(r) > 0 && total > 0 {
		var b strings.Builder
		b.Grow(len(filled) * 12)
		for i, ch := range runes {
			t := 0.0
			if total > 1 {
				t = float64(i) / float64(total-1)
			}
			b.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color(r.at(t).Hex())).
				Render(string(ch)))
		}
		body = b.String()
	} else if body != "" {
		body = lipgloss.NewStyle().Foreground(colFill).Render(body)
	}
	if trackWidth > 0 {
		body += track.Render(strings.Repeat("\u254c", trackWidth))
	}
	return body
}

// gradientByValue colours a whole string by where its value sits in a range,
// used where each cell is one datum rather than part of a sweep.
func gradientByValue(s string, t float64, r ramp) string {
	if s == "" || len(r) == 0 || !fancyEnabled() {
		return s
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.at(clamp01(t)).Hex())).
		Render(s)
}

func clamp01(t float64) float64 {
	switch {
	case t < 0:
		return 0
	case t > 1:
		return 1
	default:
		return t
	}
}

// padVisible pads a styled string to width using its visible cell count, so
// escape sequences never throw the column alignment off.
func padVisible(s string, width int, right bool) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	pad := strings.Repeat(" ", width-w)
	if right {
		return pad + s
	}
	return s + pad
}
