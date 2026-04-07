package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
	"github.com/oleary-labs/signet-min-bundler/internal/estimator"
	"github.com/oleary-labs/signet-min-bundler/internal/mempool"
	"github.com/oleary-labs/signet-min-bundler/internal/validator"
	"go.uber.org/zap"
)

var (
	testEntryPoint = common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")
	testUSDC       = common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	testSender     = common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
)

type mockEstClient struct{}

func (c *mockEstClient) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	return 50_000, nil
}

func setupServer(t *testing.T) (*Server, mempool.Repository) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	repo, err := mempool.Open(path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })

	v := validator.New(
		[]common.Address{testUSDC},
		nil,
		50_000,
		500_000,
	)

	est := estimator.New(&mockEstClient{})

	methods := NewMethods(
		MethodsConfig{
			EntryPoints: []common.Address{testEntryPoint},
			ChainID:     1,
		},
		v, repo, est, zap.NewNop(),
	)

	return NewServer(methods, zap.NewNop()), repo
}

func rpcCall(t *testing.T, srv *Server, method string, params ...any) jsonrpcResponse {
	t.Helper()
	rawParams := make([]json.RawMessage, len(params))
	for i, p := range params {
		b, _ := json.Marshal(p)
		rawParams[i] = b
	}

	reqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  rawParams,
	})

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.Handler().ServeHTTP(w, req)

	var resp jsonrpcResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, w.Body.String())
	}
	return resp
}

func validUserOp() map[string]any {
	target := testUSDC
	calldata := core.BuildExecuteCalldata(target, big.NewInt(0), []byte{})
	sig := make([]byte, 65)
	return map[string]any{
		"sender":               testSender.Hex(),
		"nonce":                "0x0",
		"callData":             core.BytesToHex(calldata),
		"callGasLimit":         "0xc350",
		"verificationGasLimit": "0x7918",
		"preVerificationGas":   "0xc350",
		"maxFeePerGas":         "0xba43b7400",
		"maxPriorityFeePerGas": "0x3b9aca00",
		"signature":            core.BytesToHex(sig),
	}
}

func TestSendUserOperation(t *testing.T) {
	srv, _ := setupServer(t)
	resp := rpcCall(t, srv, "eth_sendUserOperation", validUserOp(), testEntryPoint.Hex())

	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}

	// Result should be a hex string (the userOpHash).
	var hash string
	b, _ := json.Marshal(resp.Result)
	json.Unmarshal(b, &hash)
	if len(hash) != 66 { // 0x + 64 hex chars
		t.Errorf("hash = %q, expected 66-char hex string", hash)
	}
}

func TestSendUserOperationWrongEntryPoint(t *testing.T) {
	srv, _ := setupServer(t)
	wrongEP := common.HexToAddress("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddead")
	resp := rpcCall(t, srv, "eth_sendUserOperation", validUserOp(), wrongEP.Hex())

	if resp.Error == nil {
		t.Fatal("expected error for wrong entry point")
	}
	if resp.Error.Code != -32521 {
		t.Errorf("error code = %d, want -32521", resp.Error.Code)
	}
}

func TestSendUserOperationBadSignatureLength(t *testing.T) {
	srv, _ := setupServer(t)
	op := validUserOp()
	op["signature"] = "0xdead" // too short
	resp := rpcCall(t, srv, "eth_sendUserOperation", op, testEntryPoint.Hex())

	if resp.Error == nil {
		t.Fatal("expected error for bad signature length")
	}
	if resp.Error.Code != -32521 {
		t.Errorf("error code = %d, want -32521", resp.Error.Code)
	}
}

func TestSendUserOperationForbiddenTarget(t *testing.T) {
	srv, _ := setupServer(t)
	op := validUserOp()
	forbidden := common.HexToAddress("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddead")
	calldata := core.BuildExecuteCalldata(forbidden, big.NewInt(0), []byte{})
	op["callData"] = core.BytesToHex(calldata)
	resp := rpcCall(t, srv, "eth_sendUserOperation", op, testEntryPoint.Hex())

	if resp.Error == nil {
		t.Fatal("expected error for forbidden target")
	}
	if resp.Error.Code != -32521 {
		t.Errorf("error code = %d, want -32521", resp.Error.Code)
	}
}

func TestSendUserOperationDuplicate(t *testing.T) {
	srv, _ := setupServer(t)

	// First submission succeeds.
	resp := rpcCall(t, srv, "eth_sendUserOperation", validUserOp(), testEntryPoint.Hex())
	if resp.Error != nil {
		t.Fatalf("first submit: %v", resp.Error)
	}

	// Same op again should fail (duplicate sender+nonce).
	resp = rpcCall(t, srv, "eth_sendUserOperation", validUserOp(), testEntryPoint.Hex())
	if resp.Error == nil {
		t.Error("expected error for duplicate op")
	}
}

