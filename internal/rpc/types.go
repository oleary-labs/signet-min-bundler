package rpc

import "encoding/json"

// JSON-RPC request envelope.
type jsonrpcRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

// JSON-RPC success response.
type jsonrpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *RpcError   `json:"error,omitempty"`
}

// RpcError is a JSON-RPC error object.
type RpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RpcError) Error() string { return e.Message }

var (
	ErrInvalidRequest = func(msg string) *RpcError { return &RpcError{-32600, msg} }
	ErrMethodNotFound = func(msg string) *RpcError { return &RpcError{-32601, msg} }
	ErrInvalidParams  = func(msg string) *RpcError { return &RpcError{-32602, msg} }
	ErrOpRejected     = func(msg string) *RpcError { return &RpcError{-32521, msg} }
)
