// Package prover wraps the Noir/Barretenberg ZK proof pipeline for JWT
// authentication. It takes a raw JWT + session public key and produces an
// UltraHonk proof that the JWT is valid without revealing its contents.
//
// Requires `nargo` and `bb` (Barretenberg) on PATH and a pre-compiled
// circuit directory (the jwt_auth circuit from signet-protocol).
package prover

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// MaxDataLength is the circuit's maximum signed data (header.payload) size.
const MaxDataLength = 1024

// BoundedVecMaxLen is the max length for string BoundedVec fields in the circuit.
const BoundedVecMaxLen = 128

// Service generates ZK proofs for JWT authentication.
type Service struct {
	circuitDir string
	nargoPath  string
	bbPath     string
	log        *zap.Logger
}

// New creates a prover Service.
// circuitDir is the path to the compiled jwt_auth circuit (must contain target/jwt_auth.json).
func New(circuitDir string, log *zap.Logger) (*Service, error) {
	// Verify circuit artifacts exist.
	circuitJSON := filepath.Join(circuitDir, "target", "jwt_auth.json")
	if _, err := os.Stat(circuitJSON); err != nil {
		return nil, fmt.Errorf("compiled circuit not found at %s: %w", circuitJSON, err)
	}

	// Resolve nargo and bb paths.
	nargoPath, err := findBin("nargo", "~/.nargo/bin/nargo")
	if err != nil {
		return nil, err
	}
	bbPath, err := findBin("bb", "~/.bb/bb")
	if err != nil {
		return nil, err
	}

	return &Service{
		circuitDir: circuitDir,
		nargoPath:  nargoPath,
		bbPath:     bbPath,
		log:        log,
	}, nil
}

// ProveRequest is the input to the proof generation API.
type ProveRequest struct {
	JWT        string `json:"jwt"`         // Raw JWT (header.payload.signature)
	SessionPub []byte `json:"session_pub"` // 33-byte compressed secp256k1 key
}

// ProveResult is the output of the proof generation API.
type ProveResult struct {
	Proof        []byte `json:"proof"`         // UltraHonk proof bytes
	Sub          string `json:"sub"`           // JWT subject claim
	Iss          string `json:"iss"`           // JWT issuer
	Exp          uint64 `json:"exp"`           // JWT expiry
	Aud          string `json:"aud"`           // JWT audience
	Azp          string `json:"azp"`           // JWT authorized party
	JWKSModulus  []byte `json:"jwks_modulus"`  // RSA modulus bytes
	SessionPub   []byte `json:"session_pub"`   // Echo back for convenience
}

