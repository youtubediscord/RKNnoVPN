package ipc

import (
	"encoding/json"
	"reflect"
)

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

// Envelope is the typed daemon payload carried by JSON-RPC responses.
type Envelope struct {
	OK        bool        `json:"ok"`
	Result    interface{} `json:"result"`
	Error     interface{} `json:"error,omitempty"`
	Operation interface{} `json:"operation"`
	Warnings  []string    `json:"warnings"`
}

// EnvelopeError describes a daemon-level operation failure.
type EnvelopeError struct {
	Code    string      `json:"code"`
	RPCCode int         `json:"rpcCode"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
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
		Result: Envelope{
			OK:        true,
			Result:    result,
			Operation: operationFromResult(result),
			Warnings:  []string{},
		},
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
			Data: Envelope{
				OK: false,
				Error: EnvelopeError{
					Code:    codeName(code, data),
					RPCCode: code,
					Message: message,
					Details: data,
				},
				Operation: operationFromResult(data),
				Warnings:  []string{},
			},
		},
	}
}

func codeName(code int, data interface{}) string {
	if m, ok := data.(map[string]interface{}); ok {
		if raw, ok := m["code"].(string); ok && raw != "" {
			return raw
		}
	}
	switch code {
	case CodeParseError:
		return "PARSE_ERROR"
	case CodeInvalidRequest:
		return "INVALID_REQUEST"
	case CodeMethodNotFound:
		return "METHOD_NOT_FOUND"
	case CodeInvalidParams:
		return "INVALID_PARAMS"
	case CodeInternalError:
		return "INTERNAL_ERROR"
	case CodeProxyNotRunning:
		return "PROXY_NOT_RUNNING"
	case CodeProxyAlready:
		return "PROXY_ALREADY_RUNNING"
	case CodeConfigError:
		return "CONFIG_ERROR"
	case CodeRuntimeBusy:
		return "RUNTIME_BUSY"
	default:
		return "DAEMON_ERROR"
	}
}

func operationFromResult(result interface{}) interface{} {
	if result == nil {
		return nil
	}
	if m, ok := result.(map[string]interface{}); ok {
		if op, ok := m["operation"]; ok {
			return op
		}
		return m["activeOperation"]
	}
	value := reflect.ValueOf(result)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil
	}
	field := value.FieldByName("ActiveOperation")
	if !field.IsValid() {
		return nil
	}
	switch field.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if field.IsNil() {
			return nil
		}
	}
	if !field.CanInterface() {
		return nil
	}
	return field.Interface()
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
