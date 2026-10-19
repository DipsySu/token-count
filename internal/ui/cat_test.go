package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// catMouth appears in every pose and nowhere else in the UI, which makes it a
// reliable way to assert the mascot is or is not on screen.
const catMouth = "ω"

// catGlyphs is the set the mascot is allowed to draw with: characters present
// in DejaVu Sans Mono, Ubuntu Mono and Ubuntu Sans Mono alike. Rounder shapes
// like U+25CF read better as eyes but are missing from Ubuntu Mono, where they
// render as tofu -- far worse than a plain glyph.
const catGlyphs = ` /\()"^><zZOo@,._-=|~'` + "`" + `•ω·‹›─│┌┐└┘█░▒▓`

func allPoses() map[string][]string {
	poses := map[string][]string{
		"curled": catCurled(),
	}
	for _, m := range []catMood{moodSleepy, moodCalm, moodBusy, moodWired} {
		poses["mood:"+m.word()] = catArt(m)
	}
	for i := 0; i < 8; i++ {
		poses["blink:"+string(rune('0'+i))] = catBlink(i)
	}
	return poses
}

// Every line must measure exactly catWidth, or the summary cards beside the
// mascot shift out of alignment. It is measured the way the layout measures,
// not by runewidth's default, which follows the locale and would count the
// East Asian ambiguous glyphs as two cells under a CJK one.
func TestCatLinesAreExactlyCatWidth(t *testing.T) {
	for name, art := range allPoses() {
		if len(art) != 4 {
			t.Fatalf("%s: %d lines, want 4", name, len(art))
		}
		for i, ln := range art {
			if got := ansi.StringWidth(ln); got != catWidth {
				t.Errorf("%s line %d measures %d, want %d: %q", name, i, got, catWidth, ln)
			}
		}
	}
}

// Several mascot glyphs are East Asian Ambiguous, so a CJK locale measures them
// as two cells. What has to hold is not a fixed width but self-consistency:
// under whichever rule is in force, the four lines come out equal, because that
// is what the cards beside the mascot depend on. (The bars and rules elsewhere
// in the UI use ambiguous glyphs too, so swapping the mascot's would buy
// nothing.)
func TestCatLinesStayEqualUnderEitherWidthRule(t *testing.T) {
	rules := map[string]*runewidth.Condition{
		"default": runewidth.NewCondition(),
		"cjk":     runewidth.NewCondition(),
	}
	rules["cjk"].EastAsianWidth = true

	rawPoses := map[string][]string{
		"napping": catLines(moodSleepy.eyes(), " ", true),
		"content": catLines(moodCalm.eyes(), "~", false),
		"busy":    catLines(moodBusy.eyes(), "~", false),
		"wired":   catLines(moodWired.eyes(), "~", false),
		"blink":   catLines("^ω^", "~", false),
	}

	for ruleName, c := range rules {
		for pose, raw := range rawPoses {
			// The widest line sets the frame, exactly as padding does.
			want := 0
			for _, ln := range raw {
				if w := c.StringWidth(ln); w > want {
					want = w
				}
			}
			for i, ln := range raw {
				padded := ln + strings.Repeat(" ", want-c.StringWidth(ln))
				if got := c.StringWidth(padded); got != want {
					t.Errorf("%s/%s line %d padded to %d, want %d", ruleName, pose, i, got, want)
				}
			}
			// catWidth has to leave room for the widest line under both rules,
			// or padding silently truncates the mascot.
			if ruleName == "default" && want > catWidth {
				t.Errorf("%s needs %d cells but catWidth is %d", pose, want, catWidth)
			}
		}
	}
}

// Only font-safe glyphs, or the mascot renders as tofu boxes on a common
// terminal font.
func TestCatUsesOnlyFontSafeGlyphs(t *testing.T) {
	for name, art := range allPoses() {
		for _, ln := range art {
			for _, r := range ln {
				if !strings.ContainsRune(catGlyphs, r) {
					t.Errorf("%s: %q (U+%04X) is not in the font-safe set", name, r, r)
				}
			}
		}
	}
}

// The moods have to be visibly different, which is the only reason to grade
// them in the first place.
func TestCatMoodsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, m := range []catMood{moodSleepy, moodCalm, moodBusy, moodWired} {
		art := strings.Join(catArt(m), "\n")
		if prev, dup := seen[art]; dup {
			t.Fatalf("%s renders identically to %s", m.word(), prev)
		}
		seen[art] = m.word()
	}
	// The eyes are where a reader looks, so that is where they must differ.
	eyes := map[string]bool{}
	for _, m := range []catMood{moodSleepy, moodCalm, moodBusy, moodWired} {
		if eyes[m.eyes()] {
			t.Fatalf("%s reuses the eyes %q", m.word(), m.eyes())
		}
		eyes[m.eyes()] = true
	}
	// Dozing is the one pose that gets a sleep mark and tucks its tail.
	if !strings.Contains(strings.Join(catArt(moodSleepy), ""), "z") {
		t.Error("the napping cat should show a sleep mark")
	}
	if strings.Contains(strings.Join(catArt(moodSleepy), ""), "~") {
		t.Error("the napping cat should have its tail tucked")
	}
	if !strings.Contains(strings.Join(catArt(moodCalm), ""), "~") {
		t.Error("an awake cat should have a visible tail")
	}
}

func TestMoodForGradesAgainstPeak(t *testing.T) {
	const peak = 100
	cases := []struct {
		today int64
		want  catMood
	}{
		{0, moodSleepy},
		{5, moodSleepy},
		{30, moodCalm},
		{60, moodBusy},
		{90, moodWired},
		{peak, moodWired},
		{peak * 2, moodWired}, // beyond the old peak is still just "wired"
	}
	for _, c := range cases {
		if got := moodFor(c.today, peak); got != c.want {
			t.Errorf("moodFor(%d, %d) = %s, want %s", c.today, peak, got.word(), c.want.word())
		}
	}
	// With no history to compare against, do not claim the day is heavy.
	if got := moodFor(5_000_000, 0); got != moodCalm {
		t.Errorf("no peak: got %s", got.word())
	}
}

// The blink cycle has to actually animate.
func TestCatBlinkAnimates(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		seen[strings.Join(catBlink(i), "\n")] = true
	}
	if len(seen) < 3 {
		t.Fatalf("blink cycle produced only %d distinct frames", len(seen))
	}
}
