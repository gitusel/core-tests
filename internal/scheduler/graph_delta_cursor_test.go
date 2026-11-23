//go:build test

package scheduler

import (
	"context"
	"sort"
	"testing"

	"windshift/internal/blobstore"
	"windshift/internal/email"
	"windshift/internal/mailbox"
	"windshift/internal/mailmsg"
	"windshift/internal/models"
	"windshift/internal/services"
	"windshift/internal/sso"
	"windshift/internal/testutils"
)

// deltaRecordClient is a Graph-shaped Mailbox double: a cursor per folder, no
// UID space. Selecting a different folder drops the in-memory cursor, matching
// the real Graph mailbox.
type deltaRecordClient struct {
	folder    string
	cursor    string
	committed string
	fetches   []string
	fail      bool
	advance   string
}

func (c *deltaRecordClient) Open(_ context.Context, name string, _ bool) (mailbox.Info, error) {
	if c.folder != "" && c.folder != name {
		c.cursor = ""
		c.committed = ""
	}
	c.folder = name
	return mailbox.Info{}, nil
}
func (c *deltaRecordClient) Restore(link string) { c.cursor = link; c.committed = link }
func (c *deltaRecordClient) Checkpoint() string  { return c.committed }
func (c *deltaRecordClient) Acknowledge()        { c.committed = c.cursor }
func (c *deltaRecordClient) Fetch(_ context.Context, _ int) ([]mailbox.Delivery, error) {
	c.fetches = append(c.fetches, c.folder+"|"+c.cursor)
	if c.fail {
		if c.advance != "" {
			c.cursor = c.advance
		}
		return []mailbox.Delivery{{
			Ref: "graph-fail",
			Message: &mailmsg.Message{
				Ref:        "graph-fail",
				Raw:        []byte("From: customer@example.com\r\nSubject: fail\r\nMessage-ID: <fail@example.com>\r\n\r\nbody\r\n"),
				FetchError: errDeltaFetch,
			},
		}}, nil
	}
	if c.advance != "" {
		c.cursor = c.advance
	} else if c.cursor == "" {
		c.cursor = c.folder + "-delta-1"
	}
	return nil, nil
}
func (c *deltaRecordClient) MarkRead(context.Context, string) error { return nil }
func (c *deltaRecordClient) Delete(context.Context, string) error   { return nil }
func (c *deltaRecordClient) Expunge(context.Context) error          { return nil }
func (c *deltaRecordClient) Close() error                           { return nil }

type errString string

func (e errString) Error() string { return string(e) }

const errDeltaFetch errString = "fetch failed"

// staticProvider hands back a native Mailbox, so no IMAP client is involved.
type staticProvider struct{ box mailbox.Mailbox }

func (p *staticProvider) GetType() string { return "graph" }
func (p *staticProvider) GetIMAPServer(*models.ChannelConfig) (string, int) {
	return "", 0
}
func (p *staticProvider) Connect(context.Context, *models.ChannelConfig) (email.IMAPClient, error) {
	return nil, errDeltaFetch
}
func (p *staticProvider) TestConnection(context.Context, *models.ChannelConfig) error { return nil }
func (p *staticProvider) OpenMailbox(context.Context, *models.ChannelConfig) (mailbox.Mailbox, error) {
	return p.box, nil
}

