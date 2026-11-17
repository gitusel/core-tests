//go:build test

package graph

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"windshift/internal/models"
	"windshift/internal/outbound"
)

// TestGraphOutboundSendMail posts a threaded reply to an in-process Graph
// server: client-credentials token, sendMail path, replyTo as a recipient
// array, and no internetMessageHeaders.
func TestGraphOutboundSendMail(t *testing.T) {
	const (
		tenant = "tenant-id"
		client = "client-id"
		secret = "client-secret"
		from   = "box@contoso.example"
		token  = "graph-access-token"
	)
	var tokenForm url.Values
	var sendPath, auth string
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			tokenForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": token,
				"expires_in":   3600,
				"token_type":   "Bearer",
			})
		case strings.HasSuffix(r.URL.Path, "/sendMail"):
			sendPath = r.URL.Path
			auth = r.Header.Get("Authorization")
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				t.Errorf("read sendMail: %v", err)
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Errorf("decode sendMail: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	sender := &Sender{cfg: models.ChannelConfig{
		GraphTenantID:     tenant,
		GraphClientID:     client,
		GraphClientSecret: secret,
		GraphFromEmail:    from,
		GraphFromName:     "Windshift",
	}}
	sender.testHTTP = srv.Client()
	sender.testBaseURL = srv.URL
	sender.testTokenURL = srv.URL + "/token"

	err := sender.SendThreadedEmailContext(context.Background(), outbound.ThreadedEmailParams{
		ToEmail:      "customer@example.com",
		ToName:       "Customer",
		ReplyToEmail: "reply@windshift.example",
		ReplyToName:  "Replies",
		Subject:      "Re: ticket",
		HTMLBody:     "<p>hi</p>",
		TextBody:     "hi",
		MessageID:    "<msg@windshift>",
		References:   []string{"<orig@windshift>"},
	})
	if err != nil {
		t.Fatalf("send threaded mail: %v", err)
	}
	if tokenForm.Get("grant_type") != "client_credentials" || tokenForm.Get("client_id") != client || tokenForm.Get("client_secret") != secret {
		t.Fatalf("token form = %v", tokenForm)
	}
	if auth != "Bearer "+token {
		t.Fatalf("authorization = %q", auth)
	}
	wantPath := "/users/" + url.PathEscape(from) + "/sendMail"
	if sendPath != wantPath {
		t.Fatalf("send path = %q, want %q", sendPath, wantPath)
	}
	msg, _ := payload["message"].(map[string]any)
	if msg == nil {
		t.Fatalf("payload = %#v", payload)
	}
	if _, ok := msg["internetMessageHeaders"]; ok {
		t.Fatalf("internetMessageHeaders present: %#v", msg["internetMessageHeaders"])
	}
	replyTo, ok := msg["replyTo"].([]any)
	if !ok || len(replyTo) != 1 {
		t.Fatalf("replyTo = %#v, want a one-element array", msg["replyTo"])
	}
	recip, _ := replyTo[0].(map[string]any)
	addr, _ := recip["emailAddress"].(map[string]any)
	if addr["address"] != "reply@windshift.example" || addr["name"] != "Replies" {
		t.Fatalf("replyTo address = %#v", addr)
	}
}
