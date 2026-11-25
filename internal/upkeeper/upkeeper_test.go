package upkeeper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPatrol(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method != http.MethodPost || r.URL.Path != "/patrol" || r.Header.Get("Authorization") != "Bearer s":
			w.WriteHeader(http.StatusUnauthorized)
		case asked:
			w.WriteHeader(http.StatusConflict)
		default:
			asked = true
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := New(srv.URL+"/", "s").Patrol(ctx); err != nil {
		t.Fatal(err)
	}
	if err := New(srv.URL, "s").Patrol(ctx); !errors.Is(err, ErrAsked) {
		t.Errorf("second ask: %v", err)
	}
	if err := New(srv.URL, "wrong").Patrol(ctx); err == nil || errors.Is(err, ErrAsked) {
		t.Errorf("wrong token: %v", err)
	}
}
