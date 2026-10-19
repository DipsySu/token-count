package source

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"token-count/internal/model"
)

// parseClaude reads a Claude Code session log. Claude replays earlier messages
// into new files when a session is resumed or forked, so every record carries a
// dedup key built from the upstream message and request ids.
func parseClaude(path string) []model.Usage {
	var out []model.Usage
	cwd := ""
	seq := 0
	_ = forEachJSONL(path, func(line []byte) {
		var rec struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Cwd       string `json:"cwd"`
			RequestID string `json:"requestId"`
			Message   struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage *struct {
					InputTokens              int64 `json:"input_tokens"`
					CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
					OutputTokens             int64 `json:"output_tokens"`
					OutputTokensDetails      *struct {
						ThinkingTokens int64 `json:"thinking_tokens"`
					} `json:"output_tokens_details"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil {
			return
		}
		if rec.Cwd != "" && cwd == "" {
			cwd = rec.Cwd
		}
		if rec.Type != "assistant" || rec.Message.Usage == nil {
			return
		}
		ts, ok := model.ParseTime(rec.Timestamp)
		if !ok {
			return
		}
		u := rec.Message.Usage
		seq++
		var thinking int64
		if u.OutputTokensDetails != nil {
			thinking = u.OutputTokensDetails.ThinkingTokens
		}
		if usage, ok := model.Make(model.MakeOpts{
			Agent:                   "claude",
			Time:                    ts,
			Model:                   rec.Message.Model,
			Project:                 rec.Cwd,
			Input:                   u.InputTokens,
			CacheRead:               u.CacheReadInputTokens,
			CacheWrite:              u.CacheCreationInputTokens,
			Output:                  u.OutputTokens,
			Reasoning:               thinking,
			OutputIncludesReasoning: true,
			Dedup:                   claudeKey(rec.Message.ID, rec.RequestID, path, seq),
			Source:                  path,
		}); ok {
			out = append(out, usage)
		}
	})
	return backfillProject(out, cwd)
}

// claudeKey identifies one upstream response. Both ids are present in current
// logs; older records carry only one, or neither. Without an id the key falls
// back to the record's location -- the path relative to its root, plus its
// position in the file -- which still collapses the same file reached through
// two different roots, and cannot match a genuinely different record because
// that one lives somewhere else.
func claudeKey(messageID, requestID, path string, seq int) string {
	switch {
	case messageID != "" && requestID != "":
		return "claude|" + messageID + "|" + requestID
	case messageID != "":
		return "claude|" + messageID
	case requestID != "":
		return "claude|req|" + requestID
	default:
		return seqKey("claude", "@"+pathTail(path, 3), seq)
	}
}

// parseCodex reads a Codex rollout log. Codex reports last_token_usage as the
// delta for the exchange that just finished, so records are already additive.
func parseCodex(path string) []model.Usage {
	var out []model.Usage
	modelName := ""
	cwd := ""
	// Codex has no per-response id, so identity is the session plus the
	// position of the event within it. Both survive a file being copied
	// elsewhere, which is what deduplication across roots needs. A timestamp
	// is not enough on its own: consecutive events can share one.
	//
	// session_meta is the first line, so its session_id is in hand before any
	// usage event; the filename stem stands in if a log lacks it.
	session := ""
	seq := 0
	_ = forEachJSONL(path, func(line []byte) {
		var rec struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Type           string `json:"type"`
				Model          string `json:"model"`
				Cwd            string `json:"cwd"`
				SessionID      string `json:"session_id"`
				ThreadSettings *struct {
					Model string `json:"model"`
				} `json:"thread_settings"`
				Info *struct {
					LastTokenUsage *struct {
						InputTokens           int64 `json:"input_tokens"`
						CachedInputTokens     int64 `json:"cached_input_tokens"`
						CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
						OutputTokens          int64 `json:"output_tokens"`
						ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
					} `json:"last_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &rec) != nil {
			return
		}
		if rec.Payload.Cwd != "" && cwd == "" {
			cwd = rec.Payload.Cwd
		}
		if rec.Payload.SessionID != "" {
			session = rec.Payload.SessionID
		}
		switch {
		case rec.Type == "session_meta" && rec.Payload.Model != "":
			modelName = rec.Payload.Model
			return
		case rec.Type == "turn_context" && rec.Payload.Model != "":
			modelName = rec.Payload.Model
			return
		case rec.Type == "event_msg" && rec.Payload.Type == "thread_settings_applied":
			if rec.Payload.ThreadSettings != nil && rec.Payload.ThreadSettings.Model != "" {
				modelName = rec.Payload.ThreadSettings.Model
			}
			return
		case rec.Type != "event_msg" || rec.Payload.Type != "token_count":
			return
		}
		if rec.Payload.Info == nil || rec.Payload.Info.LastTokenUsage == nil {
			return
		}
		ts, ok := model.ParseTime(rec.Timestamp)
		if !ok {
			return
		}
		last := rec.Payload.Info.LastTokenUsage
		seq++
		id := session
		if id == "" {
			id = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		}
		if usage, ok := model.Make(model.MakeOpts{
			Agent:                   "codex",
			Time:                    ts,
			Model:                   modelName,
			Input:                   last.InputTokens,
			CacheRead:               last.CachedInputTokens,
			CacheWrite:              last.CacheWriteInputTokens,
			Output:                  last.OutputTokens,
			Reasoning:               last.ReasoningOutputTokens,
			InputIncludesCache:      true,
			OutputIncludesReasoning: true,
			Dedup:                   seqKey("codex", id, seq),
			Source:                  path,
		}); ok {
			out = append(out, usage)
		}
	})
	return backfillProject(out, cwd)
}

