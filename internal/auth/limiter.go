package auth

import (
	"sync"
	"time"
)

type Limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	lockout  time.Duration
	now      func() time.Time
	failures map[string]*attempt
}

type attempt struct {
	count       int
	first       time.Time
	lockedUntil time.Time
}

func NewLimiter(maxFailures int, window, lockout time.Duration) *Limiter {
	return &Limiter{max: maxFailures, window: window, lockout: lockout, now: time.Now, failures: map[string]*attempt{}}
}

func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.failures[key]
	if !ok {
		return false, 0
	}
	now := l.now()
	if now.Before(a.lockedUntil) {
		return true, a.lockedUntil.Sub(now)
	}
	if now.Sub(a.first) > l.window {
		delete(l.failures, key)
	}
	return false, 0
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a, ok := l.failures[key]
	if !ok || now.Sub(a.first) > l.window {
		a = &attempt{first: now}
		l.failures[key] = a
	}
	a.count++
	if a.count >= l.max {
		a.lockedUntil = now.Add(l.lockout)
		a.count = 0
		a.first = now
	}
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}
