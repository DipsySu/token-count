package source

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"token-count/internal/cache"
	"token-count/internal/model"
)

type ProgressFunc func(agent string, files, records int)

// Stats describes what a collection pass saw before and after deduplication.
type Stats struct {
	Raw           int
	Kept          int
	Dropped       int
	DroppedTokens int64
}

// Collect parses every session file for the requested agents and returns the
// deduplicated records.
func Collect(agents []string, c *cache.Cache, progress ProgressFunc) []model.Usage {
	recs, _ := CollectStats(agents, c, progress)
	return recs
}

// CollectStats is Collect plus a report of how much replayed volume was
// dropped. Deduplication has to happen here rather than inside a parser
// because agents replay the same exchange across different files.
func CollectStats(agents []string, c *cache.Cache, progress ProgressFunc) ([]model.Usage, Stats) {
	var (
		mu  sync.Mutex
		out []model.Usage
		wg  sync.WaitGroup
	)
	for _, agent := range agents {
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs := collectAgent(agent, c)
			if progress != nil {
				progress(agent, -1, len(recs))
			}
			if len(recs) == 0 {
				return
			}
			mu.Lock()
			out = append(out, recs...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	stats := Stats{Raw: len(out)}
	kept := dedup(out, &stats)
	stats.Kept = len(kept)
	return kept, stats
}

// dedup collapses records that describe the same upstream response, keeping
// the largest.
//
// Two different things produce repeats, and they need the same treatment.
// Claude Code replays earlier messages into a new log every time a session is
// resumed or forked. It also writes several snapshots of a single streaming
// response: the early ones carry a partial output count (often 1 or 3 tokens)
// and the final one carries the real figure. Keeping whichever record was seen
// first therefore threw away most of the output tokens -- 26% of them across a
// real history -- even though the totals looked nearly right, because output is
// small next to cached input.
//
// The largest snapshot is the complete one. Selecting by size rather than by
// position also makes the result independent of the order files happen to be
// scanned in, which matters because collection runs in parallel.
//
// Records without a key are kept as-is: their format offers nothing to match on.
func dedup(recs []model.Usage, stats *Stats) []model.Usage {
	at := make(map[string]int, len(recs))
	out := recs[:0:0]
	for _, r := range recs {
		if r.Dedup == "" {
			out = append(out, r)
			continue
		}
		i, seen := at[r.Dedup]
		if !seen {
			at[r.Dedup] = len(out)
			out = append(out, r)
			continue
		}
		superseded := r
		if r.Total() > out[i].Total() {
			superseded = out[i]
			out[i] = r
		}
		if stats != nil {
			stats.Dropped++
			stats.DroppedTokens += superseded.Total()
		}
	}
	return out
}

func collectAgent(agent string, c *cache.Cache) []model.Usage {
	roots, match, parse := agentSpec(agent)
	if len(roots) == 0 {
		return nil
	}
	return mapFiles(listFiles(roots, match), c, parse)
}

type matcher func(path string, d fs.DirEntry) bool
type parser func(path string) []model.Usage

// agentSpec returns every directory to scan for an agent. There can be more
// than one: a machine may hold a migrated snapshot alongside the live data, and
// Claude Code itself reads from two locations.
func agentSpec(agent string) (roots []string, match matcher, parse parser) {
	switch agent {
	case "claude":
		return sub(claudeRoots(), "projects"), suffix(".jsonl"), parseClaude
	case "codex":
		return sub(codexRoots(), "sessions"), suffix(".jsonl"), parseCodex
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
		{"codex", "Codex rollout jsonl"},
		{"grok", "Grok turn_completed.usage"},
		{"gemini", "Gemini CLI session json"},
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
				info.Files = len(listFiles([]string{root}, match))
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

func named(name string) matcher {
	return func(_ string, d fs.DirEntry) bool {
		return !d.IsDir() && d.Name() == name
	}
}

func geminiChat(path string, d fs.DirEntry) bool {
	if d.IsDir() || filepath.Ext(path) != ".json" {
		return false
	}
	return filepath.Base(filepath.Dir(path)) == "chats"
}

// listFiles walks every root, returning each matching file once. Roots can
// overlap or nest, so files are deduplicated by absolute path rather than
// parsed twice and reconciled later.
func listFiles(roots []string, match matcher) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, root := range roots {
		st, err := os.Stat(root)
		if err != nil || !st.IsDir() {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if match == nil || !match(path, d) {
				return nil
			}
			key := path
			if abs, err := filepath.Abs(path); err == nil {
				key = abs
			}
			if _, dup := seen[key]; dup {
				return nil
			}
			seen[key] = struct{}{}
			out = append(out, path)
			return nil
		})
	}
	return out
}

func mapFiles(files []string, c *cache.Cache, parse parser) []model.Usage {
	if len(files) == 0 {
		return nil
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers > len(files) {
		workers = len(files)
	}
	in := make(chan string)
	var (
		mu  sync.Mutex
		out []model.Usage
		wg  sync.WaitGroup
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range in {
				recs := parseCached(c, path, parse)
				if len(recs) == 0 {
					continue
				}
				mu.Lock()
				out = append(out, recs...)
				mu.Unlock()
			}
		}()
	}
	for _, f := range files {
		in <- f
	}
	close(in)
	wg.Wait()
	return out
}

func parseCached(c *cache.Cache, path string, parse parser) []model.Usage {
	if c != nil {
		if recs, ok := c.Get(path); ok {
			return recs
		}
	}
	recs := parse(path)
	if c != nil {
		c.Put(path, recs)
	}
	return recs
}
