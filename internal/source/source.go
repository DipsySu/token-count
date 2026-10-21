package source

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"token-count/internal/cache"
	"token-count/internal/model"
)

// ProgressFunc reports scan progress for one agent. files is how many of that
// agent's session files have been read so far; records is that agent's file
// total (0 when the agent has no roots or no matching files). It is called
// concurrently across agents, and may be called many times per agent.
type ProgressFunc func(agent string, files, records int)

// Stats describes what a collection pass saw before and after deduplication,
// and which files it could not read completely.
type Stats struct {
	Raw           int
	Kept          int
	Dropped       int
	DroppedTokens int64
	// Issues lists the files and directories whose usage is missing from the
	// totals, in whole or in part, sorted by agent and then path. It is empty
	// when everything was read through.
	Issues []Issue
}

// Issue names a file that was not read completely, or a directory that could
// not be listed: whatever usage the unread part holds is missing from the
// totals. Err says what went wrong, without repeating the path.
type Issue struct {
	Agent string `json:"agent"`
	Path  string `json:"path"`
	Err   string `json:"error"`
}

// Collect parses every session file for the requested agents and returns the
// deduplicated records, in time order.
func Collect(agents []string, c *cache.Cache, progress ProgressFunc) []model.Usage {
	recs, _ := CollectStats(agents, c, progress)
	return recs
}

// CollectStats is Collect plus a report of how much replayed volume was
// dropped, and of the files that could not be read completely. Deduplication
// has to happen here rather than inside a parser because agents replay the
// same exchange across different files.
func CollectStats(agents []string, c *cache.Cache, progress ProgressFunc) ([]model.Usage, Stats) {
	var (
		mu     sync.Mutex
		out    []model.Usage
		issues []Issue
		wg     sync.WaitGroup
	)
	for _, agent := range agents {
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs, bad := collectAgent(agent, c, progress)
			if len(recs) == 0 && len(bad) == 0 {
				return
			}
			mu.Lock()
			out = append(out, recs...)
			issues = append(issues, bad...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	sortIssues(issues)
	stats := Stats{Raw: len(out), Issues: issues}
	kept := dedup(out, &stats)
	stats.Kept = len(kept)
	return kept, stats
}

// sortIssues orders issues by agent and then path. Files are parsed in
// parallel, so without it the order would change from run to run.
func sortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Agent != issues[j].Agent {
			return issues[i].Agent < issues[j].Agent
		}
		return issues[i].Path < issues[j].Path
	})
}

// dedup collapses records that describe the same upstream response into one,
// and returns the survivors in time order.
//
// Two different things produce repeats, and they need the same treatment.
// Claude Code replays earlier messages into a new log every time a session is
// resumed or forked. It also writes several snapshots of a single streaming
// response: the early ones carry a partial output count (often 1 or 3 tokens)
// and the final one carries the real figure. Keeping whichever record was seen
// first therefore threw away most of the output tokens -- 26% of them across a
// real history -- even though the totals looked nearly right, because output is
// small next to cached input. The largest snapshot is the complete one, so the
// largest record survives.
//
// Records of one response often tie on size, though, and then differ in what
// says where the response is counted. Claude Code writes a snapshot for every
// block of a response, each at its own time, so ties within a log differ in
// time; and a copy in another session's log takes that session's working
// directory, so ties across logs differ in project and file. Collection runs
// in parallel, so taking whichever copy came first made those depend on the
// run: one project's call count moved by one between runs over the same logs.
// The survivor is therefore chosen by content alone, in this order:
//
//   - the largest record, which is the complete snapshot;
//   - the original rather than a copy that a log holds of a call made in
//     another session (see copyMark), since the original says where the
//     call was made;
//   - the record logged first, which is when the response was first seen;
//   - and, for a choice that never depends on the order the records came
//     in, the first in a fixed order of every other field (compareRecords).
//
// The survivors are returned in time order, for the same reason: buckets that
// tie on size and label are listed in the order their first record arrived.
//
// Records without a key are kept as-is: their format offers nothing to match
// on.
func dedup(recs []model.Usage, stats *Stats) []model.Usage {
	type kept struct {
		at     int // where the survivor is in out
		copied bool
	}
	best := make(map[string]kept, len(recs))
	out := make([]model.Usage, 0, len(recs))
	for _, r := range recs {
		key, copied := strings.CutPrefix(r.Dedup, copyMark)
		r.Dedup = key
		if key == "" {
			out = append(out, r)
			continue
		}
		k, seen := best[key]
		if !seen {
			best[key] = kept{len(out), copied}
			out = append(out, r)
			continue
		}
		superseded := r
		if survives(r, copied, out[k.at], k.copied) {
			superseded = out[k.at]
			out[k.at] = r
			best[key] = kept{k.at, copied}
		}
		if stats != nil {
			stats.Dropped++
			stats.DroppedTokens += superseded.Total()
		}
	}
	slices.SortFunc(out, compareRecords)
	return out
}

