package rpc

import (
	"encoding/json"
	"io"
	"net/http"

	"go.uber.org/zap"
)

// Server is the JSON-RPC HTTP server.
type Server struct {
	methods *Methods
	log     *zap.Logger
}

// NewServer creates a JSON-RPC server.
func NewServer(methods *Methods, log *zap.Logger) *Server {
	return &Server{methods: methods, log: log}
}

// Handler returns an http.Handler for the JSON-RPC endpoint.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.handleRequest)
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, nil, ErrInvalidRequest("method must be POST"))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, nil, ErrInvalidRequest("failed to read body"))
		return
	}

	var req jsonrpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, nil, ErrInvalidRequest("invalid JSON: "+err.Error()))
		return
	}

	if req.JSONRPC != "2.0" {
		writeError(w, req.ID, ErrInvalidRequest("jsonrpc must be \"2.0\""))
		return
	}

	result, rpcErr := s.dispatch(r, &req)
	if rpcErr != nil {
		writeError(w, req.ID, rpcErr)
		return
	}

	writeResult(w, req.ID, result)
}

func (s *Server) dispatch(r *http.Request, req *jsonrpcRequest) (any, *RpcError) {
	switch req.Method {
	case "eth_sendUserOperation":
		return s.methods.handleSendUserOperation(req.Params)
	case "eth_estimateUserOperationGas":
		return s.methods.handleEstimateUserOperationGas(r.Context(), req.Params)
	case "eth_getUserOperationByHash":
		return s.methods.handleGetUserOperationByHash(req.Params)
	case "eth_getUserOperationReceipt":
		return s.methods.handleGetUserOperationReceipt(req.Params)
	case "eth_supportedEntryPoints":
		return s.methods.handleSupportedEntryPoints()
	case "eth_chainId":
		return s.methods.handleChainId()
	case "pm_getPaymasterStubData":
		return s.methods.handleGetPaymasterStubData(req.Params)
	case "pm_getPaymasterData":
		return s.methods.handleGetPaymasterData(r.Context(), req.Params)
	default:
		return nil, ErrMethodNotFound("method not found: " + req.Method)
	}
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeError(w http.ResponseWriter, id json.RawMessage, rpcErr *RpcError) {
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   rpcErr,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
