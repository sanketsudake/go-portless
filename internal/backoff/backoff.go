// Package backoff implements a small exponential backoff with jitter used by
// the registry dial loop and backends.
package backoff

import (
	"math/rand/v2"
	"time"
)

// Backoff produces exponentially growing delays from Min to Max with up to
// 25% random jitter. The zero value is unusable; use New.
type Backoff struct {
	min, max time.Duration
	attempt  int
}

// New returns a Backoff growing from min to max.
func New(min, max time.Duration) *Backoff {
	if min <= 0 {
		min = time.Millisecond
	}
	if max < min {
		max = min
	}
	return &Backoff{min: min, max: max}
}

// Next returns the next delay and advances the attempt counter.
func (b *Backoff) Next() time.Duration {
	d := b.min << b.attempt
	if d > b.max || d <= 0 { // <= 0 guards shift overflow
		d = b.max
	} else {
		b.attempt++
	}
	// jitter: d ± up to 25%
	j := time.Duration(rand.Int64N(int64(d)/2+1)) - d/4
	return d + j
}

// Reset restarts the sequence from min.
func (b *Backoff) Reset() { b.attempt = 0 }
