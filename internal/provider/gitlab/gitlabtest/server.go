package gitlabtest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

type Server struct {
	*httptest.Server
	mu         sync.Mutex
	ValidToken string
	RunnerID   int64
	Verified   []string
	Deleted    []string
	Unexpected []string
	FailWith   int
}

func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{ValidToken: "glrt-valid-token", RunnerID: 4242}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v4/runners/verify", func(w http.ResponseWriter, r *http.Request) {
		token := formToken(r)
		s.mu.Lock()
		s.Verified = append(s.Verified, token)
		fail := s.FailWith
		s.mu.Unlock()
		if fail != 0 {
			http.Error(w, `{"message":"boom"}`, fail)
			return
		}
		if token != s.ValidToken {
			http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":4242,"token":"` + token + `","token_expires_at":null}`))
	})
	mux.HandleFunc("DELETE /api/v4/runners", func(w http.ResponseWriter, r *http.Request) {
		token := formToken(r)
		s.mu.Lock()
		s.Deleted = append(s.Deleted, token)
		fail := s.FailWith
		s.mu.Unlock()
		if fail != 0 {
			http.Error(w, `{"message":"boom"}`, fail)
			return
		}
		if token != s.ValidToken {
			http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.Unexpected = append(s.Unexpected, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		http.Error(w, `{"message":"404 Not Found"}`, http.StatusNotFound)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func formToken(r *http.Request) string {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return ""
	}
	return values.Get("token")
}

func (s *Server) SetFailure(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FailWith = status
}

func (s *Server) VerifiedTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Verified...)
}

func (s *Server) DeletedTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Deleted...)
}

func (s *Server) ProjectURL(path string) string {
	return s.URL + "/" + path
}
