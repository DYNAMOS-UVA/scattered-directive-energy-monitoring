package httpapi

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes the response as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// WriteError writes a JSON error response.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}

// DecodeJSON decodes the request body into the provided value.
func DecodeJSON(r *http.Request, value interface{}) error {
	decoder := json.NewDecoder(r.Body)
	return decoder.Decode(value)
}

// RequireMethod wraps an HTTP handler and enforces the specified HTTP method.
// If the request method doesn't match, it returns a 405 Method Not Allowed response.
func RequireMethod(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handler(w, r)
	}
}

// RequireGET wraps an HTTP handler and enforces GET method.
func RequireGET(handler http.HandlerFunc) http.HandlerFunc {
	return RequireMethod(http.MethodGet, handler)
}

// RequirePOST wraps an HTTP handler and enforces POST method.
func RequirePOST(handler http.HandlerFunc) http.HandlerFunc {
	return RequireMethod(http.MethodPost, handler)
}

// RequireDELETE wraps an HTTP handler and enforces DELETE method.
func RequireDELETE(handler http.HandlerFunc) http.HandlerFunc {
	return RequireMethod(http.MethodDelete, handler)
}
