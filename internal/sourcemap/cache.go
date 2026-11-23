package sourcemap

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

// key busca un mapa por debug ID o, si no lo tiene, por release, dist y fichero.
type key struct {
	project int64
	debugID string
	release string
	dist    string
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

// put guarda m durante mapTTL; dice false si no cabe y no lo guarda.
func (c *cache) put(k key, m *Map) bool {
	return c.add(&entry{key: k, m: m, size: m.size + keySize(k), expires: c.now().Add(mapTTL)})
}

// putMissing recuerda que k no tiene mapa durante missingTTL.
func (c *cache) putMissing(k key) {
	c.add(&entry{key: k, size: keySize(k), expires: c.now().Add(missingTTL)})
}

// keySize es lo que retiene una entrada además de su mapa.
func keySize(k key) int64 {
	return 128 + int64(len(k.debugID)+len(k.release)+len(k.dist)+len(k.file))
}

// maxKeySize es keySize de la clave más larga con la que se busca un mapa.
const maxKeySize = 128 + 3*maxKeyLen

func (c *cache) add(e *entry) bool {
	// Las cadenas de la clave pueden venir de un evento entero; se copian
	// para no retenerlo.
	e.key = key{project: e.key.project, debugID: strings.Clone(e.key.debugID), release: strings.Clone(e.key.release), dist: strings.Clone(e.key.dist), file: strings.Clone(e.key.file)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[e.key]; ok {
		c.remove(el)
	}
	if e.size > c.max {
		return false
	}
	c.items[e.key] = c.ll.PushFront(e)
	c.size += e.size
	for c.size > c.max {
		c.remove(c.ll.Back())
	}
	return true
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