// copyMark heads the key of a record that a log holds as a copy of a call
// made in another session. Claude Code copies a session's conversation into
// the log of a session it starts from it, rewriting each line's session and
// working directory, so a copy matches the original's key but not where the
// call was made. dedup matches a copy with its original by the rest of the
// key, prefers the original, and returns every survivor under the plain key:
// the mark never leaves this package, other than in the cache.
const copyMark = "copy|"

// survives reports whether a, a copy if aCopy, wins over b, a copy if bCopy,
// as the one record kept of a response both describe.
func survives(a model.Usage, aCopy bool, b model.Usage, bCopy bool) bool {
	if a.Total() != b.Total() {
		return a.Total() > b.Total()
	}
	if aCopy != bCopy {
		return bCopy
	}
	return compareRecords(a, b) < 0
}

// compareRecords orders records by time, and records of the same time by
// every other field, so that only identical records compare equal.
func compareRecords(a, b model.Usage) int {
	return cmp.Or(
		a.Time.Compare(b.Time),
		strings.Compare(a.Agent, b.Agent),
		strings.Compare(a.Dedup, b.Dedup),
		strings.Compare(a.Source, b.Source),
		strings.Compare(a.Project, b.Project),
		strings.Compare(a.Model, b.Model),
		cmp.Compare(a.Calls, b.Calls),
		cmp.Compare(a.Input, b.Input),
		cmp.Compare(a.CacheRead, b.CacheRead),
		cmp.Compare(a.CacheWrite, b.CacheWrite),
		cmp.Compare(a.Output, b.Output),
		cmp.Compare(a.Reasoning, b.Reasoning),
	)
}

func collectAgent(agent string, c *cache.Cache, progress ProgressFunc) ([]model.Usage, []Issue) {
	roots, match, parse := agentSpec(agent)
	if len(roots) == 0 {
		if progress != nil {
			progress(agent, 0, 0)
		}
		return nil, nil
	}
	files, unread := listFiles(roots, match)
	if agent == "codex" {
		files = codexPreferred(roots, files)
	}
	if agent == "gemini" {
		files = geminiPreferred(files)
	}
	recs, issues := mapFiles(agent, files, c, parse, progress)
	for _, d := range unread {
		issues = append(issues, Issue{Agent: agent, Path: d.path, Err: describe(d.path, d.err)})
	}
	return recs, issues
}

type matcher func(path string, d fs.DirEntry) bool

// parser reads one session file. Alongside whatever records it could read, it
// returns an error when the file was not read completely.
type parser func(path string) ([]model.Usage, error)

