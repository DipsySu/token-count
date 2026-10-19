package ui

import (
	"os"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

const (
	termenvTrueColor = termenv.TrueColor
	termenvANSI256   = termenv.ANSI256
)

// fancy controls the gradient and mascot flourishes. They are off when the
// terminal cannot show them honestly: NO_COLOR, a dumb TERM, or a colour
// profile with too few steps to interpolate across.
var (
	fancyOnce sync.Once
	fancyOn   bool
)

func fancyEnabled() bool {
	fancyOnce.Do(func() { fancyOn = detectFancy() })
	return fancyOn
}

// ForceColorIfRequested honours CLICOLOR_FORCE by lifting the automatic
// downgrade lipgloss applies when stdout is not a terminal, so styling
// survives a pipe (`tcount --plain | less -R`, capturing a screenshot).
// NO_COLOR still wins. Call this once, before rendering anything.
func ForceColorIfRequested() {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TCOUNT_NO_FANCY") != "" {
		return
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
}

func detectFancy() bool {
	// NO_COLOR wins over everything, including a force request.
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TCOUNT_NO_FANCY") != "" {
		return false
	}
	// CLICOLOR_FORCE is the usual way to ask for colour through a pipe, which
	// is what tools like `less -R` and screenshot capture need.
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	if t := os.Getenv("TERM"); t == "" || t == "dumb" {
		return false
	}
	switch lipgloss.ColorProfile() {
	case termenvTrueColor, termenvANSI256:
		return true
	default:
		return false
	}
}

// dark reports whether the palette should target a dark background. Gradients
// need concrete colours, so the adaptive pairs used elsewhere do not apply.
func dark() bool { return lipgloss.HasDarkBackground() }

// Ramps used across the UI. Each is built per call so a background change
// between renders is picked up.
func rampTitle() ramp {
	if dark() {
		return mustRamp("#7dd3fc", "#818cf8", "#c084fc")
	}
	return mustRamp("#0369a1", "#4338ca", "#7e22ce")
}

// rampVolume runs cool to hot: a long bar is also a warm bar.
func rampVolume() ramp {
	if dark() {
		return mustRamp("#22d3ee", "#38bdf8", "#818cf8", "#e879f9", "#fb7185")
	}
	return mustRamp("#0891b2", "#0284c7", "#4f46e5", "#c026d3", "#e11d48")
}

// rampHeat is the heatmap ramp: quiet hours stay cool, peaks glow.
func rampHeat() ramp {
	if dark() {
		return mustRamp("#1e3a5f", "#0e7490", "#0ea5e9", "#a78bfa", "#f472b6", "#fbbf24")
	}
	return mustRamp("#dbeafe", "#7dd3fc", "#0284c7", "#6366f1", "#c026d3", "#b45309")
}

// rampSpark keeps trend lines legible without competing with the bars.
func rampSpark() ramp {
	if dark() {
		return mustRamp("#0e7490", "#38bdf8", "#a5b4fc")
	}
	return mustRamp("#0e7490", "#0284c7", "#4f46e5")
}

func rampCat() ramp {
	if dark() {
		return mustRamp("#fbbf24", "#fb923c", "#f472b6")
	}
	return mustRamp("#b45309", "#c2410c", "#be185d")
}

var (
	colText    = lipgloss.AdaptiveColor{Light: "#1c1917", Dark: "#e4e4e7"}
	colDim     = lipgloss.AdaptiveColor{Light: "#57534e", Dark: "#a1a1aa"}
	colMuted   = lipgloss.AdaptiveColor{Light: "#78716c", Dark: "#71717a"}
	colFaint   = lipgloss.AdaptiveColor{Light: "#a8a29e", Dark: "#52525b"}
	colBorder  = lipgloss.AdaptiveColor{Light: "#d6d3d1", Dark: "#3f3f46"}
	colAccent  = lipgloss.AdaptiveColor{Light: "#0369a1", Dark: "#7dd3fc"}
	colAccent2 = lipgloss.AdaptiveColor{Light: "#a16207", Dark: "#fbbf24"}
	colFill    = lipgloss.AdaptiveColor{Light: "#0ea5e9", Dark: "#38bdf8"}
	colTrack   = lipgloss.AdaptiveColor{Light: "#e7e5e4", Dark: "#27272a"}
	colSelBg   = lipgloss.AdaptiveColor{Light: "#dbeafe", Dark: "#1e3a5f"}
	colOk      = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#86efac"}
	colWarn    = lipgloss.AdaptiveColor{Light: "#a16207", Dark: "#fcd34d"}
	colErr     = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#f87171"}
	colUp      = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#fbbf24"}
	colDown    = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#86efac"}

	agentColors = map[string]lipgloss.AdaptiveColor{
		"claude":   {Light: "#c2410c", Dark: "#fb923c"},
		"codex":    {Light: "#047857", Dark: "#34d399"},
		"grok":     {Light: "#6d28d9", Dark: "#c4b5fd"},
		"gemini":   {Light: "#1d4ed8", Dark: "#60a5fa"},
		"opencode": {Light: "#9d174d", Dark: "#f9a8d4"},
	}
)

