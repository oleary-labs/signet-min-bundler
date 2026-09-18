package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireAPIKey(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("reached"))
	})

	tests := []struct {
		name     string
		apiKey   string
		header   string
		wantCode int
		reached  bool
	}{
		{"matching key", "secret", "secret", http.StatusOK, true},
		{"missing key", "secret", "", http.StatusUnauthorized, false},
		{"wrong key", "secret", "nope", http.StatusUnauthorized, false},
		{"prefix of key", "secret", "secre", http.StatusUnauthorized, false},
		{"check disabled", "", "", http.StatusOK, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
			if tt.header != "" {
				req.Header.Set("X-API-Key", tt.header)
			}
			rec := httptest.NewRecorder()
			RequireAPIKey(tt.apiKey, ok).ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if got := rec.Body.String() == "reached"; got != tt.reached {
				t.Fatalf("reached handler = %v, want %v (body %q)", got, tt.reached, rec.Body.String())
			}
			if tt.reached {
				return
			}

			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var resp jsonrpcResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if resp.Error == nil || resp.Error.Code != ErrCodeUnauthorized {
				t.Errorf("error = %+v, want code %d", resp.Error, ErrCodeUnauthorized)
			}
		})
	}
}
