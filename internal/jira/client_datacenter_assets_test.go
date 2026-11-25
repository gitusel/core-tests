package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"
)

// TestDataCenterListObjectSchemasReportsUnavailable is the WI-2078 regression:
// a Data Center instance without the legacy Insight Assets API must report the
// whole capability unavailable instead of advertising discoverable schemas that
// become empty native sets. Data Center reads Assets through /rest/insight/1.0,
// so a 404 from that base is what an instance without Assets returns.
func TestDataCenterListObjectSchemasReportsUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	client := &dataCenterClient{
		baseURL:    server.URL + "/rest/api/2",
		httpClient: server.Client(),
		limiter:    rate.NewLimiter(rate.Inf, 1),
	}
	schemas, err := client.ListObjectSchemas(context.Background())
	if !errors.Is(err, ErrAssetsNotAvailable) {
		t.Fatalf("ListObjectSchemas() error = %v, want ErrAssetsNotAvailable", err)
	}
	if len(schemas) != 0 {
		t.Fatalf("schemas = %#v, want none", schemas)
	}
}
