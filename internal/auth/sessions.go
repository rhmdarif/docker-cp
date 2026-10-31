package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

type Session struct {
	Token, CSRF string
	Expires     time.Time
}

// Sessions keeps sessions in memory only; a panel restart logs everyone out.
type Sessions struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	m   map[string]Session
}

func NewSessions(ttl time.Duration, now func() time.Time) *Sessions {
	return &Sessions{ttl: ttl, now: now, m: map[string]Session{}}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Sessions) Create() Session {
	sess := Session{Token: randomToken(), CSRF: randomToken(), Expires: s.now().Add(s.ttl)}
	s.mu.Lock()
	s.m[sess.Token] = sess
	s.mu.Unlock()
	return sess
}

// Lookup returns the session for token; an expired one is removed here.
func (s *Sessions) Lookup(token string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return Session{}, false
	}
	if s.now().After(sess.Expires) {
		delete(s.m, token)
		return Session{}, false
	}
	return sess, true
}

func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

// DeleteAll invalidates every session; called on password change.
func (s *Sessions) DeleteAll() {
	s.mu.Lock()
	s.m = map[string]Session{}
	s.mu.Unlock()
}

func ValidCSRF(sess Session, token string) bool {
	return subtle.ConstantTimeCompare([]byte(sess.CSRF), []byte(token)) == 1
}
