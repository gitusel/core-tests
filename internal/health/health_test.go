package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"windshift/internal/internalhttp"
)

type pingerFunc func(context.Context) error

func (f pingerFunc) PingContext(ctx context.Context) error {
	return f(ctx)
}

func TestLivenessHandler(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	NewHandler(nil).Liveness(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got, want := recorder.Body.String(), "{\"status\":\"ok\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestReadinessHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pinger     Pinger
		wantStatus int
		wantBody   string
	}{
		{
			name:       "database available",
			pinger:     pingerFunc(func(context.Context) error { return nil }),
			wantStatus: http.StatusOK,
			wantBody:   "{\"status\":\"ready\",\"database\":\"ok\",\"attachments\":\"disabled\"}\n",
		},
		{
			name:       "database unavailable",
			pinger:     pingerFunc(func(context.Context) error { return errors.New("connection details must not leak") }),
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "{\"status\":\"not_ready\",\"database\":\"unavailable\",\"attachments\":\"disabled\"}\n",
		},
		{
			name:       "database not configured",
			pinger:     nil,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "{\"status\":\"not_ready\",\"database\":\"unavailable\",\"attachments\":\"unknown\"}\n",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			NewHandler(tt.pinger).Readiness(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := recorder.Body.String(); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
			if strings.Contains(recorder.Body.String(), "connection details") {
				t.Fatal("readiness response leaked the database error")
			}
		})
	}
}

func TestProbe(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	err := Probe(context.Background(), nil, server.URL)
	if err == nil {
		t.Fatal("Probe() error = nil, want a non-nil error for a non-2xx response")
	}
}

func TestBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		port        string
		contextPath string
		directTLS   bool
		want        string
	}{
		{
			name: "defaults",
			want: "http://127.0.0.1:8080",
		},
		{
			name: "custom port",
			port: "9090",
			want: "http://127.0.0.1:9090",
		},
		{
			name:        "context path",
			port:        "9090",
			contextPath: "/windshift/",
			want:        "http://127.0.0.1:9090/windshift",
		},
		{
			name:      "direct TLS",
			port:      "8443",
			directTLS: true,
			want:      "https://127.0.0.1:8443",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := internalhttp.BaseURL(tt.port, tt.contextPath, tt.directTLS); got != tt.want {
				t.Fatalf("BaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
