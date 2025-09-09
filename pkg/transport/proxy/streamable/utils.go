package streamable

import (
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/exp/jsonrpc2"
)

// isNotification returns true if the JSON-RPC message is a notification (no ID).
func isNotification(msg jsonrpc2.Message) bool {
	if req, ok := msg.(*jsonrpc2.Request); ok {
		return req.ID.Raw() == nil
	}
	return false
}

// writeHTTPError writes a plain HTTP error with status.
func writeHTTPError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, msg, status)
}

// writeJSONRPC writes a jsonrpc2.Message using the library's encoder to ensure proper serialization.
func writeJSONRPC(w http.ResponseWriter, msg jsonrpc2.Message) error {
	w.Header().Set("Content-Type", "application/json")
	data, err := jsonrpc2.EncodeMessage(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// idKeyFromID returns a stable string key for a jsonrpc2.ID using its raw value.
// We prefix with type to avoid collisions between numeric and string IDs.
func idKeyFromID(id jsonrpc2.ID) string {
	raw := id.Raw()
	switch v := raw.(type) {
	case string:
		return "s:" + v
	case float64:
		// JSON numbers decode to float64
		return fmt.Sprintf("n:%v", v)
	case json.Number:
		return "n:" + v.String()
	case nil:
		return "nil"
	default:
		return fmt.Sprintf("%T:%v", v, v)
	}
}

// compositeKey builds a stable composite key from session ID and request ID key.
func compositeKey(sessID, idKey string) string {
	return sessID + "|" + idKey
}

// isSupportedMCPVersion is intentionally permissive: we accept any present version string.
// This avoids being pedantic and breaking on new protocol dates while remaining compliant,
// since this proxy is transport-level and does not depend on specific MCP versions.
func isSupportedMCPVersion(_ string) bool {
	return true
}
