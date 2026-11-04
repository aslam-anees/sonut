package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestSlidingWindowBlocksAfterLimit(t *testing.T) {
	limiter := NewMemoryLimiter()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		allowed, err := limiter.AllowSlidingWindow(ctx, "user:1", 3, time.Minute, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Fatalf("request %d should be allowed within limit", i)
		}
	}

	allowed, err := limiter.AllowSlidingWindow(ctx, "user:1", 3, time.Minute, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatalf("4th request should be blocked once limit is reached")
	}
}

func TestFixedWindowResetsAfterTTL(t *testing.T) {
	limiter := NewMemoryLimiter()
	fakeNow := time.Now()
	limiter.now = func() time.Time { return fakeNow }
	ctx := context.Background()

	allowed, err := limiter.AllowFixedWindow(ctx, []string{"ip:1.2.3.4"}, 1, time.Minute)
	if err != nil || !allowed {
		t.Fatalf("first request should be allowed: allowed=%v err=%v", allowed, err)
	}

	allowed, err = limiter.AllowFixedWindow(ctx, []string{"ip:1.2.3.4"}, 1, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatalf("second request within the window should be blocked")
	}

	fakeNow = fakeNow.Add(2 * time.Minute)
	allowed, err = limiter.AllowFixedWindow(ctx, []string{"ip:1.2.3.4"}, 1, time.Minute)
	if err != nil || !allowed {
		t.Fatalf("request after TTL expiry should be allowed: allowed=%v err=%v", allowed, err)
	}
}
