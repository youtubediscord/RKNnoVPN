package ipc

import "encoding/json"

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// Application-defined error codes (below -32000).
	CodeProxyNotRunning = -32001
	CodeProxyAlready    = -32002
	CodeConfigError     = -32003
	CodeRuntimeBusy     = -32004
)

// Request is a JSON-RPC 2.0 request object.
type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int              `json:"id"`
	Method  string           `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response object.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

// RPCError is the error object inside a JSON-RPC 2.0 response.
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// NewResponse creates a successful JSON-RPC 2.0 response.
func NewResponse(id int, result interface{}) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

// NewErrorResponse creates an error JSON-RPC 2.0 response.
func NewErrorResponse(id int, code int, message string, data interface{}) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

// Validate checks that the request conforms to JSON-RPC 2.0.
func (r *Request) Validate() *RPCError {
	if r.JSONRPC != "2.0" {
		return &RPCError{
			Code:    CodeInvalidRequest,
			Message: "jsonrpc field must be \"2.0\"",
		}
	}
	if r.Method == "" {
		return &RPCError{
			Code:    CodeInvalidRequest,
			Message: "method field is required",
		}
	}
	return nil
}
