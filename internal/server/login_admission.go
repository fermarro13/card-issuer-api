package server

import (
	"net"
	"sync"
	"time"
)

const (
	defaultLoginAttemptLimit = 5
	loginAttemptPeriod       = time.Minute
	loginAdmissionMaxIPs     = 10_000
)

type loginAdmission struct {
	mu       sync.Mutex
	now      func() time.Time
	limit    int
	attempts map[string]loginAttemptWindow
}

type loginAttemptWindow struct {
	startedAt time.Time
	count     int
}

func newLoginAdmissionWithLimit(limit int) *loginAdmission {
	if limit < 1 {
		limit = defaultLoginAttemptLimit
	}
	return newLoginAdmissionAt(time.Now, limit)
}

func newLoginAdmissionAt(now func() time.Time, limit int) *loginAdmission {
	return &loginAdmission{now: now, limit: limit, attempts: make(map[string]loginAttemptWindow)}
}

func (a *loginAdmission) allow(remoteAddress string) (bool, time.Duration) {
	now := a.now()
	key := loginClientIP(remoteAddress)

	a.mu.Lock()
	defer a.mu.Unlock()

	attempt, exists := a.attempts[key]
	if exists && now.Sub(attempt.startedAt) >= loginAttemptPeriod {
		delete(a.attempts, key)
		exists = false
	}
	if !exists {
		if len(a.attempts) >= loginAdmissionMaxIPs {
			a.evictExpired(now)
			if len(a.attempts) >= loginAdmissionMaxIPs {
				return false, loginAttemptPeriod
			}
		}
		a.attempts[key] = loginAttemptWindow{startedAt: now, count: 1}
		return true, 0
	}
	if attempt.count >= a.limit {
		return false, loginAttemptPeriod - now.Sub(attempt.startedAt)
	}
	attempt.count++
	a.attempts[key] = attempt
	return true, 0
}

func (a *loginAdmission) evictExpired(now time.Time) {
	for ip, attempt := range a.attempts {
		if now.Sub(attempt.startedAt) >= loginAttemptPeriod {
			delete(a.attempts, ip)
		}
	}
}

func loginClientIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return "unknown"
}
