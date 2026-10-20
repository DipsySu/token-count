package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"token-count/internal/aggregate"
	"token-count/internal/model"
	"token-count/internal/ui"
)

var version = "0.4.0"

// defaultName is what to call ourselves when argv[0] is unusable.
const defaultName = "tcount"

// clock decides what "today" is. Tests pin it so that a run's answer does not
// depend on the day it happens to execute.
var clock = time.Now

func main() {
	if err := run(invokedName(os.Args[0]), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// invokedName is the name the binary was actually called by. Aliases are
// installed as symlinks, and reporting the invoked name means every alias is
// first-class: the help text tells you how to run the thing you just ran,
// rather than a name you may never have typed.
func invokedName(argv0 string) string {
	name := strings.TrimSuffix(filepath.Base(argv0), ".exe")
	switch name {
	case "", ".", "..", string(filepath.Separator):
		return defaultName
	}
	return name
}

func run(name string, args []string) error {
	ui.ForceColorIfRequested()

	period := aggregate.PeriodToday
	view := aggregate.ViewTime
	command := ""

	// Leading bare words select the span, the grouping, or a subcommand, in
	// any order: `tcount week projects` and `tcount projects week` are the same
	// request. The two are independent axes, exactly as in the TUI.
	namedSpan := false
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		word := args[0]
		if p, ok := parsePeriod(word); ok {
			period, namedSpan = p, true
		} else if v, ok := parseView(word); ok {
			view = v
		} else if word == "agents" || word == "help" || word == "version" {
			command = word
		} else {
			break // not ours; the flag parser reports it by name
		}
		args = args[1:]
	}
	// A ranking with no span named covers everything, which is what a ranking
	// is usually wanted for. Naming a span always wins.
	if view != aggregate.ViewTime && !namedSpan {
		period = aggregate.PeriodAll
	}

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage(name)) }
	agentFlag := fs.String("agent", "", "comma-separated agents: claude,codex,grok,gemini,opencode")
	fs.StringVar(agentFlag, "a", "", "short for --agent")
	byModel := fs.Bool("by-model", false, "split rows by model")
	fs.BoolVar(byModel, "m", false, "short for --by-model")
	sinceS := fs.String("since", "", "start date YYYY-MM-DD")
	untilS := fs.String("until", "", "end date YYYY-MM-DD")
	asJSON := fs.Bool("json", false, "machine-readable JSON")
	plain := fs.Bool("plain", false, "print a table instead of the TUI")
	noCache := fs.Bool("no-cache", false, "neither read nor write ~/.cache/token-count")
	noCat := fs.Bool("no-cat", false, "hide the mascot and gradients")
	showVersion := fs.Bool("version", false, "print version")
	fs.BoolVar(showVersion, "V", false, "print version")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q — run '%s help'", rest[0], name)
	}

	switch {
	case *showVersion || command == "version":
		fmt.Printf("%s %s\n", name, version)
		return nil
	case command == "help":
		fmt.Print(usage(name))
		return nil
	case command == "agents":
		return ui.WriteSources(os.Stdout, name, *asJSON)
	}

	agents, err := parseAgents(*agentFlag)
	if err != nil {
		return err
	}
	since, err := parseDate(*sinceS)
	if err != nil {
		return err
	}
	until, err := parseDate(*untilS)
	if err != nil {
		return err
	}
	if since != nil && until != nil && until.Before(*since) {
		return fmt.Errorf("--until %s is before --since %s",
			until.Format("2006-01-02"), since.Format("2006-01-02"))
	}

	opts := ui.Options{
		Period:  period,
		View:    view,
		Agents:  agents,
		ByModel: *byModel,
		Since:   since,
		Until:   until,
		NoCache: *noCache,
		NoCat:   *noCat,
		Name:    name,
		Version: version,
		Now:     clock,
	}

	if ui.ShouldTUI(*plain, *asJSON) {
		return ui.RunTUI(opts)
	}

	// A cache that cannot be saved slows the next run down; this one has
	// read everything it needs, so it still answers.
	records, stats, err := ui.Load(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: warning: could not save the cache: %v\n", name, err)
	}
	// Every output format is drawn from one export, built against one reading
	// of the clock, so none of them can disagree about the window it
	// describes. The heatmap prints only the records in that window, so it
	// skips the rows and the summary cards the other formats need.
	switch {
	case *asJSON:
		return ui.WriteJSON(os.Stdout, opts, ui.BuildExport(records, opts), stats)
	case view == aggregate.ViewHeat:
		ui.WriteHeat(os.Stdout, opts, ui.ScopeExport(records, opts), stats)
	default:
		ui.WritePlain(os.Stdout, opts, ui.BuildExport(records, opts), stats)
	}
	return nil
}

