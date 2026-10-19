package cache

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"sync"
	"time"

	"token-count/internal/model"
)

// Bumped whenever the shape or meaning of a cached record changes; v4 added
// deduplication keys for every agent, which entries cached earlier lack.
const version = 4

func init() {
	gob.Register(time.Time{})
	gob.Register(model.Usage{})
}

type entry struct {
	ModNano int64
	Size    int64
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
	return filepath.Join(root, "token-count", "index-v4.gob")
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

func (c *Cache) Get(path string) ([]model.Usage, bool) {
	if !c.enabled {
		return nil, false
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	c.mu.RLock()
	ent, ok := c.data.Files[path]
	c.mu.RUnlock()
	if !ok || ent.ModNano != st.ModTime().UnixNano() || ent.Size != st.Size() {
		return nil, false
	}
	return ent.Records, true
}

func (c *Cache) Put(path string, records []model.Usage) {
	if !c.enabled {
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.data.Files[path] = entry{
		ModNano: st.ModTime().UnixNano(),
		Size:    st.Size(),
		Records: records,
	}
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
