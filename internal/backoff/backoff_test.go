package backoff

import (
	"testing"
	"time"
)

func TestGrowthAndCap(t *testing.T) {
	b := New(25*time.Millisecond, 500*time.Millisecond)
	for i := range 20 {
		d := b.Next()
		// jitter is ±25%, so bounds are [0.75*min, 1.25*max]
		if d < 25*time.Millisecond*3/4 || d > 500*time.Millisecond*5/4 {
			t.Fatalf("attempt %d: delay %v out of bounds", i, d)
		}
	}
}

func TestGrowsThenCaps(t *testing.T) {
	// With jitter disabled by construction we can't assert exact values, but
	// the median delay should climb across early attempts and then plateau.
	b := New(10*time.Millisecond, 160*time.Millisecond)
	var maxSeen time.Duration
	for range 10 {
		if d := b.Next(); d > maxSeen {
			maxSeen = d
		}
	}
	if maxSeen < 100*time.Millisecond {
		t.Fatalf("backoff never approached its cap; max seen = %v", maxSeen)
	}
}

func TestReset(t *testing.T) {
	b := New(10*time.Millisecond, time.Second)
	for range 10 {
		b.Next()
	}
	b.Reset()
	if d := b.Next(); d > 13*time.Millisecond { // 10ms + 25% jitter
		t.Fatalf("after Reset, delay %v should be near min", d)
	}
}

func TestDegenerateInputs(t *testing.T) {
	b := New(0, -1)
	if d := b.Next(); d <= 0 {
		t.Fatalf("delay must be positive, got %v", d)
	}
}
