package internal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// waOpenWAStub wires one local OpenWA stand-in into the server config.
func waOpenWAStub(t *testing.T, s *Server, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s.cfg.OpenwaBaseURL = srv.URL
	s.cfg.OpenwaSession = "kidversa-test"
	s.cfg.OpenwaAPIKey = "k"
}

func TestWaResolveSessionMatchesName(t *testing.T) {
	s, _ := newTestServer(t)
	var listHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		listHits.Add(1)
		io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test","status":"ready"}]`)
	})

	id, err := s.waResolveSession(context.Background())
	require.NoError(t, err)
	require.Equal(t, "uuid-1", id)
	id2, err := s.waResolveSession(context.Background())
	require.NoError(t, err)
	require.Equal(t, "uuid-1", id2)
	require.Equal(t, int32(1), listHits.Load(), "the resolved id must be cached")
}

func TestWaResolveSessionFallsBackToConfiguredValue(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	id, err := s.waResolveSession(context.Background())
	require.NoError(t, err)
	require.Equal(t, "kidversa-test", id)
}

func TestWaSessionStateNotFound(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/uuid-1" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := s.waSessionState(context.Background(), "uuid-1")
	require.ErrorIs(t, err, errWaMissing)
}

func TestWaQRNoCodeIsEmpty(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/uuid-1/qr" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})

	qr, err := s.waQR(context.Background(), "uuid-1")
	require.NoError(t, err)
	require.Equal(t, "", qr)
}

func TestWaStartAlreadyStartedIsOK(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/uuid-1/start" {
			t.Errorf("unexpected request %s %q", r.Method, r.URL.Path)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})

	require.NoError(t, s.waStart(context.Background(), "uuid-1"))
}

func TestWaStartServerError(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	require.Error(t, s.waStart(context.Background(), "uuid-1"))
}