func TestGraphDeltaCursorPerIntakeAcrossTicks(t *testing.T) {
	tdb := testutils.CreateTestDB(t, true)
	t.Cleanup(func() { _ = tdb.Close() })
	data := tdb.SeedTestData(t)
	db := tdb.GetDatabase()
	ctx := context.Background()
	var itemTypeID int
	if err := db.QueryRowContext(ctx, `SELECT id FROM item_types WHERE is_default = true ORDER BY id LIMIT 1`).Scan(&itemTypeID); err != nil {
		t.Fatalf("item type: %v", err)
	}
	var channelID int
	if err := db.QueryRowContext(ctx, `
		INSERT INTO channels (name, type, direction, status, config)
		VALUES ('Graph mailbox', 'email', 'inbound', 'enabled', '{}') RETURNING id
	`).Scan(&channelID); err != nil {
		t.Fatalf("channel: %v", err)
	}
	insert := func(folder string) int {
		t.Helper()
		var id int
		if err := db.QueryRowContext(ctx, `
			INSERT INTO intakes (mailbox_id, folder, workspace_id, item_type_id, status)
			VALUES (?, ?, ?, ?, 'enabled') RETURNING id
		`, channelID, folder, data.WorkspaceID, itemTypeID).Scan(&id); err != nil {
			t.Fatalf("intake %s: %v", folder, err)
		}
		return id
	}
	inboxID := insert("INBOX")
	otherID := insert("Archive")

	var clients []*deltaRecordClient
	enc := sso.NewSecretEncryption("test-server-secret-with-sufficient-length-for-derivation")
	es := NewEmailScheduler(db, email.NewCredentialManager(db, enc), blobstore.NewLocal(t.TempDir()))
	es.SetCommentService(services.NewCommentService(db))
	es.providerForChannel = func(context.Context, int) (email.Provider, *models.ChannelConfig, error) {
		client := &deltaRecordClient{}
		clients = append(clients, client)
		return &staticProvider{box: client}, &models.ChannelConfig{}, nil
	}

	if ok := es.processChannel(ctx, channelInfo{ID: channelID, Name: "Graph mailbox"}); !ok {
		t.Fatal("first tick failed")
	}
	if ok := es.processChannel(ctx, channelInfo{ID: channelID, Name: "Graph mailbox"}); !ok {
		t.Fatal("second tick failed")
	}
	if len(clients) != 2 {
		t.Fatalf("connects = %d, want 2", len(clients))
	}
	second := append([]string(nil), clients[1].fetches...)
	sort.Strings(second)
	want := []string{"Archive|Archive-delta-1", "INBOX|INBOX-delta-1"}
	if len(second) != 2 || second[0] != want[0] || second[1] != want[1] {
		t.Fatalf("second tick fetches = %v, want each folder to resume its own delta", clients[1].fetches)
	}
	var inboxLink, otherLink string
	if err := db.QueryRowContext(ctx, `SELECT graph_delta_link FROM email_intake_state WHERE intake_id = ?`, inboxID).Scan(&inboxLink); err != nil {
		t.Fatalf("inbox delta: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT graph_delta_link FROM email_intake_state WHERE intake_id = ?`, otherID).Scan(&otherLink); err != nil {
		t.Fatalf("archive delta: %v", err)
	}
	if inboxLink != "INBOX-delta-1" || otherLink != "Archive-delta-1" {
		t.Fatalf("stored deltas = %q %q", inboxLink, otherLink)
	}
}

func TestGraphDeltaCursorRetriesOriginalLinkAfterFailedTick(t *testing.T) {
	tdb := testutils.CreateTestDB(t, true)
	t.Cleanup(func() { _ = tdb.Close() })
	data := tdb.SeedTestData(t)
	db := tdb.GetDatabase()
	ctx := context.Background()
	var itemTypeID int
	if err := db.QueryRowContext(ctx, `SELECT id FROM item_types WHERE is_default = true ORDER BY id LIMIT 1`).Scan(&itemTypeID); err != nil {
		t.Fatalf("item type: %v", err)
	}
	var channelID int
	if err := db.QueryRowContext(ctx, `
		INSERT INTO channels (name, type, direction, status, config)
		VALUES ('Graph mailbox', 'email', 'inbound', 'enabled', '{}') RETURNING id
	`).Scan(&channelID); err != nil {
		t.Fatalf("channel: %v", err)
	}
	if _, err := db.ExecWriteContext(ctx, `
		INSERT INTO intakes (mailbox_id, folder, workspace_id, item_type_id, status)
		VALUES (?, 'INBOX', ?, ?, 'enabled')
	`, channelID, data.WorkspaceID, itemTypeID); err != nil {
		t.Fatalf("intake: %v", err)
	}
	var clients []*deltaRecordClient
	enc := sso.NewSecretEncryption("test-server-secret-with-sufficient-length-for-derivation")
	es := NewEmailScheduler(db, email.NewCredentialManager(db, enc), blobstore.NewLocal(t.TempDir()))
	es.SetCommentService(services.NewCommentService(db))
	es.providerForChannel = func(context.Context, int) (email.Provider, *models.ChannelConfig, error) {
		client := &deltaRecordClient{fail: len(clients) == 0, advance: "should-not-stick"}
		clients = append(clients, client)
		return &staticProvider{box: client}, &models.ChannelConfig{}, nil
	}
	if ok := es.processChannel(ctx, channelInfo{ID: channelID, Name: "Graph mailbox"}); ok {
		t.Fatal("failed tick reported success")
	}
	if ok := es.processChannel(ctx, channelInfo{ID: channelID, Name: "Graph mailbox"}); !ok {
		t.Fatal("retry tick failed")
	}
	if len(clients) != 2 || len(clients[1].fetches) != 1 || clients[1].fetches[0] != "INBOX|" {
		t.Fatalf("retry fetches = %#v, want the original empty cursor", clients[1].fetches)
	}
}