// agentSpec returns every directory to scan for an agent. There can be more
// than one: a machine may hold a migrated snapshot alongside the live data, and
// Claude Code itself reads from two locations.
func agentSpec(agent string) (roots []string, match matcher, parse parser) {
	switch agent {
	case "claude":
		return sub(claudeRoots(), "projects"), suffix(".jsonl"), parseClaude
	case "codex":
		return codexSessionDirs(codexRoots()), codexRollout, parseCodex
	case "grok":
		return sub(grokRoots(), "sessions"), named("updates.jsonl"), parseGrok
	case "gemini":
		return geminiRoots(), geminiChat, parseGemini
	case "opencode":
		return sub(opencodeRoots(), "storage", "message"), suffix(".json"), parseOpenCode
	default:
		return nil, nil, nil
	}
}

// codexSessionDirs lists, for every Codex home in turn, its live sessions/
// and then archived_sessions/, where Codex moves rollouts it archives. The
// order matters to codexPreferred.
func codexSessionDirs(homes []string) []string {
	var out []string
	for _, h := range homes {
		out = append(out, filepath.Join(h, "sessions"), filepath.Join(h, "archived_sessions"))
	}
	return out
}

// codexPreferred keeps one file per rollout of each Codex home. A rollout
// found at the same path under both sessions/ and archived_sessions/ is read
// from whichever dir comes first in roots, the live sessions/, as ccusage
// does, whatever its encoding. Within a dir, x.jsonl sorts before
// x.jsonl.zst, so the plain copy wins, as it does in Codex, which restores it
// on resume before deleting the .zst. files must be in the order listFiles
// walked roots.
func codexPreferred(roots, files []string) []string {
	seen := map[string]bool{}
	out := files[:0:0]
	for _, f := range files {
		key := f
		for _, r := range roots {
			if rel, err := filepath.Rel(r, f); err == nil && !strings.HasPrefix(rel, "..") {
				key = filepath.Join(filepath.Dir(r), strings.TrimSuffix(rel, ".zst"))
				break
			}
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
	}
	return out
}

// sub appends the same path elements to every root.
func sub(roots []string, elem ...string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, filepath.Join(append([]string{r}, elem...)...))
	}
	return out
}

// Infos reports one entry per directory scanned, so a configuration with
// several roots is visible rather than collapsed into one line.
func Infos() []model.SourceInfo {
	specs := []struct {
		agent, note string
	}{
		{"claude", "Claude Code session jsonl"},
		{"codex", "Codex rollout jsonl[.zst] (sessions + archived_sessions)"},
		{"grok", "Grok turn_completed.usage"},
		{"gemini", "Gemini CLI chats json/jsonl"},
		{"opencode", "OpenCode assistant messages"},
	}
	var out []model.SourceInfo
	for _, s := range specs {
		roots, match, _ := agentSpec(s.agent)
		if len(roots) == 0 {
			out = append(out, model.SourceInfo{Agent: s.agent, Note: s.note})
			continue
		}
		for _, root := range roots {
			info := model.SourceInfo{Agent: s.agent, Root: root, Note: s.note}
			if st, err := os.Stat(root); err == nil && st.IsDir() {
				info.Exists = true
				files, _ := listFiles([]string{root}, match)
				info.Files = len(files)
				if s.agent == "codex" {
					// Count only what is read from this root: an archived
					// rollout still live in sessions/ is read from there.
					all, _ := listFiles(roots, match)
					info.Files = 0
					for _, f := range codexPreferred(roots, all) {
						if rel, err := filepath.Rel(root, f); err == nil && !strings.HasPrefix(rel, "..") {
							info.Files++
						}
					}
				}
			}
			out = append(out, info)
		}
	}
	return out
}

// Claude Code reads from ~/.claude and from the XDG config location, so both
// are scanned by default. The other agents have a single home each.
func claudeRoots() []string {
	if v := envRoots("CLAUDE_CONFIG_DIR"); len(v) > 0 {
		return v
	}
	return dedupPaths([]string{homedir(".claude"), xdgConfig("claude")})
}

