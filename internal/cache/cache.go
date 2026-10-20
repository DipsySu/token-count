package cache

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"token-count/internal/model"
)

// Bumped whenever the shape or meaning of a cached record changes. v5 stopped
// counting the token_count events Codex publishes again unchanged, and keyed
// Codex records by thread. v6 counts a Codex log's token_usage_record lines,
// one per response, keyed by response id and timed by the record, in place of
// the token_count events that repeat them, and marks the key of a call Claude
// Code copied into another session's log. Entries cached earlier lack both.
// v7 reads a record behind a run of NUL bytes, which a crash can leave glued
// to its front, and reports a corrupt line instead of skipping it; a file
// cached by v6 may hold a log read short without a word, so none of it is
// reused. v8 keys Codex exchanges by time, model and usage so copies merge
// across files. v9 drops a Codex subagent's replay of a single parent event,
// which v8 cached as the child's own.
const version = 9

func init() {
	gob.Register(time.Time{})
	gob.Register(model.Usage{})
}

// FileState is what the cache knows a file by. Records are served only while
// the file still has the state they were parsed from.
type FileState struct {
	ModNano int64
	Size    int64
}

// Stat reads the current state of the file at path.
func Stat(path string) (FileState, error) {
	st, err := os.Stat(path)
	if err != nil {
		return FileState{}, err
	}
	return FileState{ModNano: st.ModTime().UnixNano(), Size: st.Size()}, nil
}

type entry struct {
	State   FileState
	Records []model.Usage
}

type file struct {
	Version int
	Files   map[string]entry
}

type Cache struct {
	mu      sync.RWMutex
	path    string
	enabled bool
	data    file
	dirty   bool
}

func DefaultPath() string {
	root := os.Getenv("XDG_CACHE_HOME")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".cache")
	}
	return filepath.Join(root, "token-count", "index-v"+strconv.Itoa(version)+".gob")
}

func New(path string, enabled bool) *Cache {
	c := &Cache{
		path:    path,
		enabled: enabled,
		data:    file{Version: version, Files: map[string]entry{}},
	}
	if path == "" {
		c.path = DefaultPath()
	}
	if enabled {
		c.load()
	}
	return c
}

func (c *Cache) load() {
	f, err := os.Open(c.path)
	if err != nil {
		return
	}
	defer f.Close()
	var data file
	if err := gob.NewDecoder(f).Decode(&data); err != nil {
		return
	}
	if data.Version != version || data.Files == nil {
		return
	}
	c.data = data
}

// Get returns the records cached for path, provided they were parsed from the
// state the file has now.
func (c *Cache) Get(path string, now FileState) ([]model.Usage, bool) {
	if !c.enabled {
		return nil, false
	}
	c.mu.RLock()
	ent, ok := c.data.Files[path]
	c.mu.RUnlock()
	if !ok || ent.State != now {
		return nil, false
	}
	return ent.Records, true
}

// Put stores the records parsed from path while it had state st. The caller
// says which state that was because only it knows: by the time the parse
// finishes an agent may have appended to the file, and reading the state then
// would bind the records to contents they do not describe.
func (c *Cache) Put(path string, st FileState, records []model.Usage) {
	if !c.enabled {
		return
	}
	c.mu.Lock()
	c.data.Files[path] = entry{State: st, Records: records}
	c.dirty = true
	c.mu.Unlock()
}

func (c *Cache) Save() error {
	if !c.enabled {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), "token-count-*.gob")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	enc := gob.NewEncoder(tmp)
	err = enc.Encode(c.data)
	closeErr := tmp.Close()
	if err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return closeErr
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	c.dirty = false
	return nil
}
