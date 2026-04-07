package contentcache

import (
	"container/list"
	pathpkg "path"
	"strings"
	"sync"
)

type Signature struct {
	Size    int64
	ModTime int64
}

type Cache struct {
	mu       sync.Mutex
	maxBytes int64
	used     int64
	lru      *list.List
	entries  map[string]*list.Element
}

type entry struct {
	path    string
	sig     Signature
	content []byte
}

func New(maxBytes int64) *Cache {
	return &Cache{
		maxBytes: maxBytes,
		lru:      list.New(),
		entries:  make(map[string]*list.Element),
	}
}

func (c *Cache) MaxBytes() int64 {
	if c == nil {
		return 0
	}
	return c.maxBytes
}

func (c *Cache) Get(relPath string, sig Signature) ([]byte, bool) {
	if c == nil || c.maxBytes <= 0 {
		return nil, false
	}

	key := normalize(relPath)

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	item, ok := elem.Value.(*entry)
	if !ok {
		c.removeElement(elem)
		return nil, false
	}
	if item.sig != sig {
		c.removeElement(elem)
		return nil, false
	}

	c.lru.MoveToFront(elem)
	return cloneBytes(item.content), true
}

func (c *Cache) Put(relPath string, sig Signature, content []byte) bool {
	if c == nil || c.maxBytes <= 0 {
		return false
	}

	key := normalize(relPath)
	size := int64(len(content))
	if size > c.maxBytes {
		c.Invalidate(key)
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.entries[key]; ok {
		c.removeElement(elem)
	}

	item := &entry{
		path:    key,
		sig:     sig,
		content: cloneBytes(content),
	}
	elem := c.lru.PushFront(item)
	c.entries[key] = elem
	c.used += int64(len(item.content))
	c.evictLocked()
	return true
}

func (c *Cache) Invalidate(relPath string) {
	if c == nil || c.maxBytes <= 0 {
		return
	}

	key := normalize(relPath)

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.entries[key]; ok {
		c.removeElement(elem)
	}
}

func (c *Cache) InvalidatePrefix(relPath string) {
	if c == nil || c.maxBytes <= 0 {
		return
	}

	key := normalize(relPath)

	c.mu.Lock()
	defer c.mu.Unlock()

	for path, elem := range c.entries {
		if matchesPrefix(path, key) {
			c.removeElement(elem)
		}
	}
}

func (c *Cache) Clear() {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.used = 0
	c.lru.Init()
	c.entries = make(map[string]*list.Element)
}

func (c *Cache) Len() int {
	if c == nil {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *Cache) evictLocked() {
	for c.used > c.maxBytes {
		back := c.lru.Back()
		if back == nil {
			return
		}
		c.removeElement(back)
	}
}

func (c *Cache) removeElement(elem *list.Element) {
	if elem == nil {
		return
	}
	item, ok := elem.Value.(*entry)
	if !ok {
		deleteUnknownElement(c.entries, elem)
		_ = c.lru.Remove(elem)
		return
	}
	delete(c.entries, item.path)
	_ = c.lru.Remove(elem)
	c.used -= int64(len(item.content))
	if c.used < 0 {
		c.used = 0
	}
}

func deleteUnknownElement(entries map[string]*list.Element, target *list.Element) {
	for key, elem := range entries {
		if elem == target {
			delete(entries, key)
			return
		}
	}
}

func matchesPrefix(path, prefix string) bool {
	if prefix == "" {
		return true
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func normalize(relPath string) string {
	relPath = strings.ReplaceAll(relPath, "\\", "/")
	if relPath == "" || relPath == "." || relPath == "/" {
		return ""
	}
	cleaned := pathpkg.Clean(relPath)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func cloneBytes(in []byte) []byte {
	if len(in) == 0 {
		return []byte{}
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}
