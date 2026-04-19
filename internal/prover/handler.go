package prover

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// Handler returns an http.Handler for the /v1/prove endpoint.
// If apiKey is non-empty, requests must include a matching X-API-Key header.
func (s *Service) Handler(apiKey string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}

		// API key check.
		if apiKey != "" {
			if r.Header.Get("X-API-Key") != apiKey {
				writeJSONError(w, http.StatusUnauthorized, "invalid or missing API key")
				return
			}
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "failed to read body")
			return
		}

		var req proveHTTPRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		// Decode hex session_pub.
		sessionPub, err := hex.DecodeString(trimHexPrefix(req.SessionPub))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid session_pub hex: "+err.Error())
			return
		}

		s.log.Info("prove request received",
			zap.String("session_pub", req.SessionPub[:16]+"..."))

		start := time.Now()
		result, err := s.Prove(&ProveRequest{
			JWT:        req.JWT,
			SessionPub: sessionPub,
		})
		elapsed := time.Since(start)

		if err != nil {
			s.log.Error("proof generation failed",
				zap.Duration("elapsed", elapsed),
				zap.String("error", err.Error()))
			writeJSONError(w, http.StatusInternalServerError, "proof generation failed: "+err.Error())
			return
		}

		s.log.Info("proof generated",
			zap.Duration("elapsed", elapsed),
			zap.Int("proof_size", len(result.Proof)),
			zap.String("iss", result.Iss),
			zap.String("sub", result.Sub))

		resp := proveHTTPResponse{
			Proof:       hex.EncodeToString(result.Proof),
			Sub:         result.Sub,
			Iss:         result.Iss,
			Exp:         result.Exp,
			Aud:         result.Aud,
			Azp:         result.Azp,
			JWKSModulus: hex.EncodeToString(result.JWKSModulus),
			SessionPub:  hex.EncodeToString(result.SessionPub),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
}

type proveHTTPRequest struct {
	JWT        string `json:"jwt"`
	SessionPub string `json:"session_pub"` // hex-encoded
}

type proveHTTPResponse struct {
	Proof       string `json:"proof"`        // hex
	Sub         string `json:"sub"`
	Iss         string `json:"iss"`
	Exp         uint64 `json:"exp"`
	Aud         string `json:"aud"`
	Azp         string `json:"azp"`
	JWKSModulus string `json:"jwks_modulus"` // hex
	SessionPub  string `json:"session_pub"`  // hex
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func trimHexPrefix(s string) string {
	if len(s) >= 2 && s[:2] == "0x" {
		return s[2:]
	}
	return s
}
