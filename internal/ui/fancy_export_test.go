package ui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// withFancy forces the gradient and mascot paths on or off for one test.
//
// Two things have to be overridden. Our own gate is memoised from the
// environment, which a test binary has no terminal to inspect; and lipgloss
// independently downgrades colours to nothing when its profile says the output
// cannot show them, which is the case when stdout is a pipe.
func withFancy(t *testing.T, on bool) {
	t.Helper()
	fancyEnabled() // let the real detection settle before overriding it
	prevOn := fancyOn
	prevProfile := lipgloss.ColorProfile()
	fancyOn = on
	if on {
		lipgloss.SetColorProfile(termenv.TrueColor)
	} else {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
	t.Cleanup(func() {
		fancyOn = prevOn
		lipgloss.SetColorProfile(prevProfile)
	})
}
