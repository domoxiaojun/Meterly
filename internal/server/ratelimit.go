package server

import (
	"sync"
	"time"
)

// limiter 按 key(来源 IP)的令牌桶。
type limiter struct {
	mu    sync.Mutex
	rate  float64 // 每秒补充
	burst float64
	m     map[string]*bucket
	last  time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(rate, burst float64) *limiter {
	return &limiter{rate: rate, burst: burst, m: map[string]*bucket{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.last) > time.Minute { // 周期清理闲置桶
		for k, b := range l.m {
			if now.Sub(b.at) > 10*time.Minute {
				delete(l.m, k)
			}
		}
		l.last = now
	}
	b := l.m[key]
	if b == nil {
		b = &bucket{tokens: l.burst, at: now}
		l.m[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
