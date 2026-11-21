// Package apierr writes the shared JSON error format used by both apps.
package apierr

import (
	"encoding/json"
	"net/http"
)

type body struct {
	Error detail `json:"error"`
}

type detail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Write sends {"error":{"code":..., "message":...}} with the given status.
func Write(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body{Error: detail{Code: code, Message: msg}})
}
