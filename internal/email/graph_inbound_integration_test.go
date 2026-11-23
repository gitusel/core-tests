//go:build test

package email

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"windshift/internal/blobstore"
	"windshift/internal/models"
	"windshift/internal/services"
)

// Graph inbound is Graph only. SelectFolder does not call Graph: it maps the
// scheduler folder name onto a well-known mailFolders segment. "INBOX",
// "Inbox", and "inbox" all mean "inbox". Poll then uses that segment.
//
// The cursor is the @odata.deltaLink from the last page, not a Unix-second
// UID. A page of messages that share receivedDateTime must be followed via
// @odata.nextLink inside the same poll. The next poll GETs the saved
// deltaLink once. An empty delta page ends the poll. It must not skip the
// remainder of an equal-timestamp page, and it must not repeat that page.

func TestGraphInboundFolderDeltaAndIntake(t *testing.T) {
	const (
		mailbox = "inbox@contoso.example"
		token   = "delegated-graph-token"
		when    = "2026-07-17T08:00:00Z"
	)
	ids := []string{"AAMkAG1", "AAMkAG2", "AAMkAG3"}
	mime := map[string]string{
		ids[0]: graphInboundMIME(ids[0], "Graph inbound one"),
		ids[1]: graphInboundMIME(ids[1], "Graph inbound two"),
		ids[2]: graphInboundMIME(ids[2], "Graph inbound three"),
	}
	deltaPage2 := "/users/" + mailbox + "/mailFolders/inbox/messages/delta?$skiptoken=page2"
	deltaSaved := "/users/" + mailbox + "/mailFolders/inbox/messages/delta?$deltatoken=saved"

	var calls []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("authorization = %q", got)
		}
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/users/"+mailbox+"/mailFolders/inbox/messages/delta" && r.URL.RawQuery == "$top=2":
			_, _ = io.WriteString(w, graphDeltaPage([]string{ids[0], ids[1]}, when, srv.URL+deltaPage2, ""))
		case r.Method == http.MethodGet && r.URL.RequestURI() == deltaPage2:
			_, _ = io.WriteString(w, graphDeltaPage([]string{ids[2]}, when, "", srv.URL+deltaSaved))
		case r.Method == http.MethodGet && r.URL.RequestURI() == deltaSaved:
			_, _ = io.WriteString(w, graphDeltaPage(nil, "", "", srv.URL+deltaSaved))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/$value"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/users/"+mailbox+"/messages/"), "/$value")
			body, ok := mime[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "message/rfc822")
			_, _ = io.WriteString(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	box, err := OpenGraphMailbox(context.Background(), GraphMailboxConfig{
		BaseURL:    srv.URL,
		Token:      token,
		Mailbox:    mailbox,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("OpenGraphMailbox: %v", err)
	}
	if err := box.SelectFolder("INBOX"); err != nil {
		t.Fatalf("SelectFolder: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("SelectFolder called Graph: %s", strings.Join(calls, "\n"))
	}

	messages, cursor, err := box.Poll(context.Background(), GraphMailboxCursor{}, 2)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("first poll returned %d messages, want 3", len(messages))
	}
	if cursor.DeltaLink != srv.URL+deltaSaved {
		t.Fatalf("cursor = %q, want saved deltaLink", cursor.DeltaLink)
	}

	db := newProcessorTestDB(t)
	var workspaceID, itemTypeID, channelID int
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES ('Graph inbound', 'GIN') RETURNING id`).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO item_types (name) VALUES ('Inbound email') RETURNING id`).Scan(&itemTypeID); err != nil {
		t.Fatalf("insert item type: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO channels (name, type, direction) VALUES ('Graph inbox', 'email', 'inbound') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	processor := NewProcessor(db, blobstore.NewLocal(t.TempDir()))
	processor.SetCommentService(services.NewCommentService(db))
	config := &models.ChannelConfig{EmailWorkspaceID: workspaceID, EmailItemTypeID: &itemTypeID}
	var firstID *int
	for i, msg := range messages {
		parsed := NewParser().Parse(msg)
		created, err := processor.ProcessEmail(context.Background(), parsed, channelID, 1, config)
		if err != nil {
			t.Fatalf("process %d: %v", i, err)
		}
		if created.Action != ActionItemCreated || created.ItemID == nil {
			t.Fatalf("message %d result = %#v", i, created)
		}
		if i == 0 {
			firstID = created.ItemID
		}
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM items WHERE id = ?`, *firstID).Scan(&title); err != nil {
		t.Fatalf("load item: %v", err)
	}
	if title != "Graph inbound one" {
		t.Fatalf("title = %q", title)
	}
	var items int
	if err := db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&items); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if items != 3 {
		t.Fatalf("items = %d, want 3", items)
	}
	again, err := processor.ProcessEmail(context.Background(), NewParser().Parse(messages[0]), channelID, 1, config)
	if err != nil || again.Action != ActionAlreadyExists {
		t.Fatalf("duplicate = (%#v, %v)", again, err)
	}

	beforeSecond := len(calls)
	second, next, err := box.Poll(context.Background(), cursor, 2)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second poll returned %d messages, want 0", len(second))
	}
	if next.DeltaLink != cursor.DeltaLink {
		t.Fatalf("second cursor = %q, want the same deltaLink", next.DeltaLink)
	}
	secondCalls := calls[beforeSecond:]
	if len(secondCalls) != 1 || secondCalls[0] != "GET "+deltaSaved {
		t.Fatalf("second poll calls = %s, want one GET of the saved deltaLink", strings.Join(secondCalls, "\n"))
	}

	firstCalls := calls[:beforeSecond]
	wantFirst := []string{
		"GET /users/" + mailbox + "/mailFolders/inbox/messages/delta?$top=2",
		"GET " + deltaPage2,
		"GET /users/" + mailbox + "/messages/" + ids[0] + "/$value",
		"GET /users/" + mailbox + "/messages/" + ids[1] + "/$value",
		"GET /users/" + mailbox + "/messages/" + ids[2] + "/$value",
	}
	if strings.Join(firstCalls, "\n") != strings.Join(wantFirst, "\n") {
		t.Fatalf("first poll calls =\n%s\nwant\n%s", strings.Join(firstCalls, "\n"), strings.Join(wantFirst, "\n"))
	}
}

func TestGraphInboundDispositionCallsGraph(t *testing.T) {
	const (
		mailbox = "inbox@contoso.example"
		token   = "delegated-graph-token"
		graphID = "AAMkAG1"
	)
	var calls []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages/delta"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, graphDeltaPage([]string{graphID}, "2026-07-17T08:00:00Z", "", srv.URL+"/delta-saved"))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/$value"):
			w.Header().Set("Content-Type", "message/rfc822")
			_, _ = io.WriteString(w, graphInboundMIME(graphID, "Graph inbound one"))
		case r.Method == http.MethodPatch && r.URL.Path == "/users/"+mailbox+"/messages/"+graphID:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && r.URL.Path == "/users/"+mailbox+"/messages/"+graphID:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	provider := NewMicrosoftProvider("client-id", "client-secret", "tenant-id", nil)
	provider.graphBaseURL = srv.URL
	provider.httpClient = srv.Client()
	box, err := provider.OpenMailbox(context.Background(), &models.ChannelConfig{
		EmailOAuthAccessToken: token,
		EmailOAuthEmail:       mailbox,
	})
	if err != nil {
		t.Fatalf("OpenMailbox: %v", err)
	}
	if _, err := box.Open(context.Background(), "INBOX", false); err != nil {
		t.Fatalf("Open: %v", err)
	}
	deliveries, err := box.Fetch(context.Background(), 2)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("fetch = (%d, %v)", len(deliveries), err)
	}
	if err := box.MarkRead(context.Background(), deliveries[0].Ref); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if err := box.Delete(context.Background(), deliveries[0].Ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"PATCH /users/" + mailbox + "/messages/" + graphID,
		"DELETE /users/" + mailbox + "/messages/" + graphID,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("calls =\n%s\nmissing %s", joined, want)
		}
	}
}

func graphInboundMIME(id, subject string) string {
	return "Date: Fri, 17 Jul 2026 10:00:00 +0000\r\n" +
		"From: Customer <customer@example.com>\r\n" +
		"To: Inbox <inbox@contoso.example>\r\n" +
		"Subject: " + subject + "\r\n" +
		"Message-ID: <" + id + "@example.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		"Body for " + id + "\r\n"
}

func graphDeltaPage(ids []string, when, nextLink, deltaLink string) string {
	var b strings.Builder
	b.WriteString(`{"value":[`)
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"` + id + `","receivedDateTime":"` + when + `"}`)
	}
	b.WriteString(`]`)
	if nextLink != "" {
		b.WriteString(`,"@odata.nextLink":"` + nextLink + `"`)
	}
	if deltaLink != "" {
		b.WriteString(`,"@odata.deltaLink":"` + deltaLink + `"`)
	}
	b.WriteString(`}`)
	return b.String()
}
