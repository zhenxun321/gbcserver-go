package httpapi

import (
	"sync"
	"time"
)

type webSession struct {
	username string
	expires  time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]webSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]webSession)}
}

func (s *sessionStore) Put(id, username string, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = webSession{username: username, expires: expires}
}

func (s *sessionStore) Get(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return "", false
	}
	if !session.expires.After(time.Now()) {
		delete(s.sessions, id)
		return "", false
	}
	return session.username, true
}

func (s *sessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *sessionStore) DeleteExcept(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sessionID := range s.sessions {
		if sessionID != id {
			delete(s.sessions, sessionID)
		}
	}
}

func (s *sessionStore) DeleteUser(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.sessions {
		if session.username == username {
			delete(s.sessions, id)
		}
	}
}
