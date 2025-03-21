package transport

import (
	"encoding/json"
	"fmt"
)

// JSONRPCMessage represents a JSON-RPC message
type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC error
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// NewRequestMessage creates a new JSON-RPC request message
func NewRequestMessage(method string, params interface{}, id interface{}) (*JSONRPCMessage, error) {
	var paramsJSON json.RawMessage
	if params != nil {
		var err error
		paramsJSON, err = json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal params: %w", err)
		}
	}

	return &JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
		ID:      id,
	}, nil
}

// NewResponseMessage creates a new JSON-RPC response message
func NewResponseMessage(id interface{}, result interface{}) (*JSONRPCMessage, error) {
	var resultJSON json.RawMessage
	if result != nil {
		var err error
		resultJSON, err = json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}
	}

	return &JSONRPCMessage{
		JSONRPC: "2.0",
		Result:  resultJSON,
		ID:      id,
	}, nil
}

// NewErrorMessage creates a new JSON-RPC error message
func NewErrorMessage(id interface{}, code int, message string, data interface{}) (*JSONRPCMessage, error) {
	var dataJSON json.RawMessage
	if data != nil {
		var err error
		dataJSON, err = json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal error data: %w", err)
		}
	}

	return &JSONRPCMessage{
		JSONRPC: "2.0",
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    dataJSON,
		},
		ID: id,
	}, nil
}

// NewNotificationMessage creates a new JSON-RPC notification message
func NewNotificationMessage(method string, params interface{}) (*JSONRPCMessage, error) {
	var paramsJSON json.RawMessage
	if params != nil {
		var err error
		paramsJSON, err = json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal params: %w", err)
		}
	}

	return &JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
	}, nil
}

// IsRequest returns true if the message is a request
func (m *JSONRPCMessage) IsRequest() bool {
	return m.Method != "" && m.ID != nil
}

// IsResponse returns true if the message is a response
func (m *JSONRPCMessage) IsResponse() bool {
	return m.ID != nil && (m.Result != nil || m.Error != nil) && m.Method == ""
}

// IsNotification returns true if the message is a notification
func (m *JSONRPCMessage) IsNotification() bool {
	return m.Method != "" && m.ID == nil
}

// Validate validates the JSON-RPC message
func (m *JSONRPCMessage) Validate() error {
	if m.JSONRPC != "2.0" {
		return fmt.Errorf("invalid JSON-RPC version: %s", m.JSONRPC)
	}

	if !m.IsRequest() && !m.IsResponse() && !m.IsNotification() {
		return fmt.Errorf("invalid JSON-RPC message format")
	}

	return nil
}