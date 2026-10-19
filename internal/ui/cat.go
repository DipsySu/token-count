package ui

import "token-count/internal/model"

// The mascot is drawn on a fixed grid so it can sit beside the summary cards
// without shifting their columns.
//
// The silhouette is deliberately closed: the head's parentheses, the body's
// bars and the base's slashes all sit on the same two columns, so the left and
// right edges read as one continuous outline. An earlier version drew the body
// one column wider than the head, and the result looked like a face floating
// above an unrelated box.
const catWidth = 11

// Only glyphs present in DejaVu Sans Mono, Ubuntu Mono and Ubuntu Sans Mono are
// used here. Rounder alternatives read better -- a filled circle makes a nicer
// eye than a bullet -- but U+25CF and friends are missing from Ubuntu Mono and
// render as tofu, which looks far worse than a plain glyph.
const (
	catEars = `  /\_/\`  // sits on top of the head, inside its width
	catBody = ` |  ░  |` // bars continue the head's outline downwards
	catBase = ` \_____/` // tucked paws close the shape
)

// catMood is the expression, chosen from how hard the day is going.
type catMood int

const (
	moodSleepy catMood = iota // nothing much recorded
	moodCalm
	moodBusy
	moodWired // a lot of tokens burned today
)

// moodFor grades today against the busiest day on record, so the expression
// still means something on a light week as well as a heavy one.
func moodFor(today, peak int64) catMood {
	if today <= 0 {
		return moodSleepy
	}
	if peak <= 0 {
		return moodCalm
	}
	switch share := float64(today) / float64(peak); {
	case share < 0.15:
		return moodSleepy
	case share < 0.50:
		return moodCalm
	case share < 0.85:
		return moodBusy
	default:
		return moodWired
	}
}

// eyes carries the whole expression. The mouth stays as omega throughout, so
// the four moods differ only where a reader is already looking.
func (m catMood) eyes() string {
	switch m {
	case moodSleepy:
		return "-ω-"
	case moodBusy:
		return "OωO"
	case moodWired:
		return "@ω@"
	default:
		return "•ω•"
	}
}

func (m catMood) word() string {
	switch m {
	case moodSleepy:
		return "napping"
	case moodBusy:
		return "busy"
	case moodWired:
		return "wired"
	default:
		return "content"
	}
}

// catLines is the mascot before padding: the fixed silhouette plus the parts
// that vary -- the eyes, the tail, and the sleep mark.
//
// Padding is applied separately because the width rule is not fixed. Several of
// these glyphs are East Asian Ambiguous, so a CJK locale measures them as two
// cells; padding by measured width keeps the four lines equal under either
// rule, which is what the summary cards beside the mascot depend on.
func catLines(eyes, tail string, dozing bool) []string {
	face := " ( " + eyes + " )"
	if dozing {
		face += " z"
	}
	return []string{catEars, face, catBody + tail, catBase}
}

func catFrame(eyes, tail string, dozing bool) []string {
	raw := catLines(eyes, tail, dozing)
	out := make([]string, 0, len(raw))
	for _, ln := range raw {
		out = append(out, padRight(ln, catWidth))
	}
	return out
}

func catArt(mood catMood) []string {
	tail := "~"
	if mood == moodSleepy {
		tail = " " // tucked under while dozing
	}
	return catFrame(mood.eyes(), tail, mood == moodSleepy)
}

// catBlink is the mascot mid-blink with a flicking tail, for the loading screen
// where there is nothing else to look at.
func catBlink(frame int) []string {
	eyes := []string{
		"•ω•", "•ω•", "•ω•",
		"-ω-", "^ω^",
		"•ω•", "•ω•", "-ω-",
	}
	tails := []string{"~", " ", "~", " "}
	return catFrame(eyes[frame%len(eyes)], tails[frame%len(tails)], false)
}

// catCurled is the resting cat for the empty state. It is the napping pose:
// one mascot to maintain, and dozing is the right mood for "nothing here".
func catCurled() []string { return catArt(moodSleepy) }

// catStrip applies a vertical gradient down the mascot's lines.
func catStrip(lines []string) []string {
	r := rampCat()
	out := make([]string, 0, len(lines))
	for i, ln := range lines {
		t := 0.0
		if len(lines) > 1 {
			t = float64(i) / float64(len(lines)-1)
		}
		out = append(out, gradientByValue(ln, t, r))
	}
	return out
}

// catMoodChip is the short mood readout shown among the status chips.
func catMoodChip(mood catMood, today int64) string {
	return mood.word() + " " + model.FmtTokens(today)
}