type grokUsage struct {
	InputTokens         int64                `json:"inputTokens"`
	OutputTokens        int64                `json:"outputTokens"`
	CachedReadTokens    int64                `json:"cachedReadTokens"`
	CacheCreationTokens int64                `json:"cacheCreationTokens"`
	ReasoningTokens     int64                `json:"reasoningTokens"`
	ModelCalls          int64                `json:"modelCalls"`
	ModelUsage          map[string]grokUsage `json:"modelUsage"`
}

// parseGrok reads a Grok ACP update stream. Each turn_completed carries the
// usage for that turn only, and modelCalls says how many upstream requests the
// turn actually made.
func parseGrok(path string) []model.Usage {
	fallback := ""
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "summary.json")); err == nil {
		var summary struct {
			CurrentModelID string `json:"current_model_id"`
		}
		if json.Unmarshal(data, &summary) == nil {
			fallback = summary.CurrentModelID
		}
	}
	project := grokProject(path)
	// The session directory is a UUID, which identifies the conversation
	// independently of where the file happens to be stored.
	session := filepath.Base(filepath.Dir(path))
	turn := 0
	var out []model.Usage
	_ = forEachJSONL(path, func(line []byte) {
		var rec struct {
			Timestamp json.RawMessage `json:"timestamp"`
			Params    struct {
				Update *struct {
					SessionUpdate string     `json:"sessionUpdate"`
					Usage         *grokUsage `json:"usage"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &rec) != nil {
			return
		}
		if rec.Params.Update == nil || rec.Params.Update.SessionUpdate != "turn_completed" || rec.Params.Update.Usage == nil {
			return
		}
		ts, ok := model.ParseRawTime(rec.Timestamp)
		if !ok {
			return
		}
		u := rec.Params.Update.Usage
		turn++
		emitted := false
		for name, item := range u.ModelUsage {
			// One turn can bill several models, so the model name is part of
			// the key alongside the turn number.
			key := seqKey("grok", session, turn) + "|" + name
			if usage, ok := grokMake(path, project, ts, name, item, key); ok {
				out = append(out, usage)
				emitted = true
			}
		}
		if !emitted {
			key := seqKey("grok", session, turn)
			if usage, ok := grokMake(path, project, ts, fallback, *u, key); ok {
				out = append(out, usage)
			}
		}
	})
	return out
}

// grokProject recovers the working directory from the session path, which Grok
// stores as a percent-encoded absolute path.
func grokProject(path string) string {
	// .../sessions/<encoded cwd>/<session id>/updates.jsonl
	dir := filepath.Dir(filepath.Dir(path))
	decoded, err := url.QueryUnescape(filepath.Base(dir))
	if err != nil || !strings.HasPrefix(decoded, "/") {
		return ""
	}
	return decoded
}

func grokMake(path, project string, ts time.Time, name string, u grokUsage, dedup string) (model.Usage, bool) {
	return model.Make(model.MakeOpts{
		Agent:                   "grok",
		Time:                    ts,
		Model:                   name,
		Project:                 project,
		Input:                   u.InputTokens,
		CacheRead:               u.CachedReadTokens,
		CacheWrite:              u.CacheCreationTokens,
		Output:                  u.OutputTokens,
		Reasoning:               u.ReasoningTokens,
		Calls:                   u.ModelCalls,
		InputIncludesCache:      true,
		OutputIncludesReasoning: true,
		Dedup:                   dedup,
		Source:                  path,
	})
}

func parseGemini(path string) []model.Usage {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec struct {
		Cwd      string `json:"cwd"`
		Messages []struct {
			Timestamp string `json:"timestamp"`
			Model     string `json:"model"`
			Tokens    *struct {
				Input                   int64 `json:"input"`
				Output                  int64 `json:"output"`
				Cached                  int64 `json:"cached"`
				CachedContentTokenCount int64 `json:"cached_content_token_count"`
				Thoughts                int64 `json:"thoughts"`
				ThoughtsTokenCount      int64 `json:"thoughts_token_count"`
			} `json:"tokens"`
		} `json:"messages"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return nil
	}
	// Gemini exposes no message id, so identity falls back to where the file
	// sits relative to its root plus the message's position. That is stable
	// for a copied tree and cannot collide with a genuinely different session,
	// which would live at a different path.
	session := "@" + pathTail(path, 3)
	var out []model.Usage
	seq := 0
	for _, msg := range rec.Messages {
		if msg.Tokens == nil {
			continue
		}
		seq++
		ts, ok := model.ParseTime(msg.Timestamp)
		if !ok {
			continue
		}
		cached := msg.Tokens.Cached
		if cached == 0 {
			cached = msg.Tokens.CachedContentTokenCount
		}
		thoughts := msg.Tokens.Thoughts
		if thoughts == 0 {
			thoughts = msg.Tokens.ThoughtsTokenCount
		}
		if usage, ok := model.Make(model.MakeOpts{
			Agent:                   "gemini",
			Time:                    ts,
			Model:                   msg.Model,
			Project:                 rec.Cwd,
			Input:                   msg.Tokens.Input,
			CacheRead:               cached,
			Output:                  msg.Tokens.Output,
			Reasoning:               thoughts,
			InputIncludesCache:      true,
			OutputIncludesReasoning: false,
			Dedup:                   seqKey("gemini", session, seq),
			Source:                  path,
		}); ok {
			out = append(out, usage)
		}
	}
	return out
}

func parseOpenCode(path string) []model.Usage {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec struct {
		ID      string `json:"id"`
		Role    string `json:"role"`
		ModelID string `json:"modelID"`
		Model   string `json:"model"`
		Path    *struct {
			Cwd  string `json:"cwd"`
			Root string `json:"root"`
		} `json:"path"`
		Tokens *struct {
			Input     int64 `json:"input"`
			Output    int64 `json:"output"`
			Reasoning int64 `json:"reasoning"`
			Cache     *struct {
				Read  int64 `json:"read"`
				Write int64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
		Time *struct {
			Created   int64 `json:"created"`
			Completed int64 `json:"completed"`
		} `json:"time"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.Role != "assistant" || rec.Tokens == nil {
		return nil
	}
	var unix int64
	if rec.Time != nil {
		unix = rec.Time.Completed
		if unix == 0 {
			unix = rec.Time.Created
		}
	}
	ts, ok := model.ParseTime(unix)
	if !ok {
		return nil
	}
	var cr, cw int64
	if rec.Tokens.Cache != nil {
		cr = rec.Tokens.Cache.Read
		cw = rec.Tokens.Cache.Write
	}
	name := rec.ModelID
	if name == "" {
		name = rec.Model
	}
	project := ""
	if rec.Path != nil {
		project = rec.Path.Cwd
		if project == "" {
			project = rec.Path.Root
		}
	}
	dedup := ""
	if rec.ID != "" {
		dedup = "opencode|" + rec.ID
	}
	if usage, ok := model.Make(model.MakeOpts{
		Agent:                   "opencode",
		Time:                    ts,
		Model:                   name,
		Project:                 project,
		Input:                   rec.Tokens.Input,
		CacheRead:               cr,
		CacheWrite:              cw,
		Output:                  rec.Tokens.Output,
		Reasoning:               rec.Tokens.Reasoning,
		OutputIncludesReasoning: false,
		Dedup:                   dedup,
		Source:                  path,
	}); ok {
		return []model.Usage{usage}
	}
	return nil
}

// seqKey builds a deduplication key from a session identity and a position
// within it. It returns nothing when there is no session to anchor to, so an
// unidentifiable record is kept rather than merged with an unrelated one.
func seqKey(agent, session string, seq int) string {
	if session == "" {
		return ""
	}
	return agent + "|" + session + "|" + strconv.Itoa(seq)
}

// pathTail keeps the last n path elements, so a key derived from a location is
// the same whichever root the tree was reached through.
func pathTail(path string, n int) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	return strings.Join(parts, "/")
}

// backfillProject fills records whose line carried no working directory with
// the one seen elsewhere in the same session file.
func backfillProject(recs []model.Usage, cwd string) []model.Usage {
	if cwd == "" {
		return recs
	}
	for i := range recs {
		if recs[i].Project == "" {
			recs[i].Project = cwd
		}
	}
	return recs
}

func forEachJSONL(path string, fn func([]byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		fn(line)
	}
	return sc.Err()
}
