package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"windshift/internal/utils"
)

func TestSSRFSafeTransportBlocksLocalTargetsWhenExplicitlyDisallowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	utils.SetAllowLocalConnections(false)
	defer utils.SetAllowLocalConnections(true)
	// A process-level proxy must not become an unchecked alternate dial path.
	t.Setenv("HTTP_PROXY", server.URL)
	t.Setenv("HTTPS_PROXY", server.URL)

	blockedTransport := ssrfSafeTransport(time.Second)
	blockedRequest, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	if response, err := blockedTransport.RoundTrip(blockedRequest); err == nil {
		_ = response.Body.Close()
		t.Fatal("SSRF-safe transport connected to a loopback target")
	}
	// The transport does read the proxy environment, but only through the
	// policy hook: with local connections disallowed it declines every proxy,
	// so a process-level HTTP_PROXY cannot become an unchecked dial path.
	transport, ok := blockedTransport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("SSRF-safe transport does not consult the proxy policy: %#v", blockedTransport)
	}
	proxyRequest, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	if proxyURL, err := transport.Proxy(proxyRequest); err != nil || proxyURL != nil {
		t.Fatalf("proxy policy returned (%v, %v) with local connections disallowed, want (nil, nil)", proxyURL, err)
	}

	utils.SetAllowLocalConnections(true)
	allowedTransport := ssrfSafeTransport(time.Second)
	allowedRequest, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	response, err := allowedTransport.RoundTrip(allowedRequest)
	if err != nil {
		t.Fatalf("transport with local override: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}
