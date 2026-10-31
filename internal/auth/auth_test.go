package auth

import (
	"errors"
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
