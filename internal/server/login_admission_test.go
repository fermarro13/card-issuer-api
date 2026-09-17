package server

import (
	"testing"
	"time"
)

func TestLoginAdmissionWindowAndSourceNormalization(t *testing.T) {
	now := time.Date(2026, time.September, 16, 0, 0, 0, 0, time.UTC)
	admission := newLoginAdmissionAt(func() time.Time { return now }, defaultLoginAttemptLimit)
	for range defaultLoginAttemptLimit {
		if allowed, _ := admission.allow("[2001:db8::1]:443"); !allowed {
			t.Fatal("attempt before limit was rejected")
		}
	}
	allowed, retryAfter := admission.allow("[2001:db8::1]:8443")
	if allowed || retryAfter != loginAttemptPeriod {
		t.Fatalf("limited attempt = allowed:%v retry-after:%s", allowed, retryAfter)
	}
	now = now.Add(loginAttemptPeriod)
	if allowed, _ := admission.allow("[2001:db8::1]:443"); !allowed {
		t.Fatal("attempt after window was rejected")
	}
}