func parsePeriod(word string) (aggregate.Period, bool) {
	switch word {
	case "today", "day", "week", "month", "year", "all":
		return aggregate.Period(word), true
	default:
		return "", false
	}
}

func parseView(word string) (aggregate.View, bool) {
	switch word {
	case "time":
		return aggregate.ViewTime, true
	case "projects", "project":
		return aggregate.ViewProject, true
	case "models", "model":
		return aggregate.ViewModel, true
	case "hours", "heat", "heatmap":
		return aggregate.ViewHeat, true
	default:
		return "", false
	}
}

func parseAgents(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return append([]string{}, model.Agents...), nil
	}
	known := map[string]struct{}{}
	for _, a := range model.Agents {
		known[a] = struct{}{}
	}
	var out []string
	seen := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("unknown agent %q (%s)", name, strings.Join(model.Agents, ","))
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return append([]string{}, model.Agents...), nil
	}
	return out, nil
}

func parseDate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return nil, fmt.Errorf("date must be YYYY-MM-DD: %s", s)
	}
	return &t, nil
}

func usage(name string) string {
	return strings.ReplaceAll(`NAME — local AI agent token usage

USAGE
  NAME [span] [view] [options]

Span and view are independent, and may be given in either order:
  NAME week projects   ==   NAME projects week

SPAN
  today     today                                        (default)
  day       the last 14 days
  week      the last 8 ISO weeks (Monday start)
  month     the last 12 calendar months
  year      calendar years
  all       everything on record

VIEW
  time      grouped by span, expandable by agent          (default)
  projects  rank working directories
  models    rank models
  hours     hour-of-week heatmap
A view named without a span covers all history.

OTHER
  agents    list local data sources

OPTIONS
  -a, --agent string   claude,codex,grok,gemini,opencode
  -m, --by-model       split rows by model
      --since date     YYYY-MM-DD
      --until date     YYYY-MM-DD
      --json           machine-readable output
      --plain          table instead of TUI
      --no-cache       neither read nor write ~/.cache/token-count
      --no-cat         hide the mascot
  -V, --version

KEYS (TUI)
  Two axes, two keys: ← → walks the span, tab walks the view.

  1-5 0 / ← →  span              tab / shift-tab   view
  t p M H      time / project / model / hours
  enter        expand a row      E C       expand / collapse all
  a A          agent filter      m         split by model
  s            cycle sort        /         search
  d            detail panel      c         mascot
  r            rescan            ?         help
  q            quit              ctrl+c    quit, from anywhere

  / searches agent, model and project as you type, in any case, by the
  names the table shows too: ~/ paths, short model names. CJK input and
  pasted text work. enter keeps the search, esc clears it.

SCREEN
  The line under the tabs is the table's scope: its first and last dates,
  how many days that is, the UTC offset, then any filters (agent, by model,
  sort, search). The OVERVIEW cards below it ignore the span and the search;
  each delta compares like for like, e.g. today with yesterday up to the
  same time. The last row, RANGE TOTAL, sums the table. The screen needs at
  least 30x10, and moves on to the new day at midnight.

OUTPUT
  --plain prints every row, under a header naming the span, view, dates,
  UTC offset and filters. --json reports the effective local dates as
  since/until with utc_offset; records, calls and the rows cover that
  window, and heat.total equals the rows. deduped counts the whole load.

  Logs (files or directories) that cannot be read completely make the
  totals a lower bound: the TUI says so in a warning line, --plain lists up
  to five after the table, and --json sets "complete": false and lists them
  all under "issues".

ENVIRONMENT
  NO_COLOR             disable all colour
  TCOUNT_NO_FANCY      disable gradients and the mascot only
  CLICOLOR_FORCE       keep colour when piping (for less -R)

NOTES
  totals = input + cache read + cache write + output
  calls  = upstream API requests, not conversation turns
  cache is processed volume, not billed cost

  Agents repeat records: resumed and forked sessions replay history, logs
  get copied, and Codex publishes some usage twice. Repeats are counted
  once, the same way on every run. Run with --json to see how many.
`, "NAME", name)
}
