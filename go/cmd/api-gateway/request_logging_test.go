package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestAgentRequestErrorLogOmitsPayload(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	originalLogger := logger
	logger = zap.New(core)
	t.Cleanup(func() { logger = originalLogger })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected", http.StatusBadRequest)
	}))
	defer server.Close()

	payload := []byte(`{"embeddings":"large-private-payload"}`)
	_, err := sendData(server.URL, payload)
	if err == nil {
		t.Fatal("expected agent HTTP error")
	}
	entries := logs.FilterMessage("Agent HTTP request failed").All()
	if len(entries) != 1 {
		t.Fatalf("expected one request failure log, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["endpoint"] != server.URL || fields["request_bytes"] != int64(len(payload)) {
		t.Fatalf("missing compact request details: %v", fields)
	}
	for _, field := range entries[0].Context {
		if strings.Contains(field.String, "large-private-payload") {
			t.Fatal("request payload leaked into log")
		}
	}
}
