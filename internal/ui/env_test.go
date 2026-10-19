package ui

import (
	"os"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestMain detaches the suite from the terminal it runs in. `go test` gives a
// test binary a pipe for stdout, but a binary run by hand (go test -c, a
// debugger) writes to the terminal, and two things would then follow it. A
// text export takes the terminal's width, which moves the columns the export
// tests read. And the palette follows the terminal's background, while the
// colour tests assume a dark one. The background cannot be handed back to
// detection once set, so both are pinned for the whole run rather than per
// test. (Bubble Tea still asks the terminal for its background when the
// package loads, which can take seconds where nothing answers; pinning only
// stops the answer from mattering.)
func TestMain(m *testing.M) {
	lipgloss.SetHasDarkBackground(true)
	exportWidth = func() int { return 100 }
	os.Exit(m.Run())
}

// withExportWidth makes text exports w columns wide until the test ends.
func withExportWidth(t *testing.T, w int) {
	t.Helper()
	prev := exportWidth
	exportWidth = func() int { return w }
	t.Cleanup(func() { exportWidth = prev })
}