func codexRoots() []string  { return envRootsOr("CODEX_HOME", homedir(".codex")) }
func grokRoots() []string   { return envRootsOr("GROK_HOME", homedir(".grok")) }
func geminiRoots() []string { return envRootsOr("GEMINI_DIR", homedir(".gemini")) }

func opencodeRoots() []string {
	if v := envRoots("OPENCODE_DATA"); len(v) > 0 {
		return v
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return []string{filepath.Join(expand(xdg), "opencode")}
	}
	return []string{homedir(".local/share/opencode")}
}

func xdgConfig(elem string) string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(expand(v), elem)
	}
	return homedir(filepath.Join(".config", elem))
}

// envRoots reads a directory list from the environment. Entries may be
// separated by commas -- the convention these agents' own tooling uses -- or by
// the platform's path list separator, so both habits work.
func envRoots(key string) []string {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == os.PathListSeparator
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, expand(f))
		}
	}
	return dedupPaths(out)
}

func envRootsOr(key, fallback string) []string {
	if v := envRoots(key); len(v) > 0 {
		return v
	}
	return []string{fallback}
}

// dedupPaths drops repeats so a root listed twice, or reachable by two names,
// is only walked once.
func dedupPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := paths[:0:0]
	for _, p := range paths {
		key := filepath.Clean(p)
		if abs, err := filepath.Abs(key); err == nil {
			key = abs
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}

func homedir(elem string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return elem
	}
	return filepath.Join(home, elem)
}

func expand(p string) string {
	if p == "" {
		return p
	}
	if p[0] == '~' {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}

func suffix(ext string) matcher {
	return func(_ string, d fs.DirEntry) bool {
		return !d.IsDir() && filepath.Ext(d.Name()) == ext
	}
}

// codexRollout matches a Codex rollout, plain or zstd-compressed.
func codexRollout(path string, d fs.DirEntry) bool {
	return !d.IsDir() && (strings.HasSuffix(d.Name(), ".jsonl") || strings.HasSuffix(d.Name(), ".jsonl.zst"))
}

func named(name string) matcher {
	return func(_ string, d fs.DirEntry) bool {
		return !d.IsDir() && d.Name() == name
	}
}

// geminiPreferred drops a legacy x.json whose x.jsonl sits beside it. Gemini
// CLI migrates a resumed .json session by appending its whole history to the
// .jsonl and leaves the .json in place, so reading both counts it twice.
func geminiPreferred(files []string) []string {
	have := make(map[string]bool, len(files))
	for _, f := range files {
		have[f] = true
	}
	out := files[:0:0]
	for _, f := range files {
		if filepath.Ext(f) == ".json" && have[f+"l"] {
			continue
		}
		out = append(out, f)
	}
	return out
}

func geminiChat(path string, d fs.DirEntry) bool {
	if ext := filepath.Ext(path); d.IsDir() || (ext != ".json" && ext != ".jsonl") {
		return false
	}
	// Subagents are recorded one level down, in chats/<parent-session-id>/.
	dir := filepath.Dir(path)
	return filepath.Base(dir) == "chats" || filepath.Base(filepath.Dir(dir)) == "chats"
}

// unreadDir is a directory that listFiles could not list.
type unreadDir struct {
	path string
	err  error
}

// listFiles walks every root, returning each matching file once. Roots can
// overlap or nest, so files are deduplicated by absolute path rather than
// parsed twice and reconciled later.
//
// It also returns the directories it could not list, roots included, since
// every session below one is missing from the totals. One that does not exist
// holds none: a root the agent never created, or a directory removed while
// the walk was under way. The walk carries on past the others.
func listFiles(roots []string, match matcher) ([]string, []unreadDir) {
	var (
		out    []string
		unread []unreadDir
	)
	seen := map[string]struct{}{}
	first := func(path string) bool {
		key := path
		if abs, err := filepath.Abs(path); err == nil {
			key = abs
		}
		if _, dup := seen[key]; dup {
			return false
		}
		seen[key] = struct{}{}
		return true
	}
	// failed records a directory that could not be listed, found at path
	// and reported as name.
	failed := func(path, name string, err error) {
		if !errors.Is(err, fs.ErrNotExist) && first(path) {
			unread = append(unread, unreadDir{name, err})
		}
	}
	for _, root := range roots {
		st, err := os.Stat(root)
		if err != nil {
			failed(root, root, err)
			continue
		}
		if !st.IsDir() {
			continue
		}
		// os.Stat follows a symlinked root, but WalkDir does not descend
		// one: it reports the link and stops, and the root yielded nothing.
		// The walk goes through the target, and what it finds is named under
		// the root as given, the path the user knows it by. Files are told
		// apart by where they really are, so a root reached both through a
		// link and directly is read once.
		walked := root
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			walked = resolved
		}
		named := func(path string) string {
			if rel, err := filepath.Rel(walked, path); err == nil {
				return filepath.Join(root, rel)
			}
			return path
		}
		_ = filepath.WalkDir(walked, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				failed(path, named(path), err)
				return nil
			}
			if d.IsDir() || match == nil || !match(named(path), d) {
				return nil
			}
			if first(path) {
				out = append(out, named(path))
			}
			return nil
		})
	}
	return out, unread
}

