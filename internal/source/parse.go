package source

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"token-count/internal/model"
)

// parseClaude reads a Claude Code session log. Claude replays earlier messages
// into new files when a session is resumed or forked, so every record carries a
// dedup key built from the upstream message and request ids.
//
// A session started from another, such as a background session, begins its
// log with a copy of the other's conversation. The copied lines keep their ids
// and times but take the new session's id and working directory; session_id
// still names the session they were first written in. Such a line's key gets
// copyMark, so that deduplication keeps the original, which says where the
// call was made, and falls back on the copy only where the original's log is
// gone. Lines without session_id, as older versions write them, are taken for
// originals.
func parseClaude(path string) ([]model.Usage, error) {
	var out []model.Usage
	cwd := ""
	seq := 0
	err := forEachJSONL(path, func(line []byte) error {
		var rec struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Cwd       string `json:"cwd"`
			RequestID string `json:"requestId"`
			SessionID string `json:"sessionId"`
			// Origin is the session the line was first written in.
			Origin  string `json:"session_id"`
			Message struct {
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
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if rec.Cwd != "" && cwd == "" {
			cwd = rec.Cwd
		}
		if rec.Type != "assistant" || rec.Message.Usage == nil {
			return nil
		}
		ts, ok := model.ParseTime(rec.Timestamp)
		if !ok {
			return nil
		}
		u := rec.Message.Usage
		seq++
		var thinking int64
		if u.OutputTokensDetails != nil {
			thinking = u.OutputTokensDetails.ThinkingTokens
		}
		key := claudeKey(rec.Message.ID, rec.RequestID, path, seq)
		if rec.Origin != "" && rec.SessionID != "" && rec.Origin != rec.SessionID {
			key = copyMark + key
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
			Dedup:                   key,
			Source:                  path,
		}); ok {
			out = append(out, usage)
		}
		return nil
	})
	return backfillProject(out, cwd), err
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

// parseCodex reads a Codex rollout log. Codex reports usage in two ways, and
// one log can hold both.
//
// Codex 0.154 and later write a token_usage_record line for every upstream
// response as it completes: what that one response cost, with the response's
// id. Wherever a log has these records they are what is counted, a call each,
// because they are the precise report. Every call gets one, including those
// nothing else reports: compacting the context is an upstream request of its
// own, which token_count only follows with an empty exchange, and a turn cut
// short can leave its last response with no token_count at all. Each is
// written once, so there is no re-publication to see through. And the
// response id is unique across every log, so a fork or a copy of a log that
// holds the same response deduplicates exactly, by that id rather than by
// position.
//
// Older versions report usage only in token_count events. Every event carries
// two figures: last_token_usage, what the exchange that just finished cost,
// and total_token_usage, the running sum for the thread. Codex publishes the
// event again whenever something else changes -- the rate limits refresh, the
// context is compacted, a resumed process announces itself -- so one exchange
// can appear several times, and summing every event overstated a real history
// by almost 10%. An event whose running total equals the previous one is such
// a re-publication and is skipped.
//
// The running total serves only to recognise those. It does not measure what
// an exchange cost: a resumed process starts a fresh total, or one that already
// includes usage recorded elsewhere, so across a resume consecutive totals can
// differ by millions of tokens more, or fewer, than the exchange in between.
// last_token_usage is therefore the cost whenever it is present, and the
// difference of totals only stands in for an event that lacks it.
//
// The versions that write records still publish token_count events, each
// repeating the usage of the record written just before it, and a newer Codex
// that resumes an older session appends its records after the old events. So
// events count up to a log's first record and are ignored from there on: by
// then the records report every call the events do, and more, and counting
// both would count each call twice.
func parseCodex(path string) ([]model.Usage, error) {
	var out []model.Usage
	modelName := ""
	cwd := ""
	// token_count events carry no response id, so an exchange is keyed by its
	// time, model and usage (see below), which also match its copies in other
	// logs. A record that lacks its response id is keyed by the thread plus
	// its position. Both survive a file being copied elsewhere. A timestamp is
	// not enough on its own: consecutive events can share one.
	//
	// The thread is the id in the log's first session_meta, which precedes any
	// usage event; the filename stem stands in if a log lacks it. session_id
	// will not do: recent versions put the root session's id there in a
	// subagent's log, which also quotes its parent's session_meta, so keying on
	// it merged every subagent's exchanges with its parent's.
	//
	// Only calls advance the position: counted exchanges, and records of some
	// usage. A re-publication is not an exchange, and what triggers one has
	// nothing to do with the conversation, so letting it shift the exchanges
	// after it would make their keys depend on timing rather than on what was
	// said. Either way a key is a function of the file's contents, which is all
	// that matching a copy needs.
	thread := ""
	seq := 0
	// position takes the next position and returns the key it makes.
	position := func() string {
		seq++
		id := thread
		if id == "" {
			id = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".zst"), ".jsonl")
		}
		return seqKey("codex", id, seq)
	}
	// prev is the running total of the latest usable event that reported one.
	var prev *codexTokens
	// recorded is set at the log's first token_usage_record, from where on
	// token_count events are not counted.
	recorded := false
	// A fork or subagent names its parent in its first session_meta. Its
	// token_count events are tracked, so that the burst of parent history it
	// replays at the head of the log can be told from its own usage.
	child := false
	var events []int
	err := forEachJSONL(path, func(line []byte) error {
		var rec struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Type           string          `json:"type"`
				ID             string          `json:"id"`
				Model          string          `json:"model"`
				Cwd            string          `json:"cwd"`
				ForkedFromID   string          `json:"forked_from_id"`
				Source         json.RawMessage `json:"source"`
				SessionID      string          `json:"session_id"`
				ResponseID     string          `json:"response_id"`
				Usage          *codexTokens    `json:"usage"`
				ThreadSettings *struct {
					Model string `json:"model"`
				} `json:"thread_settings"`
				Info *struct {
					LastTokenUsage  *codexTokens `json:"last_token_usage"`
					TotalTokenUsage *codexTokens `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if rec.Payload.Cwd != "" && cwd == "" {
			cwd = rec.Payload.Cwd
		}
		switch {
		case rec.Type == "session_meta":
			if thread == "" {
				child = codexParent(rec.Payload.ForkedFromID, rec.Payload.Source) != ""
				thread = rec.Payload.ID
				if thread == "" {
					thread = rec.Payload.SessionID
				}
			}
			if rec.Payload.Model != "" {
				modelName = rec.Payload.Model
			}
			return nil
		case rec.Type == "turn_context" && rec.Payload.Model != "":
			modelName = rec.Payload.Model
			return nil
		case rec.Type == "event_msg" && rec.Payload.Type == "thread_settings_applied":
			if rec.Payload.ThreadSettings != nil && rec.Payload.ThreadSettings.Model != "" {
				modelName = rec.Payload.ThreadSettings.Model
			}
			return nil
		case rec.Type == "token_usage_record":
			// Whatever this one holds, the log records its responses from
			// here on, and every token_count event after it repeats one.
			recorded = true
			// A record that cannot be placed in time is dropped, as an
			// untimed event is, and one of no usage is no call.
			ts, ok := model.ParseTime(rec.Timestamp)
			if !ok || rec.Payload.Usage == nil {
				return nil
			}
			usage, ok := codexCall(path, ts, modelName, *rec.Payload.Usage)
			if !ok {
				return nil
			}
			// Every call takes a position, which keys it only if it lacks
			// a response id.
			usage.Dedup = position()
			if rec.Payload.ResponseID != "" {
				usage.Dedup = "codex|resp|" + rec.Payload.ResponseID
			}
			out = append(out, usage)
			return nil
		case recorded || rec.Type != "event_msg" || rec.Payload.Type != "token_count":
			return nil
		}
		info := rec.Payload.Info
		if info == nil {
			return nil
		}
		// An event without a usable time is dropped whole, like a malformed
		// line. Were its total kept as the baseline, a re-publication of the
		// same state with a readable time would be skipped as a repeat, and
		// the exchange would not be counted at all.
		ts, ok := model.ParseTime(rec.Timestamp)
		if !ok {
			return nil
		}
		use, counted := codexExchange(info.LastTokenUsage, info.TotalTokenUsage, prev)
		if info.TotalTokenUsage != nil {
			prev = info.TotalTokenUsage
		}
		if !counted {
			return nil
		}
		position()
		if usage, ok := codexCall(path, ts, modelName, use); ok {
			// Branches and subagents copy token_count events into their own
			// logs, so an exchange is known by what it says, not where it
			// sits: its time, model and usage, as ccusage matches them.
			usage.Dedup = fmt.Sprintf("codex|use|%s|%s|%d|%d|%d|%d|%d", rec.Timestamp, modelName,
				use.InputTokens, use.CachedInputTokens, use.CacheWriteInputTokens, use.OutputTokens, use.ReasoningOutputTokens)
			events = append(events, len(out))
			out = append(out, usage)
		}
		return nil
	})
	if child {
		out = dropReplayedBurst(out, events)
	}
	return backfillProject(out, cwd), err
}

// codexParent returns the thread a session was forked or spawned from, or ""
// for a session of its own. source is a string ("cli", "exec") for those.
func codexParent(forkedFrom string, source json.RawMessage) string {
	if forkedFrom != "" {
		return forkedFrom
	}
	var src struct {
		Subagent struct {
			ThreadSpawn struct {
				ParentThreadID string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(source, &src) != nil {
		return ""
	}
	return src.Subagent.ThreadSpawn.ParentThreadID
}

// replayGap is the longest pause inside a burst of replayed usage. Codex
// writes a child's copy of its parent's history in one go, stamped with the
// moment of the fork, while the child's own first exchange follows a real
// pause of seconds. ccusage uses the same bound.
const replayGap = time.Second

// dropReplayedBurst removes the parent history a fork or subagent replayed at
// the head of its log: its leading token_count records, when the first two
// were written within replayGap of each other, up to the first real pause.
// Their timestamps were rewritten, so matching across files cannot catch them.
//
// ccusage first lines the child's usage up against the parent's log and falls
// back to this burst when it cannot; counting each file on its own, tcount
// uses the burst alone, which drops the same events whenever Codex rewrote
// the replay.
func dropReplayedBurst(out []model.Usage, events []int) []model.Usage {
	within := func(a, b int) bool {
		d := out[b].Time.Sub(out[a].Time)
		return d >= 0 && d <= replayGap
	}
	if len(events) < 2 || !within(events[0], events[1]) {
		return out
	}
	n := 1
	for n < len(events) && within(events[n-1], events[n]) {
		n++
	}
	drop := map[int]bool{}
	for _, i := range events[:n] {
		drop[i] = true
	}
	kept := out[:0]
	for i, r := range out {
		if !drop[i] {
			kept = append(kept, r)
		}
	}
	return kept
}

// codexCall makes the record of one call from its Codex usage block, or
// reports false for a block of no usage at all.
func codexCall(path string, ts time.Time, modelName string, use codexTokens) (model.Usage, bool) {
	return model.Make(model.MakeOpts{
		Agent:                   "codex",
		Time:                    ts,
		Model:                   modelName,
		Input:                   use.InputTokens,
		CacheRead:               use.CachedInputTokens,
		CacheWrite:              use.CacheWriteInputTokens,
		Output:                  use.OutputTokens,
		Reasoning:               use.ReasoningOutputTokens,
		InputIncludesCache:      true,
		OutputIncludesReasoning: true,
		Source:                  path,
	})
}

// codexTokens is one usage block, of a token_usage_record or a token_count
// event. Input includes the cached part and output includes reasoning, as
// Codex reports them.
type codexTokens struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// codexExchange works out what one token_count event adds, given the running
// total of the event before it (nil at the start of the log). It reports false
// for an event that adds nothing: a re-publication, whose running total equals
// the previous one, or an event that carries neither figure.
func codexExchange(last, total, prev *codexTokens) (codexTokens, bool) {
	if total != nil && prev != nil && *total == *prev {
		return codexTokens{}, false
	}
	if last != nil {
		return *last, true
	}
	if total == nil {
		return codexTokens{}, false
	}
	if prev != nil {
		if d, ok := total.since(*prev); ok {
			return d, true
		}
	}
	// The first total, or one that dropped because a resumed process started
	// counting afresh, is all usage since that accumulator began.
	return *total, true
}

// since returns how far the running total t moved on from prev, and false if
// any field went down, which means t comes from a different accumulator.
func (t codexTokens) since(prev codexTokens) (codexTokens, bool) {
	d := codexTokens{
		InputTokens:           t.InputTokens - prev.InputTokens,
		CachedInputTokens:     t.CachedInputTokens - prev.CachedInputTokens,
		CacheWriteInputTokens: t.CacheWriteInputTokens - prev.CacheWriteInputTokens,
		OutputTokens:          t.OutputTokens - prev.OutputTokens,
		ReasoningOutputTokens: t.ReasoningOutputTokens - prev.ReasoningOutputTokens,
	}
	ok := d.InputTokens >= 0 && d.CachedInputTokens >= 0 && d.CacheWriteInputTokens >= 0 &&
		d.OutputTokens >= 0 && d.ReasoningOutputTokens >= 0
	return d, ok
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
func parseGrok(path string) ([]model.Usage, error) {
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
	err := forEachJSONL(path, func(line []byte) error {
		var rec struct {
			Timestamp json.RawMessage `json:"timestamp"`
			Params    struct {
				Update *struct {
					SessionUpdate string     `json:"sessionUpdate"`
					Usage         *grokUsage `json:"usage"`
				} `json:"update"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if rec.Params.Update == nil || rec.Params.Update.SessionUpdate != "turn_completed" || rec.Params.Update.Usage == nil {
			return nil
		}
		ts, ok := model.ParseRawTime(rec.Timestamp)
		if !ok {
			return nil
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
		return nil
	})
	return out, err
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

// geminiMessage is one message of a Gemini CLI chat recording.
type geminiMessage struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Model     string `json:"model"`
	Tokens    *struct {
		Input                   int64 `json:"input"`
		Output                  int64 `json:"output"`
		Cached                  int64 `json:"cached"`
		CachedContentTokenCount int64 `json:"cached_content_token_count"`
		Thoughts                int64 `json:"thoughts"`
		ThoughtsTokenCount      int64 `json:"thoughts_token_count"`
		Tool                    int64 `json:"tool"`
		ToolUsePromptTokenCount int64 `json:"tool_use_prompt_token_count"`
	} `json:"tokens"`
}

func parseGemini(path string) ([]model.Usage, error) {
	if filepath.Ext(path) == ".jsonl" {
		return parseGeminiJSONL(path)
	}
	var rec struct {
		Cwd      string          `json:"cwd"`
		Messages []geminiMessage `json:"messages"`
	}
	if ok, err := readJSON(path, &rec); !ok {
		return nil, err
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
		if usage, ok := geminiUsage(path, rec.Cwd, msg, seqKey("gemini", session, seq)); ok {
			out = append(out, usage)
		}
	}
	return out, nil
}

// parseGeminiJSONL reads the JSONL chat recording of newer Gemini CLI
// versions: a metadata line, then a line for every write of a message. A
// message is written again each time it changes -- its tokens arrive after
// the stream, its tool calls finish -- so the last line of an id is the
// message. Lines of $set, $rewindTo or $patch edit metadata or the history
// shown, not what was spent, and are skipped; so is every message but a
// "gemini" reply, as ccusage does.
func parseGeminiJSONL(path string) ([]model.Usage, error) {
	session := "@" + pathTail(path, 3)
	cwd := ""
	var msgs []geminiMessage
	at := map[string]int{}
	err := forEachJSONL(path, func(line []byte) error {
		var rec struct {
			geminiMessage
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if cwd == "" {
			cwd = rec.Cwd
		}
		m := rec.geminiMessage
		if m.Type != "gemini" {
			return nil
		}
		if i, ok := at[m.ID]; ok && m.ID != "" {
			msgs[i] = m
			return nil
		}
		at[m.ID] = len(msgs)
		msgs = append(msgs, m)
		return nil
	})
	var out []model.Usage
	for i, m := range msgs {
		if m.Tokens == nil {
			continue
		}
		key := seqKey("gemini", session, i+1)
		if m.ID != "" {
			key = "gemini|" + session + "|" + m.ID
		}
		if usage, ok := geminiUsage(path, cwd, m, key); ok {
			out = append(out, usage)
		}
	}
	return out, err
}

// geminiUsage makes the record of one Gemini reply, or reports false for one
// without a usable time or of no usage.
func geminiUsage(path, cwd string, msg geminiMessage, dedup string) (model.Usage, bool) {
	ts, ok := model.ParseTime(msg.Timestamp)
	if !ok || msg.Tokens == nil {
		return model.Usage{}, false
	}
	cached := msg.Tokens.Cached
	if cached == 0 {
		cached = msg.Tokens.CachedContentTokenCount
	}
	thoughts := msg.Tokens.Thoughts
	if thoughts == 0 {
		thoughts = msg.Tokens.ThoughtsTokenCount
	}
	// tokens.tool is toolUsePromptTokenCount: uncached prompt-side input
	// billed on top of tokens.input, never part of cached.
	tool := msg.Tokens.Tool
	if tool == 0 {
		tool = msg.Tokens.ToolUsePromptTokenCount
	}
	return model.Make(model.MakeOpts{
		Agent:                   "gemini",
		Time:                    ts,
		Model:                   msg.Model,
		Project:                 cwd,
		Input:                   msg.Tokens.Input + tool,
		CacheRead:               cached,
		Output:                  msg.Tokens.Output,
		Reasoning:               thoughts,
		InputIncludesCache:      true,
		OutputIncludesReasoning: false,
		Dedup:                   dedup,
		Source:                  path,
	})
}

func parseOpenCode(path string) ([]model.Usage, error) {
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
	if ok, err := readJSON(path, &rec); !ok {
		return nil, err
	}
	if rec.Role != "assistant" || rec.Tokens == nil {
		return nil, nil
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
		return nil, nil
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
		return []model.Usage{usage}, nil
	}
	return nil, nil
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

// readJSON decodes a log that is a single JSON document. It reports false,
// with no error, for a file that holds JSON of another shape: there is nothing
// of ours in it to count. Any other failure is returned -- a read error, or
// JSON that is malformed or cut short, down to an empty file, as one caught
// while being rewritten is -- because that file's records are missing from
// the totals.
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		var shape *json.UnmarshalTypeError
		if errors.As(err, &shape) {
			return false, nil
		}
		return false, fmt.Errorf("malformed JSON: %w", err)
	}
	return true, nil
}

// maxLineBytes is the longest JSONL line that is parsed. Nothing near it is a
// usage record -- such a line is a huge tool output or a corrupt file -- so a
// longer line is skipped, and reported, rather than held in memory. It is a
// variable so that tests can lower it.
var maxLineBytes = 64 << 20

// forEachJSONL calls fn with every non-blank line of a JSONL file, trimmed of
// surrounding space. The slice is only valid until fn returns. fn returns the
// error decoding the line gave, if any.
//
// A line longer than maxLineBytes is skipped and reading carries on with the
// next; the skipped lines are reported in the error returned at the end,
// because whatever usage they held is missing from the totals. bufio.Scanner,
// used before, stopped for good at the first such line, so the records after
// it were lost without a word. A read error ends the walk and is returned.
//
// A line that is not JSON is reported the same way, unless it is the last:
// an agent still writing its log leaves the last line half done, and it will
// be whole on the next pass. A crash can leave a run of NUL bytes where data
// was due, alone on a line or glued in front of the next record, so leading
// NULs are dropped first: a record after them is intact and counts, and a
// line of nothing else is a corrupt one. JSON of a shape fn did not expect
// is not ours to count, and is not corrupt.
func forEachJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".zst") {
		// Codex compresses old rollouts with zstd.
		zr, err := zstd.NewReader(f)
		if err != nil {
			return err
		}
		defer zr.Close()
		r = zr
	}
	lr := lineReader{r: bufio.NewReaderSize(r, 64<<10)}
	var (
		skipped *longLinesError
		corrupt *corruptLinesError
		// pending is the number of the latest line that did not parse, until
		// a line after it shows that it was not the last.
		pending int
	)
	confirm := func() {
		if pending == 0 {
			return
		}
		if corrupt == nil {
			corrupt = &corruptLinesError{first: pending}
		}
		corrupt.count++
		pending = 0
	}
	for {
		line, tooLong, err := lr.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if tooLong {
			confirm()
			if skipped == nil {
				skipped = &longLinesError{first: lr.n, limit: maxLineBytes}
			}
			skipped.count++
			continue
		}
		if line = bytes.TrimSpace(line); len(line) == 0 {
			continue
		}
		confirm()
		line = bytes.TrimSpace(bytes.TrimLeft(line, "\x00"))
		if len(line) == 0 || malformed(fn(line)) {
			pending = lr.n
		}
	}
	switch {
	case skipped != nil && corrupt != nil:
		return fmt.Errorf("%w; %w", skipped, corrupt)
	case skipped != nil:
		return skipped
	case corrupt != nil:
		return corrupt
	}
	return nil
}