// Styles holds every style the views use. It is built once per render so that
// callers pass one named value around instead of restating the set.
type Styles struct {
	Title    lipgloss.Style
	Version  lipgloss.Style
	Muted    lipgloss.Style
	Dim      lipgloss.Style
	Faint    lipgloss.Style
	Text     lipgloss.Style
	Tab      lipgloss.Style
	TabOn    lipgloss.Style
	Header   lipgloss.Style
	Footer   lipgloss.Style
	Key      lipgloss.Style
	Help     lipgloss.Style
	CardNum  lipgloss.Style
	Total    lipgloss.Style
	Track    lipgloss.Style
	Spark    lipgloss.Style
	Up       lipgloss.Style
	Down     lipgloss.Style
	Ok       lipgloss.Style
	Warn     lipgloss.Style
	Err      lipgloss.Style
	Sel      lipgloss.Style
	Rule     lipgloss.Style
	Child    lipgloss.Style
	Notice   lipgloss.Style
	HeatOn   lipgloss.Style
	HeatOff  lipgloss.Style
	HeatAxis lipgloss.Style
}

func newStyles() Styles {
	return Styles{
		Title:   lipgloss.NewStyle().Bold(true).Foreground(colAccent),
		Version: lipgloss.NewStyle().Foreground(colFaint),
		Muted:   lipgloss.NewStyle().Foreground(colMuted),
		Dim:     lipgloss.NewStyle().Foreground(colDim),
		Faint:   lipgloss.NewStyle().Foreground(colFaint),
		Text:    lipgloss.NewStyle().Foreground(colText),
		Tab:     lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1),
		TabOn: lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#f0f9ff", Dark: "#082f49"}).
			Background(colAccent).Padding(0, 1),
		Header:   lipgloss.NewStyle().Foreground(colFaint).Bold(true),
		Footer:   lipgloss.NewStyle().Foreground(colMuted),
		Key:      lipgloss.NewStyle().Foreground(colAccent).Bold(true),
		Help:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(1, 3).Foreground(colText),
		CardNum:  lipgloss.NewStyle().Bold(true).Foreground(colText),
		Total:    lipgloss.NewStyle().Bold(true).Foreground(colAccent2),
		Track:    lipgloss.NewStyle().Foreground(colTrack),
		Spark:    lipgloss.NewStyle().Foreground(colFill),
		Up:       lipgloss.NewStyle().Foreground(colUp),
		Down:     lipgloss.NewStyle().Foreground(colDown),
		Ok:       lipgloss.NewStyle().Foreground(colOk),
		Warn:     lipgloss.NewStyle().Foreground(colWarn),
		Err:      lipgloss.NewStyle().Foreground(colErr),
		Sel:      lipgloss.NewStyle().Background(colSelBg),
		Rule:     lipgloss.NewStyle().Foreground(colBorder),
		Child:    lipgloss.NewStyle().Foreground(colDim),
		Notice:   lipgloss.NewStyle().Foreground(colFaint).Italic(true),
		HeatOn:   lipgloss.NewStyle().Foreground(colFill),
		HeatOff:  lipgloss.NewStyle().Foreground(colTrack),
		HeatAxis: lipgloss.NewStyle().Foreground(colFaint),
	}
}

func agentStyle(name string) lipgloss.Style {
	if c, ok := agentColors[name]; ok {
		return lipgloss.NewStyle().Foreground(c)
	}
	return lipgloss.NewStyle().Foreground(colDim)
}
