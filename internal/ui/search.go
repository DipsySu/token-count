package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"

	"token-count/internal/model"
)

// unknown is the label the tables give a record that has no model or no
// project, so it is also what a search has to match for one.
const unknown = "(unknown)"

// searchIndex is the text a search is matched against, worked out once per
// load. A search matches the agent, the model and the project, each as stored
// and as the table shows it, so that whatever is on screen can be typed back:
// a model by its short name, a project under the home directory as ~/...
//
// Records repeat a few hundred combinations of the three many thousands of
// times over, so each combination's text is lowercased once, and a record
// refers to it by number. A search then tests each combination once, rather
// than lowercasing five fields of every record on every keystroke.
type searchIndex struct {
	ids map[combo]int32
	hay []string // per combination, every form of its fields, lowercased
}

// combo is the part of a record that a search reads.
type combo struct{ agent, model, project string }

func newSearchIndex(recs []model.Usage) *searchIndex {
	ix := &searchIndex{ids: map[combo]int32{}}
	for i := range recs {
		ix.id(&recs[i])
	}
	return ix
}

// id is the number of the record's combination, which is added to the index
// the first time it is seen.
func (ix *searchIndex) id(r *model.Usage) int32 {
	k := combo{r.Agent, r.Model, r.Project}
	if id, ok := ix.ids[k]; ok {
		return id
	}
	id := int32(len(ix.hay))
	ix.ids[k] = id
	ix.hay = append(ix.hay, k.haystack())
	return id
}

// haystack joins the forms of the fields that a search may type. NUL
// separates them, and since the search box drops control characters, a term
// can never match across the end of one field and the start of the next.
func (k combo) haystack() string {
	mdl, project := k.model, k.project
	if mdl == "" {
		mdl = unknown
	}
	if project == "" {
		project = unknown
	}
	return strings.ToLower(strings.Join([]string{
		k.agent, mdl, model.ShortModel(mdl), project, model.ShortProject(project),
	}, "\x00"))
}

// matching reports, for each combination, whether its text contains q, which
// must already be in the form searchTerm gives.
func (ix *searchIndex) matching(q string) []bool {
	out := make([]bool, len(ix.hay))
	for i, h := range ix.hay {
		out[i] = strings.Contains(h, q)
	}
	return out
}

// searchTerm is the form of a search that is matched: trimmed, so that a
// pasted line's spaces do not count, and lowercased, so that case never does.
func searchTerm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// newSearchInput is the search box. The component takes care of what a text
// field has to get right and a key switch did not: multi-rune input from an
// IME, bracketed pastes, a cursor, and deleting a whole character.
func newSearchInput() textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	// A blinking cursor needs a timer running for as long as the box is
	// open, redrawing the screen twice a second to say nothing new.
	in.Cursor.SetMode(cursor.CursorStatic)
	// ctrl+v would read the clipboard through xclip, xsel or wl-paste, which
	// a terminal does not need: its own paste arrives as text.
	in.KeyMap.Paste.SetEnabled(false)
	return in
}

// pasted prepares the text of a bracketed paste for the search box, which
// turns every line break into a space. A Windows line end is two characters,
// and would otherwise leave two spaces where the lines join.
func pasted(rs []rune) []rune {
	out := make([]rune, 0, len(rs))
	for i, r := range rs {
		if r == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
			continue
		}
		out = append(out, r)
	}
	return out
}

// fitInput draws the search box exactly width cells wide. The component
// scrolls its text as a viewport that only moves when the cursor leaves it,
// so after the space for it shrinks it goes on drawing a stretch that was cut
// for the old width until then. Drawing a copy whose viewport is rebuilt for
// this width keeps the box inside the space it is given.
func fitInput(in textinput.Model, width int) string {
	if width < 2 {
		return ""
	}
	pos, val := in.Position(), in.Value()
	in.Width = width - 1 // the cursor takes a cell after the last character
	in.Reset()
	in.SetValue(val)
	in.SetCursor(pos)
	return in.View()
}
