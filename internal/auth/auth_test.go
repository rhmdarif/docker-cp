package auth

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "rahasia123") {
		t.Error("correct password rejected")
	}
	if CheckPassword(h, "salah12345") {
		t.Error("wrong password accepted")
	}
	if _, err := HashPassword("pendek"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("err = %v, want ErrPasswordTooShort", err)
	}
}

func TestSessionsExpire(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	s := NewSessions(24*time.Hour, c.now)
	sess := s.Create()
	if sess.Token == "" || sess.CSRF == "" || sess.Token == sess.CSRF {
		t.Fatalf("bad session %+v", sess)
	}
	if got, ok := s.Lookup(sess.Token); !ok || got.CSRF != sess.CSRF {
		t.Fatal("lookup failed")
	}
	if !ValidCSRF(sess, sess.CSRF) || ValidCSRF(sess, "x") {
		t.Error("ValidCSRF wrong")
	}
	c.t = c.t.Add(24*time.Hour + time.Second)
	if _, ok := s.Lookup(sess.Token); ok {
		t.Error("expired session still valid")
	}
	if len(s.m) != 0 {
		t.Error("expired session not removed")
	}
}

func TestDeleteAll(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	s := NewSessions(time.Hour, c.now)
	a, b := s.Create(), s.Create()
	s.DeleteAll()
	for _, x := range []Session{a, b} {
		if _, ok := s.Lookup(x.Token); ok {
			t.Error("token valid after DeleteAll")
		}
	}
	d := s.Create()
	s.Delete(d.Token)
	if _, ok := s.Lookup(d.Token); ok {
		t.Error("token valid after Delete")
	}
}

func TestLimiter(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	l := NewLimiter(5, 15*time.Minute, c.now)
	for i := 0; i < 5; i++ {
		if !l.Allow("ip") {
			t.Fatalf("blocked early at %d", i)
		}
		l.Fail("ip")
	}
	if l.Allow("ip") {
		t.Error("should be blocked")
	}
	if !l.Allow("other") {
		t.Error("other key blocked")
	}
	c.t = c.t.Add(15*time.Minute + time.Second)
	if !l.Allow("ip") {
		t.Error("should be allowed after window")
	}
	for i := 0; i < 5; i++ {
		l.Fail("ip")
	}
	l.Reset("ip")
	if !l.Allow("ip") {
		t.Error("should be allowed after Reset")
	}
}

func TestLimiterTake(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	l := NewLimiter(5, 15*time.Minute, c.now)
	for i := 0; i < 5; i++ {
		if !l.Take("ip") {
			t.Fatalf("Take refused at %d", i)
		}
	}
	if l.Take("ip") || l.Allow("ip") {
		t.Error("6th Take should be refused")
	}
	if !l.Take("other") {
		t.Error("other key refused")
	}
	l.Reset("ip")
	if !l.Take("ip") {
		t.Error("refused after Reset")
	}
	for i := 0; i < 4; i++ {
		l.Take("ip")
	}
	c.t = c.t.Add(15*time.Minute + time.Second)
	if !l.Take("ip") {
		t.Error("refused after window")
	}
}

func TestLimiterTakeConcurrent(t *testing.T) {
	l := NewLimiter(5, time.Hour, time.Now)
	var wg sync.WaitGroup
	var granted atomic.Int32
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Take("ip") {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if g := granted.Load(); g != 5 {
		t.Fatalf("granted %d, want 5", g)
	}
}

func TestLimiterKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1": "2001:db8:1:2::/64",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"not-an-ip":            "not-an-ip",
	} {
		if got := LimiterKey(in); got != want {
			t.Errorf("LimiterKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLimiterBounded(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	l := NewLimiter(5, 15*time.Minute, c.now)
	for i := 0; i < 2000; i++ {
		l.Take(fmt.Sprintf("old-%d", i))
	}
	c.t = c.t.Add(16 * time.Minute)
	l.Take("fresh")
	if n := l.size(); n > 1024 {
		t.Fatalf("expired entries not pruned: %d", n)
	}
	for i := 0; i < maxLimiterKeys; i++ {
		l.Take(fmt.Sprintf("k-%d", i))
	}
	if !l.Take("one-too-many") {
		t.Error("new key at the cap must be admitted by evicting the oldest")
	}
	if n := l.size(); n > maxLimiterKeys {
		t.Fatalf("size %d > cap", n)
	}
	if !l.Take("k-1") {
		t.Error("existing key under its limit must still be allowed")
	}
}

func TestLimiterEvictsOldestWhenFull(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	l := NewLimiter(5, 15*time.Minute, c.now)
	l.Take("oldest")
	c.t = c.t.Add(time.Second)
	for i := 0; i < 5; i++ {
		l.Take("blocked")
	}
	for i := 0; i < maxLimiterKeys-2; i++ {
		c.t = c.t.Add(time.Millisecond)
		l.Take(fmt.Sprintf("k-%d", i))
	}
	if n := l.size(); n != maxLimiterKeys {
		t.Fatalf("setup: size %d, want %d", n, maxLimiterKeys)
	}
	c.t = c.t.Add(time.Millisecond)
	if !l.Take("newcomer") {
		t.Fatal("new key must be admitted when full")
	}
	if n := l.size(); n > maxLimiterKeys {
		t.Fatalf("size %d > cap", n)
	}
	if _, ok := l.fails["oldest"]; ok {
		t.Error("oldest key should have been evicted")
	}
	if l.Take("blocked") {
		t.Error("recently blocked key that is not the oldest must stay blocked")
	}
}