// Prove generates a ZK proof for the given JWT and session key.
func (s *Service) Prove(req *ProveRequest) (*ProveResult, error) {
	if len(req.SessionPub) != 33 {
		return nil, fmt.Errorf("session_pub must be 33 bytes (compressed secp256k1)")
	}

	// 1. Parse JWT.
	parts := strings.SplitN(req.JWT, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT: expected 3 parts")
	}

	signingInput := parts[0] + "." + parts[1]

	// Decode payload to extract claims.
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}

	var claims struct {
		Iss string `json:"iss"`
		Sub string `json:"sub"`
		Aud string `json:"aud"`
		Azp string `json:"azp"`
		Exp uint64 `json:"exp"`
	}
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, fmt.Errorf("parse JWT claims: %w", err)
	}

	// Decode signature.
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode JWT signature: %w", err)
	}

	// 2. Fetch JWKS to get RSA public key.
	pubKey, err := fetchJWKSPublicKey(claims.Iss, parts[0])
	if err != nil {
		return nil, fmt.Errorf("fetch JWKS: %w", err)
	}

	// 3. Compute circuit inputs.
	modulus := pubKey.N
	redcParam := new(big.Int).Lsh(big.NewInt(1), 2*2048+4)
	redcParam.Div(redcParam, modulus)
	sigBigInt := new(big.Int).SetBytes(sigBytes)

	modulusLimbs := splitBigIntToLimbs(modulus, 120, 18)
	redcLimbs := splitBigIntToLimbs(redcParam, 120, 18)
	sigLimbs := splitBigIntToLimbs(sigBigInt, 120, 18)

	headerB64 := parts[0]
	base64DecodeOffset := len(headerB64) + 1

	dataBytes := []byte(signingInput)
	if len(dataBytes) > MaxDataLength {
		return nil, fmt.Errorf("signed data too long: %d > %d", len(dataBytes), MaxDataLength)
	}

	dataStorage := make([]int, MaxDataLength)
	for i, b := range dataBytes {
		dataStorage[i] = int(b)
	}

	sessionPub := make([]int, 33)
	for i, b := range req.SessionPub {
		sessionPub[i] = int(b)
	}

	// 4. Generate a unique ID for this request to isolate concurrent runs.
	reqID := randomID()

	proverName := "Prover_" + reqID
	witnessName := reqID + "_witness"
	proofDir := reqID + "_proof"

	// Clean up per-request files when done.
	defer s.cleanupRequest(reqID, proverName, witnessName, proofDir)

	// Write Prover_<id>.toml.
	proverPath := filepath.Join(s.circuitDir, proverName+".toml")
	if err := writeProverToml(proverPath, proverData{
		DataStorage:        dataStorage,
		DataLen:            len(dataBytes),
		Base64DecodeOffset: base64DecodeOffset,
		ModulusLimbs:       modulusLimbs,
		RedcLimbs:          redcLimbs,
		SigLimbs:           sigLimbs,
		Iss:                claims.Iss,
		Sub:                claims.Sub,
		Exp:                claims.Exp,
		Aud:                claims.Aud,
		Azp:                claims.Azp,
		SessionPub:         sessionPub,
	}); err != nil {
		return nil, fmt.Errorf("write Prover.toml: %w", err)
	}

	// 5. Generate witness.
	s.log.Debug("generating witness", zap.String("req_id", reqID))
	if err := s.runCmd("nargo", "execute", "-p", proverName, witnessName); err != nil {
		return nil, fmt.Errorf("nargo execute: %w", err)
	}

	// 6. Generate proof.
	// bb doesn't create the output directory — do it ourselves.
	if err := os.MkdirAll(filepath.Join(s.circuitDir, "target", proofDir), 0755); err != nil {
		return nil, fmt.Errorf("create proof dir: %w", err)
	}
	s.log.Debug("generating proof", zap.String("req_id", reqID))
	if err := s.runCmd("bb", "prove",
		"-b", "target/jwt_auth.json",
		"-w", "target/"+witnessName+".gz",
		"-o", "target/"+proofDir,
		"--write_vk"); err != nil {
		return nil, fmt.Errorf("bb prove: %w", err)
	}

	// 7. Read proof bytes.
	// bb prove outputs the combined format: [4-byte BE field count][public inputs][proof].
	// The node expects just the proof portion — it reconstructs public inputs itself.
	// Strip the 4-byte header and public inputs to match the bb.js format.
	fullProof, err := os.ReadFile(filepath.Join(s.circuitDir, "target", proofDir, "proof"))
	if err != nil {
		return nil, fmt.Errorf("read proof: %w", err)
	}
	proofBytes, err := stripPublicInputs(fullProof)
	if err != nil {
		return nil, fmt.Errorf("strip public inputs: %w", err)
	}

	return &ProveResult{
		Proof:       proofBytes,
		Sub:         claims.Sub,
		Iss:         claims.Iss,
		Exp:         claims.Exp,
		Aud:         claims.Aud,
		Azp:         claims.Azp,
		JWKSModulus: modulus.Bytes(),
		SessionPub:  req.SessionPub,
	}, nil
}

func (s *Service) runCmd(name string, args ...string) error {
	// Resolve to full path for nargo/bb.
	binPath := name
	switch name {
	case "nargo":
		binPath = s.nargoPath
	case "bb":
		binPath = s.bbPath
	}
	cmd := exec.Command(binPath, args...)
	cmd.Dir = s.circuitDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, string(out))
	}
	return nil
}

// findBin locates a binary by checking PATH first, then a well-known fallback path.
func findBin(name, fallback string) (string, error) {
	// Check PATH first.
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	// Try the fallback path (expand ~).
	if strings.HasPrefix(fallback, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			fallback = home + fallback[1:]
		}
	}

	if _, err := os.Stat(fallback); err == nil {
		return fallback, nil
	}

	return "", fmt.Errorf("%s not found on PATH or at %s", name, fallback)
}

// totalPIElements is the number of public input field elements in the circuit.
// Must match the circuit's public input declaration:
//   18 modulus limbs + 4×(128 storage + 1 len) + 1 exp + 33 session_pub = 568
const totalPIElements = 568
const fieldElementSize = 32

// stripPublicInputs removes the 4-byte size header and public inputs from
// the bb prove output, returning just the proof portion. This matches the
// format returned by bb.js's generateProof (proof only, no public inputs).
func stripPublicInputs(fullProof []byte) ([]byte, error) {
	piBytes := totalPIElements * fieldElementSize // 568 × 32 = 18176
	headerSize := 4
	offset := headerSize + piBytes
	if len(fullProof) <= offset {
		return nil, fmt.Errorf("proof file too small: %d bytes, expected > %d", len(fullProof), offset)
	}
	return fullProof[offset:], nil
}

// cleanupRequest removes per-request files from the circuit directory.
func (s *Service) cleanupRequest(reqID, proverName, witnessName, proofDir string) {
	os.Remove(filepath.Join(s.circuitDir, proverName+".toml"))
	os.Remove(filepath.Join(s.circuitDir, "target", witnessName+".gz"))
	os.RemoveAll(filepath.Join(s.circuitDir, "target", proofDir))
}

// randomID returns a short random hex string for request isolation.
func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ── Witness encoding ────────────────────────────────────────────────────