func TestEstimateUserOperationGas(t *testing.T) {
	srv, _ := setupServer(t)
	resp := rpcCall(t, srv, "eth_estimateUserOperationGas", validUserOp(), testEntryPoint.Hex())

	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}

	b, _ := json.Marshal(resp.Result)
	var result map[string]string
	json.Unmarshal(b, &result)

	if result["verificationGasLimit"] == "" {
		t.Error("verificationGasLimit missing")
	}
	if result["callGasLimit"] == "" {
		t.Error("callGasLimit missing")
	}
	if result["preVerificationGas"] == "" {
		t.Error("preVerificationGas missing")
	}
}

func TestGetUserOperationByHash(t *testing.T) {
	srv, _ := setupServer(t)

	// Submit an op first.
	resp := rpcCall(t, srv, "eth_sendUserOperation", validUserOp(), testEntryPoint.Hex())
	if resp.Error != nil {
		t.Fatalf("submit: %v", resp.Error)
	}
	var hash string
	b, _ := json.Marshal(resp.Result)
	json.Unmarshal(b, &hash)

	// Fetch it.
	resp = rpcCall(t, srv, "eth_getUserOperationByHash", hash)
	if resp.Error != nil {
		t.Fatalf("getByHash: %v", resp.Error)
	}
	if resp.Result == nil {
		t.Error("expected non-null result")
	}

	// Verify the result contains sender.
	b, _ = json.Marshal(resp.Result)
	var result map[string]any
	json.Unmarshal(b, &result)
	if result["sender"] == nil {
		t.Error("sender field missing")
	}
}

func TestGetUserOperationByHashNotFound(t *testing.T) {
	srv, _ := setupServer(t)
	resp := rpcCall(t, srv, "eth_getUserOperationByHash", "0x0000000000000000000000000000000000000000000000000000000000000000")

	// Should return null result, no error.
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if resp.Result != nil {
		t.Error("expected null result for unknown hash")
	}
}

func TestGetUserOperationReceiptNotConfirmed(t *testing.T) {
	srv, _ := setupServer(t)

	// Submit an op (status = pending, not confirmed).
	resp := rpcCall(t, srv, "eth_sendUserOperation", validUserOp(), testEntryPoint.Hex())
	if resp.Error != nil {
		t.Fatalf("submit: %v", resp.Error)
	}
	var hash string
	b, _ := json.Marshal(resp.Result)
	json.Unmarshal(b, &hash)

	// Receipt should be null (not yet confirmed).
	resp = rpcCall(t, srv, "eth_getUserOperationReceipt", hash)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if resp.Result != nil {
		t.Error("expected null for pending op")
	}
}

func TestSupportedEntryPoints(t *testing.T) {
	srv, _ := setupServer(t)
	resp := rpcCall(t, srv, "eth_supportedEntryPoints")

	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}

	b, _ := json.Marshal(resp.Result)
	var eps []string
	json.Unmarshal(b, &eps)

	if len(eps) != 1 {
		t.Fatalf("got %d entry points, want 1", len(eps))
	}
}

func TestChainId(t *testing.T) {
	srv, _ := setupServer(t)
	resp := rpcCall(t, srv, "eth_chainId")

	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}

	var chainId string
	b, _ := json.Marshal(resp.Result)
	json.Unmarshal(b, &chainId)

	if chainId != "0x1" {
		t.Errorf("chainId = %s, want 0x1", chainId)
	}
}

func TestMethodNotFound(t *testing.T) {
	srv, _ := setupServer(t)
	resp := rpcCall(t, srv, "eth_nonExistentMethod")

	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
}

func TestInvalidJSON(t *testing.T) {
	srv, _ := setupServer(t)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	var resp jsonrpcResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != -32600 {
		t.Errorf("expected -32600 for invalid JSON, got %v", resp.Error)
	}
}

func TestGetMethodRejected(t *testing.T) {
	srv, _ := setupServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	var resp jsonrpcResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != -32600 {
		t.Errorf("expected -32600 for GET, got %v", resp.Error)
	}
}

func TestMissingParams(t *testing.T) {
	srv, _ := setupServer(t)
	resp := rpcCall(t, srv, "eth_sendUserOperation") // no params

	if resp.Error == nil {
		t.Fatal("expected error for missing params")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
}