func mapFiles(agent string, files []string, c *cache.Cache, parse parser, progress ProgressFunc) ([]model.Usage, []Issue) {
	total := len(files)
	if progress != nil {
		progress(agent, 0, total)
	}
	if total == 0 {
		return nil, nil
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers > total {
		workers = total
	}
	in := make(chan string)
	var (
		mu     sync.Mutex
		out    []model.Usage
		issues []Issue
		wg     sync.WaitGroup
		done   atomic.Int32
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range in {
				recs, err := parseCached(c, path, parse)
				n := int(done.Add(1))
				if progress != nil {
					progress(agent, n, total)
				}
				if len(recs) == 0 && err == nil {
					continue
				}
				mu.Lock()
				out = append(out, recs...)
				if err != nil {
					issues = append(issues, Issue{Agent: agent, Path: path, Err: describe(path, err)})
				}
				mu.Unlock()
			}
		}()
	}
	for _, f := range files {
		in <- f
	}
	close(in)
	wg.Wait()
	return out, issues
}

// parseCached returns the records of one file, from the cache when the file
// has not changed since they were stored.
//
// Agents append to their logs while they run, possibly while the parse is
// under way. The file is therefore examined before and after, and the records
// are cached only if both agree, under the state they were read from. Caching
// them against a state read afterwards would match the grown file, and every
// later pass would serve the stale result until the file changed again.
//
// A file that was not read completely is never cached: its records are
// returned, with the error, but the next pass reads it again rather than
// taking the part it got for the whole.
func parseCached(c *cache.Cache, path string, parse parser) ([]model.Usage, error) {
	if c == nil {
		return parseListed(path, parse)
	}
	before, err := cache.Stat(path)
	if err != nil {
		return parseListed(path, parse)
	}
	if recs, ok := c.Get(path, before); ok {
		return recs, nil
	}
	recs, err := parseListed(path, parse)
	if err != nil {
		return recs, err
	}
	if after, err := cache.Stat(path); err == nil && after == before {
		c.Put(path, before, recs)
	}
	return recs, nil
}

// parseListed parses a file that the listing found. One that has disappeared
// since, as when an agent prunes old sessions, holds nothing to count, which
// is not a failure to read it.
func parseListed(path string, parse parser) ([]model.Usage, error) {
	recs, err := parse(path)
	if errors.Is(err, fs.ErrNotExist) {
		return recs, nil
	}
	return recs, err
}

// describe words a parse error for an Issue. The path has a field of its own,
// so the copy that os errors carry is left out.
func describe(path string, err error) string {
	if pe, ok := err.(*fs.PathError); ok && pe.Path == path {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}
