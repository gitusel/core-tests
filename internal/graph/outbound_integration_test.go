//go:build test

package graph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"testing"

	"windshift/internal/models"
	"windshift/internal/outbound"
)

// TestGraphOutboundSendMail posts a threaded reply to an in-process Graph
// server: client-credentials token, sendMail path, and the message as
// base64-encoded RFC 5322 MIME.
//
// MIME, not the JSON message shape, because a threaded reply has to carry
// Message-ID, In-Reply-To, References, and Reply-To. Graph's JSON sendMail
// cannot express Message-ID or In-Reply-To at all: internetMessageHeaders
// rejects any name that does not start with "x-". Sending JSON would therefore
// deliver the mail but silently drop the threading the caller asked for.
func TestGraphOutboundSendMail(t *testing.T) {
	const (
		tenant = "tenant-id"
		client = "client-id"
		secret = "client-secret"
		from   = "box@contoso.example"
		token  = "graph-access-token"
	)
	var tokenForm url.Values
	var sendPath, auth, contentType string
	var rawMessage []byte
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
			contentType = r.Header.Get("Content-Type")
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				t.Errorf("read sendMail: %v", err)
			}
			decoded, err := base64.StdEncoding.DecodeString(string(body))
			if err != nil {
				t.Errorf("decode sendMail body as base64 MIME: %v", err)
			}
			rawMessage = decoded
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
		InReplyTo:    "<orig@windshift>",
		References:   []string{"<root@windshift>", "<orig@windshift>"},
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
	if contentType != "text/plain" {
		t.Fatalf("sendMail content type = %q, want text/plain (base64 MIME)", contentType)
	}
	wantPath := "/users/" + url.PathEscape(from) + "/sendMail"
	if sendPath != wantPath {
		t.Fatalf("send path = %q, want %q", sendPath, wantPath)
	}
	if len(rawMessage) == 0 {
		t.Fatal("sendMail body was not base64 MIME")
	}

	msg, err := mail.ReadMessage(strings.NewReader(string(rawMessage)))
	if err != nil {
		t.Fatalf("parse sent MIME: %v", err)
	}
	if got, want := msg.Header.Get("To"), "customer@example.com"; !strings.Contains(got, want) {
		t.Fatalf("To = %q, want it to contain %q", got, want)
	}
	// The threading headers are the whole point of using MIME here.
	if got := msg.Header.Get("Message-ID"); got != "<msg@windshift>" {
		t.Fatalf("Message-ID = %q, want <msg@windshift>", got)
	}
	if got := msg.Header.Get("In-Reply-To"); got != "<orig@windshift>" {
		t.Fatalf("In-Reply-To = %q, want <orig@windshift>", got)
	}
	references := msg.Header.Get("References")
	for _, want := range []string{"<root@windshift>", "<orig@windshift>"} {
		if !strings.Contains(references, want) {
			t.Fatalf("References = %q, missing %q", references, want)
		}
	}
	replyTo, err := msg.Header.AddressList("Reply-To")
	if err != nil || len(replyTo) != 1 {
		t.Fatalf("Reply-To = %q (%v), want one address", msg.Header.Get("Reply-To"), err)
	}
	if replyTo[0].Address != "reply@windshift.example" || replyTo[0].Name != "Replies" {
		t.Fatalf("Reply-To address = %#v", replyTo[0])
	}
}
