package cache

import (
	"sync"
	"time"
)

type item[T any] struct {
	v   T
	exp time.Time
}

type TTLCache[T any] struct {
	mu      sync.RWMutex
	m       map[string]item[T]
	stopJan chan struct{}
	closed  bool
	janitor bool
}

func NewTTLCache[T any](cleanupInterval time.Duration) *TTLCache[T] {
	c := &TTLCache[T]{
		m:       make(map[string]item[T]),
		stopJan: make(chan struct{}),
	}
	if cleanupInterval > 0 {
		c.janitor = true
		go c.janitorLoop(cleanupInterval)
	}
	return c
}

func (c *TTLCache[T]) Put(key string, val T, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	c.m[key] = item[T]{v: val, exp: exp}
}

func (c *TTLCache[T]) Get(key string) (T, bool) {
	c.mu.RLock()
	it, ok := c.m[key]
	c.mu.RUnlock()
	if !ok {
		var zero T
		return zero, false
	}
	if !it.exp.IsZero() && time.Now().After(it.exp) {
		c.mu.Lock()
		if it2, ok2 := c.m[key]; ok2 && it2.exp == it.exp {
			delete(c.m, key)
		}
		c.mu.Unlock()
		var zero T
		return zero, false
	}
	return it.v, true
}

func (c *TTLCache[T]) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; ok {
		delete(c.m, key)
		return true
	}
	return false
}

func (c *TTLCache[T]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}

func (c *TTLCache[T]) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.stopJan)
	c.mu.Unlock()
}

func (c *TTLCache[T]) janitorLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			now := time.Now()
			c.mu.Lock()
			for k, it := range c.m {
				if !it.exp.IsZero() && now.After(it.exp) {
					delete(c.m, k)
				}
			}
			c.mu.Unlock()
		case <-c.stopJan:
			return
		}
	}
}
