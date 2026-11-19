package sourcemap

import (
	"container/list"
	"sync"
	"time"
)

type key struct {
	project int64
	release string
	file    string
}

type entry struct {
	key     key
	m       *Map // nil si no hay mapa o está roto
	size    int64
	expires time.Time
}

// cache es un LRU de mapas ya decodificados con tope de bytes aproximado.
// También recuerda un rato los ficheros sin mapa, para no ir a Postgres en
// cada evento.
type cache struct {
	mu    sync.Mutex
	max   int64
	size  int64
	ll    *list.List
	items map[key]*list.Element
	now   func() time.Time
}

func newCache(max int64) *cache {
	return &cache{max: max, ll: list.New(), items: map[key]*list.Element{}, now: time.Now}
}

func (c *cache) get(k key) (*Map, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return nil, false
	}
	e := el.Value.(*entry)
	if !e.expires.IsZero() && c.now().After(e.expires) {
		c.remove(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return e.m, true
}

func (c *cache) put(k key, m *Map) {
	c.add(&entry{key: k, m: m, size: m.size})
}

func (c *cache) putMissing(k key) {
	c.add(&entry{key: k, size: 128, expires: c.now().Add(missingTTL)})
}

func (c *cache) add(e *entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[e.key]; ok {
		c.remove(el)
	}
	if e.size > c.max {
		return
	}
	c.items[e.key] = c.ll.PushFront(e)
	c.size += e.size
	for c.size > c.max {
		c.remove(c.ll.Back())
	}
}

func (c *cache) invalidate(k key) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.remove(el)
	}
}

func (c *cache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = map[key]*list.Element{}
	c.size = 0
}

func (c *cache) remove(el *list.Element) {
	e := c.ll.Remove(el).(*entry)
	delete(c.items, e.key)
	c.size -= e.size
}
