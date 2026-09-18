package rpc

import (
	"crypto/subtle"
	"net/http"
)

// ErrCodeUnauthorized is returned when a request lacks a valid X-API-Key.
const ErrCodeUnauthorized = -32001

// RequireAPIKey wraps next so every request must carry an X-API-Key header
// matching apiKey. An empty apiKey disables the check.
//
// Without this, anyone who can reach the bundler can call pm_getPaymasterData
// and have the hot key sign sponsorships against the paymaster's deposit —
// only the paymaster's on-chain target check would stand in the way.
func RequireAPIKey(apiKey string, next http.Handler) http.Handler {
	if apiKey == "" {
		return next
	}
	want := []byte(apiKey)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("X-API-Key"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			// Headers must be set before WriteHeader; writeError's own Set
			// would otherwise be dropped.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			writeError(w, nil, &RpcError{Code: ErrCodeUnauthorized, Message: "invalid or missing API key"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
