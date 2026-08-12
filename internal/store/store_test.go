package store

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClock lets tests advance time without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestPutGet(t *testing.T) {
	s := New(Options{})
	blob := []byte("sealed bytes")

	if err := s.Put("B4K7QW2M", blob); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("B4K7QW2M")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("Get = %q, want %q", got, blob)
	}
}

func TestGetUnknownCode(t *testing.T) {
	s := New(Options{})
	if _, err := s.Get("NOSUCH00"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestExpiry(t *testing.T) {
	c := newClock()
	s := New(Options{TTL: time.Minute, now: c.now})

	if err := s.Put("B4K7QW2M", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	c.advance(59 * time.Second)
	if _, err := s.Get("B4K7QW2M"); err != nil {
		t.Fatalf("Get just before expiry: %v", err)
	}

	c.advance(2 * time.Second)
	if _, err := s.Get("B4K7QW2M"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after expiry: err = %v, want ErrNotFound", err)
	}

	// The expired entry must also stop counting against the size budget.
	if n, b := s.Stats(); n != 0 || b != 0 {
		t.Errorf("Stats after expiry = (%d, %d), want (0, 0)", n, b)
	}
}

func TestFetchLimit(t *testing.T) {
	s := New(Options{MaxFetches: 2})
	if err := s.Put("B4K7QW2M", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	for i := 1; i <= 2; i++ {
		if _, err := s.Get("B4K7QW2M"); err != nil {
			t.Fatalf("Get #%d: %v", i, err)
		}
	}
	if _, err := s.Get("B4K7QW2M"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get past the limit: err = %v, want ErrNotFound", err)
	}
	if n, _ := s.Stats(); n != 0 {
		t.Errorf("live drops = %d, want 0", n)
	}
}

func TestBlobTooLarge(t *testing.T) {
	s := New(Options{MaxBlob: 16})
	if err := s.Put("B4K7QW2M", make([]byte, 17)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestTotalBudget(t *testing.T) {
	s := New(Options{MaxBlob: 100, MaxTotal: 150})

	if err := s.Put("AAAAAAAA", make([]byte, 100)); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := s.Put("BBBBBBBB", make([]byte, 100)); !errors.Is(err, ErrFull) {
		t.Fatalf("second Put: err = %v, want ErrFull", err)
	}

	// Collecting the first drop should free its budget again.
	if _, err := s.Get("AAAAAAAA"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	s.Delete("AAAAAAAA")
	if err := s.Put("BBBBBBBB", make([]byte, 100)); err != nil {
		t.Fatalf("Put after freeing space: %v", err)
	}
}

func TestDuplicateCode(t *testing.T) {
	s := New(Options{})
	if err := s.Put("B4K7QW2M", []byte("a")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Put("B4K7QW2M", []byte("b")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s := New(Options{})
	if err := s.Put("B4K7QW2M", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	s.Delete("B4K7QW2M")
	s.Delete("B4K7QW2M") // must not panic or corrupt the total
	if n, b := s.Stats(); n != 0 || b != 0 {
		t.Errorf("Stats = (%d, %d), want (0, 0)", n, b)
	}
}

func TestSweepReleasesBudget(t *testing.T) {
	c := newClock()
	s := New(Options{TTL: time.Minute, MaxTotal: 100, now: c.now})

	if err := s.Put("AAAAAAAA", make([]byte, 100)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	c.advance(2 * time.Minute)

	// Put sweeps first, so the expired drop must not block the new one.
	if err := s.Put("BBBBBBBB", make([]byte, 100)); err != nil {
		t.Fatalf("Put after expiry: %v", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := New(Options{MaxFetches: 1, MaxTotal: 1 << 20})

	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := fmt.Sprintf("CODE%04d", i)
			if err := s.Put(c, []byte("payload")); err != nil {
				t.Errorf("Put %s: %v", c, err)
				return
			}
			if _, err := s.Get(c); err != nil {
				t.Errorf("Get %s: %v", c, err)
			}
		}(i)
	}
	wg.Wait()

	if count, bytes := s.Stats(); count != 0 || bytes != 0 {
		t.Errorf("Stats = (%d, %d), want (0, 0) after every drop was collected", count, bytes)
	}
}