type proverData struct {
	DataStorage        []int
	DataLen            int
	Base64DecodeOffset int
	ModulusLimbs       []string
	RedcLimbs          []string
	SigLimbs           []string
	Iss                string
	Sub                string
	Exp                uint64
	Aud                string
	Azp                string
	SessionPub         []int
}

func writeProverToml(path string, d proverData) error {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("base64_decode_offset = %d\n", d.Base64DecodeOffset))
	b.WriteString(fmt.Sprintf("expected_exp = %d\n", d.Exp))
	b.WriteString(fmt.Sprintf("redc_params_limbs = [%s]\n", joinQuoted(d.RedcLimbs)))
	b.WriteString(fmt.Sprintf("signature_limbs = [%s]\n", joinQuoted(d.SigLimbs)))
	b.WriteString(fmt.Sprintf("pubkey_modulus_limbs = [%s]\n", joinQuoted(d.ModulusLimbs)))
	b.WriteString(fmt.Sprintf("session_pub = [%s]\n\n", joinInts(d.SessionPub)))

	b.WriteString("[data]\n")
	b.WriteString(fmt.Sprintf("storage = [%s]\n", joinInts(d.DataStorage)))
	b.WriteString(fmt.Sprintf("len = %d\n\n", d.DataLen))
	writeBoundedVec(&b, "expected_iss", d.Iss, BoundedVecMaxLen)
	writeBoundedVec(&b, "expected_sub", d.Sub, BoundedVecMaxLen)
	writeBoundedVec(&b, "expected_aud", d.Aud, BoundedVecMaxLen)
	writeBoundedVec(&b, "expected_azp", d.Azp, BoundedVecMaxLen)

	return os.WriteFile(path, []byte(b.String()), 0644)
}

func writeBoundedVec(b *strings.Builder, name, value string, maxLen int) {
	storage := make([]int, maxLen)
	for i, c := range []byte(value) {
		storage[i] = int(c)
	}
	b.WriteString(fmt.Sprintf("[%s]\n", name))
	b.WriteString(fmt.Sprintf("storage = [%s]\n", joinInts(storage)))
	b.WriteString(fmt.Sprintf("len = %d\n\n", len(value)))
}

func splitBigIntToLimbs(n *big.Int, chunkBits, numChunks int) []string {
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(chunkBits)), big.NewInt(1))
	limbs := make([]string, numChunks)
	tmp := new(big.Int).Set(n)
	for i := 0; i < numChunks; i++ {
		limb := new(big.Int).And(tmp, mask)
		limbs[i] = limb.Text(10)
		tmp.Rsh(tmp, uint(chunkBits))
	}
	return limbs
}

func joinInts(vals []int) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, ", ")
}

func joinQuoted(vals []string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprintf("\"%s\"", v)
	}
	return strings.Join(parts, ", ")
}

// ── JWKS fetching ───────────────────────────────────────────────────────

// fetchJWKSPublicKey fetches the RSA public key from the issuer's OIDC
// discovery endpoint, matching the kid from the JWT header.
func fetchJWKSPublicKey(issuer, headerB64 string) (*rsa.PublicKey, error) {
	// Decode JWT header to get kid.
	headerJSON, err := base64.RawURLEncoding.DecodeString(headerB64)
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}

	var header struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported algorithm: %s (need RS256)", header.Alg)
	}

	// Fetch OIDC discovery document.
	discoveryURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	oidcConfig, err := httpGetJSON(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("fetch OIDC config: %w", err)
	}

	jwksURI, ok := oidcConfig["jwks_uri"].(string)
	if !ok {
		return nil, fmt.Errorf("no jwks_uri in OIDC config")
	}

	// Fetch JWKS.
	jwksData, err := httpGetJSON(jwksURI)
	if err != nil {
		return nil, fmt.Errorf("fetch JWKS: %w", err)
	}

	keys, ok := jwksData["keys"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("no keys in JWKS")
	}

	// Find the matching key.
	for _, k := range keys {
		key, ok := k.(map[string]interface{})
		if !ok {
			continue
		}
		kid, _ := key["kid"].(string)
		kty, _ := key["kty"].(string)
		if kid == header.Kid && kty == "RSA" {
			return parseJWK(key)
		}
	}

	return nil, fmt.Errorf("no matching RSA key found for kid=%s", header.Kid)
}

func parseJWK(key map[string]interface{}) (*rsa.PublicKey, error) {
	nB64, _ := key["n"].(string)
	eB64, _ := key["e"].(string)
	if nB64 == "" || eB64 == "" {
		return nil, fmt.Errorf("missing n or e in JWK")
	}

	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, fmt.Errorf("decode JWK n: %w", err)
	}

	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, fmt.Errorf("decode JWK e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := int(new(big.Int).SetBytes(eBytes).Int64())

	return &rsa.PublicKey{N: n, E: e}, nil
}