// malformed reports whether err says a line is not JSON at all, rather than
// JSON of another shape.
func malformed(err error) bool {
	var shape *json.UnmarshalTypeError
	return err != nil && !errors.As(err, &shape)
}

// lineReader splits a stream into lines without ever holding more than
// maxLineBytes of one.
type lineReader struct {
	r   *bufio.Reader
	buf []byte // assembles a line too long for r's buffer
	n   int    // the number of the line last returned, from 1
}

// next returns the next line without its newline. A line longer than
// maxLineBytes is read to its end in pieces and dropped: next reports it with
// tooLong set and no content. After the last line, which may lack a newline,
// next returns io.EOF.
func (lr *lineReader) next() (line []byte, tooLong bool, err error) {
	lr.buf = lr.buf[:0]
	size := 0
	for {
		var chunk []byte
		chunk, err = lr.r.ReadSlice('\n')
		more := err == bufio.ErrBufferFull
		switch {
		case err == nil:
			chunk = chunk[:len(chunk)-1]
		case err == io.EOF:
			if size == 0 && len(chunk) == 0 {
				return nil, false, io.EOF
			}
		case !more:
			return nil, false, err
		}
		size += len(chunk)
		switch {
		case size > maxLineBytes:
			// Keep none of it, but read on to its end. The buffer goes too,
			// rather than sit at the limit for the rest of the file.
			lr.buf = nil
		case !more && len(lr.buf) == 0:
			// The whole line arrived at once, as nearly every line does, so
			// it is used where it lies.
			line = chunk
		default:
			// The buffer doubles as it fills, as bufio.Scanner's did, up to
			// the limit. append grows a large slice by a quarter at a time,
			// and the copies it left behind came to about five times the
			// line.
			if need := len(lr.buf) + len(chunk); need > cap(lr.buf) {
				grown := make([]byte, len(lr.buf), max(need, min(2*cap(lr.buf), maxLineBytes)))
				copy(grown, lr.buf)
				lr.buf = grown
			}
			lr.buf = append(lr.buf, chunk...)
			line = lr.buf
		}
		if !more {
			lr.n++
			if size > maxLineBytes {
				return nil, true, nil
			}
			return line, false, nil
		}
	}
}

// longLinesError reports the lines forEachJSONL skipped for being longer than
// maxLineBytes.
type longLinesError struct {
	count int // how many were skipped
	first int // the number of the first, from 1
	limit int
}

func (e *longLinesError) Error() string {
	limit := strconv.Itoa(e.limit) + " bytes"
	if e.limit >= 1<<20 && e.limit%(1<<20) == 0 {
		limit = strconv.Itoa(e.limit>>20) + " MiB"
	}
	if e.count == 1 {
		return fmt.Sprintf("line %d is longer than %s and was not counted", e.first, limit)
	}
	return fmt.Sprintf("%d lines longer than %s were not counted, the first at line %d", e.count, limit, e.first)
}

// corruptLinesError reports the lines forEachJSONL could not parse, other
// than the last.
type corruptLinesError struct {
	count int // how many there were
	first int // the number of the first, from 1
}

func (e *corruptLinesError) Error() string {
	if e.count == 1 {
		return fmt.Sprintf("line %d is corrupt and was not counted", e.first)
	}
	return fmt.Sprintf("%d corrupt lines were not counted, the first at line %d", e.count, e.first)
}
