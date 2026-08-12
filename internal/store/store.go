// Package store keeps sealed drops in memory until they are collected or expire.
//
// Nothing is written to disk. A drop is opaque ciphertext, and it disappears
// once its time to live runs out or the client has fetched it enough times,
// whichever comes first.
package store

import (
	"errors"
	"sync"
	"time"
)

// Defaults chosen for the screenshot workflow: long enough that you can get
// distracted between pasting and pulling, short enough that a guessed code has
// almost no window to be useful.
const (
	DefaultTTL        = 10 * time.Minute
	DefaultMaxFetches = 3
	DefaultMaxBlob    = 32 << 20  // 32 MiB per drop
	DefaultMaxTotal   = 256 << 20 // 256 MiB across all live drops
)

var (
	// ErrNotFound means the code is unknown, already collected, or expired.
	// Callers must not distinguish these cases to the client, since doing so
	// would let an attacker probe which codes exist.
	ErrNotFound = errors.New("store: drop not found")
	// ErrTooLarge means the blob exceeds MaxBlob.
	ErrTooLarge = errors.New("store: drop too large")
	// ErrFull means accepting the blob would exceed MaxTotal.
	ErrFull = errors.New("store: storage full")
	// ErrDuplicate means the code is already in use.
	ErrDuplicate = errors.New("store: code already in use")
)

// Options configures a Store. The zero value selects the defaults above.
type Options struct {
	TTL        time.Duration
	MaxFetches int
	MaxBlob    int64
	MaxTotal   int64

	// now is overridable so tests can advance time without sleeping.
	now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.MaxFetches <= 0 {
		o.MaxFetches = DefaultMaxFetches
	}
	if o.MaxBlob <= 0 {
		o.MaxBlob = DefaultMaxBlob
	}
	if o.MaxTotal <= 0 {
		o.MaxTotal = DefaultMaxTotal
	}
	if o.now == nil {
		o.now = time.Now
	}
	return o
}

type drop struct {
	blob    []byte
	expires time.Time
	fetches int
}

// Store is a concurrency safe set of live drops.
type Store struct {
	opt Options

	mu    sync.Mutex
	drops map[string]*drop
	total int64
}

// New returns a Store using opt, filling in defaults for the zero fields.
func New(opt Options) *Store {
	return &Store{
		opt:   opt.withDefaults(),
		drops: make(map[string]*drop),
	}
}

// TTL returns how long a drop created now stays live.
func (s *Store) TTL() time.Duration { return s.opt.TTL }

// Put stores blob under code. The caller owns generating the code and must not
// reuse one that is still live.
func (s *Store) Put(code string, blob []byte) error {
	size := int64(len(blob))
	if size > s.opt.MaxBlob {
		return ErrTooLarge
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()

	if _, ok := s.drops[code]; ok {
		return ErrDuplicate
	}
	if s.total+size > s.opt.MaxTotal {
		return ErrFull
	}

	s.drops[code] = &drop{blob: blob, expires: s.opt.now().Add(s.opt.TTL)}
	s.total += size
	return nil
}

// Get returns the blob for code and counts the fetch. The drop is removed once
// it has been fetched MaxFetches times.
func (s *Store) Get(code string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.drops[code]
	if !ok || !s.opt.now().Before(d.expires) {
		// Drop the expired entry eagerly rather than waiting for the janitor.
		if ok {
			s.removeLocked(code, d)
		}
		return nil, ErrNotFound
	}

	d.fetches++
	blob := d.blob
	if d.fetches >= s.opt.MaxFetches {
		s.removeLocked(code, d)
	}
	return blob, nil
}

// Delete removes a drop early. It is not an error if the code is unknown.
func (s *Store) Delete(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.drops[code]; ok {
		s.removeLocked(code, d)
	}
}

// Stats reports the number of live drops and the bytes they occupy.
func (s *Store) Stats() (count int, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	return len(s.drops), s.total
}

// Sweep discards expired drops. Run returns after ctx style cancellation via
// the stop channel; callers normally use it as a background janitor.
func (s *Store) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
}

// RunJanitor sweeps every interval until stop is closed.
func (s *Store) RunJanitor(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.Sweep()
		case <-stop:
			return
		}
	}
}

func (s *Store) sweepLocked() {
	now := s.opt.now()
	for c, d := range s.drops {
		if !now.Before(d.expires) {
			s.removeLocked(c, d)
		}
	}
}

func (s *Store) removeLocked(code string, d *drop) {
	delete(s.drops, code)
	s.total -= int64(len(d.blob))
}
