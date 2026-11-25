package email

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"windshift/internal/blobstore"
	"windshift/internal/database"
	"windshift/internal/testutils"
)

func newProcessorRegressionTestDB(t *testing.T) database.Database {
	t.Helper()
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "email-processor.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	if err := db.Initialize(); err != nil {
		_ = db.Close()
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestProcessEmailRejectsInvalidSenderBeforeClaim(t *testing.T) {
	processor := &Processor{}
	_, err := processor.ProcessEmail(context.Background(), &ParsedEmail{
		From: EmailAddress{Address: "not-an-address"},
	}, 1, 1, nil)
	if err == nil {
		t.Fatal("invalid sender unexpectedly reached processing")
	}
}

func TestProcessEmailRejectsMissingInputWithoutPanic(t *testing.T) {
	processor := &Processor{}
	if _, err := processor.ProcessEmail(context.Background(), nil, 1, 1, nil); err == nil {
		t.Fatal("nil email unexpectedly reached processing")
	}
	if _, err := processor.ProcessEmail(context.Background(), &ParsedEmail{
		From: EmailAddress{Address: "sender@example.com"},
	}, 1, 1, nil); err == nil {
		t.Fatal("nil channel config unexpectedly reached processing")
	}
}

func TestTrackingPreclaimRecoversOnlyStaleIncompleteClaims(t *testing.T) {
	db := newProcessorRegressionTestDB(t)
	var channelID int
	if err := db.QueryRow(`
		INSERT INTO channels (name, type, direction)
		VALUES ('Inbound', 'email', 'inbound') RETURNING id
	`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	processor := NewProcessor(db, blobstore.Disabled())
	message := &ParsedEmail{
		MessageID: "<claim@example.com>",
		From:      EmailAddress{Address: "sender@example.com"},
		Subject:   "Claim",
	}
	ctx := context.Background()

	claim, err := processor.preclaimTracking(ctx, message, channelID, nil, message.MessageID, 0)
	if err != nil || claim != trackingClaimAcquired {
		t.Fatalf("first preclaim = (%v, %v), want (acquired, nil)", claim, err)
	}
	claim, err = processor.preclaimTracking(ctx, message, channelID, nil, message.MessageID, 0)
	if err != nil || claim != trackingClaimInProgress {
		t.Fatalf("fresh duplicate preclaim = (%v, %v), want (in progress, nil)", claim, err)
	}

	if _, err := db.ExecWrite(`
		UPDATE email_message_tracking SET processed_at = ?
		WHERE channel_id = ? AND dedup_key = ?
	`, time.Now().Add(-trackingClaimStaleAfter-time.Minute), channelID, message.MessageID); err != nil {
		t.Fatalf("age incomplete claim: %v", err)
	}
	claim, err = processor.preclaimTracking(ctx, message, channelID, nil, message.MessageID, 0)
	if err != nil || claim != trackingClaimAcquired {
		t.Fatalf("stale incomplete preclaim = (%v, %v), want (acquired, nil)", claim, err)
	}

	var workspaceID, itemID int
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES ('Test', 'TST') RETURNING id`).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO items (workspace_id, workspace_item_number, title, frac_index)
		VALUES (?, 1, 'Claimed item', ?) RETURNING id
	`, workspaceID, testutils.NextTestFracIndex()).Scan(&itemID); err != nil {
		t.Fatalf("insert item: %v", err)
	}
	if _, err := db.ExecWrite(`
		UPDATE email_message_tracking SET item_id = ?, processed_at = ?
		WHERE channel_id = ? AND dedup_key = ?
	`, itemID, time.Now().Add(-trackingClaimStaleAfter-time.Minute), channelID, message.MessageID); err != nil {
		t.Fatalf("complete claim: %v", err)
	}
	claim, err = processor.preclaimTracking(ctx, message, channelID, nil, message.MessageID, 0)
	if err != nil || claim != trackingClaimCompleted {
		t.Fatalf("completed preclaim = (%v, %v), want (completed, nil)", claim, err)
	}

	// Deleting the created item nulls item_id via ON DELETE SET NULL. The
	// completed_at marker must keep the row deduplicated so the email is not
	// reprocessed into a fresh item.
	if _, err := db.ExecWrite(`
		UPDATE email_message_tracking SET item_id = NULL, completed_at = CURRENT_TIMESTAMP
		WHERE channel_id = ? AND dedup_key = ?
	`, channelID, message.MessageID); err != nil {
		t.Fatalf("simulate item deletion: %v", err)
	}
	claim, err = processor.preclaimTracking(ctx, message, channelID, nil, message.MessageID, 0)
	if err != nil || claim != trackingClaimCompleted {
		t.Fatalf("preclaim after item deletion = (%v, %v), want (completed, nil)", claim, err)
	}
}

// TestSenderThreadParticipationIsItemScoped pins the item-scoped reply
// contract: authorization belongs to the request, not to the mailbox a message
// arrived on, and not to the sender's own thread history. A prior sender is not
// an identity (WI-2365); the item's recorded contact address is, on whichever
// channel the conversation started.
func TestSenderThreadParticipationIsItemScoped(t *testing.T) {
	db := newProcessorRegressionTestDB(t)
	insertChannel := func(name string) int {
		t.Helper()
		var id int
		if err := db.QueryRow(`
			INSERT INTO channels (name, type, direction)
			VALUES (?, 'email', 'inbound') RETURNING id
		`, name).Scan(&id); err != nil {
			t.Fatalf("insert channel: %v", err)
		}
		return id
	}
	firstChannelID := insertChannel("First mailbox")
	secondChannelID := insertChannel("Second mailbox")

	var workspaceID, itemID, otherItemID int
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES ('Thread', 'THR') RETURNING id`).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO items (workspace_id, workspace_item_number, title, channel_id, contact_email, frac_index)
		VALUES (?, 1, 'Thread item', ?, 'sender@example.com', ?) RETURNING id
	`, workspaceID, firstChannelID, testutils.NextTestFracIndex()).Scan(&itemID); err != nil {
		t.Fatalf("insert item: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO items (workspace_id, workspace_item_number, title, channel_id, frac_index)
		VALUES (?, 2, 'Other item', ?, ?) RETURNING id
	`, workspaceID, secondChannelID, testutils.NextTestFracIndex()).Scan(&otherItemID); err != nil {
		t.Fatalf("insert other item: %v", err)
	}
	// The sender's mail arrived on the other mailbox, which is irrelevant: the
	// address is the item's recorded contact.
	if _, err := db.ExecWrite(`
		INSERT INTO email_message_tracking
			(channel_id, message_id, dedup_key, from_email, item_id)
		VALUES (?, '<other-channel@example.com>', '<other-channel@example.com>', 'sender@example.com', ?)
	`, secondChannelID, itemID); err != nil {
		t.Fatalf("insert cross-channel participant: %v", err)
	}
	// Thread history on a different request authorizes nothing on that request.
	if _, err := db.ExecWrite(`
		INSERT INTO email_message_tracking
			(channel_id, message_id, dedup_key, from_email, item_id)
		VALUES (?, '<other-item@example.com>', '<other-item@example.com>', 'sender@example.com', ?)
	`, secondChannelID, otherItemID); err != nil {
		t.Fatalf("insert other-item history: %v", err)
	}

	processor := NewProcessor(db, blobstore.Disabled())
	if !processor.senderIsThreadParticipant(context.Background(), itemID, "sender@example.com") {
		t.Fatal("the item's recorded contact (other channel) was not authorized")
	}
	// The sender's own prior message on another request is not a grant there.
	if processor.senderIsThreadParticipant(context.Background(), otherItemID, "sender@example.com") {
		t.Fatal("prior thread history on another item authorized this item")
	}
	// Unknown senders stay rejected.
	if processor.senderIsThreadParticipant(context.Background(), itemID, "stranger@example.com") {
		t.Fatal("unknown sender was authorized")
	}
}

// TestFindParentItemRoutesPortalReplyAcrossChannels is the WI-1546 loop: the
// ticket was created on the portal channel, the customer replies by email to
// the notification on the email intake channel. Matching happens on the
// recipient-bound reply key recorded when that notice was delivered, and the
// creator is recognized without any prior tracked message (WI-2365).
func TestFindParentItemRoutesPortalReplyAcrossChannels(t *testing.T) {
	db := newProcessorRegressionTestDB(t)
	var workspaceID int
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES ('Portal loop', 'PLP') RETURNING id`).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	var portalChannelID, emailChannelID int
	if err := db.QueryRow(`INSERT INTO channels (name, type, direction) VALUES ('Portal', 'portal', 'inbound') RETURNING id`).Scan(&portalChannelID); err != nil {
		t.Fatalf("insert portal channel: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO channels (name, type, direction) VALUES ('Mailbox', 'email', 'inbound') RETURNING id`).Scan(&emailChannelID); err != nil {
		t.Fatalf("insert email channel: %v", err)
	}
	var customerID int
	if err := db.QueryRow(`INSERT INTO portal_customers (name, email) VALUES ('Customer', 'customer@example.com') RETURNING id`).Scan(&customerID); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	var itemID int
	if err := db.QueryRow(`
		INSERT INTO items (workspace_id, workspace_item_number, title, channel_id, creator_portal_customer_id, frac_index)
		VALUES (?, 1, 'Portal request', ?, ?, ?) RETURNING id
	`, workspaceID, portalChannelID, customerID, testutils.NextTestFracIndex()).Scan(&itemID); err != nil {
		t.Fatalf("insert portal item: %v", err)
	}
	// The notice sent for this request carries a recipient-bound reply key.
	keyID := "<ws-notice-reply@windshift.local>"
	if _, err := db.ExecWrite(`
		INSERT INTO email_reply_outbox
			(kind, channel_id, item_id, to_email, subject, html_body, text_body,
			 message_id, key_generation, from_email)
		VALUES ('notice', ?, ?, 'customer@example.com', 'Your request', '', '', ?, 1, 'support@example.com')
	`, portalChannelID, itemID, keyID); err != nil {
		t.Fatalf("insert reply key: %v", err)
	}

	processor := NewProcessor(db, blobstore.Disabled())
	parent := processor.findParentItem(context.Background(), &ParsedEmail{
		InReplyTo: keyID,
		From:      EmailAddress{Address: "customer@example.com"},
	})
	if parent == nil || *parent != itemID {
		t.Fatalf("portal reply parent = %v, want %d", parent, itemID)
	}

	// A stranger quoting the leaked key must not land on the ticket: the key
	// names its intended recipient.
	if strangerParent := processor.findParentItem(context.Background(), &ParsedEmail{
		InReplyTo: keyID,
		From:      EmailAddress{Address: "stranger@example.com"},
	}); strangerParent != nil {
		t.Fatalf("stranger reply matched item %d; hijack guard failed", *strangerParent)
	}

	// An added participant of the request may also append after the key names
	// them, regardless of which channel the conversation started on.
	if _, err := db.ExecWrite(`
		INSERT INTO item_participants (item_id, portal_customer_id) VALUES (?, ?)
	`, itemID, customerID); err != nil {
		t.Fatalf("insert participant: %v", err)
	}
	if !processor.senderIsThreadParticipant(context.Background(), itemID, "customer@example.com") {
		t.Fatal("thread participant lost authorization after channel change")
	}
}

func TestFindParentItemAcceptsLegacyBareMessageID(t *testing.T) {
	db := newProcessorRegressionTestDB(t)
	var workspaceID, channelID, itemID int
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES ('Legacy thread', 'LTH') RETURNING id`).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO channels (name, type, direction) VALUES ('Legacy mailbox', 'email', 'inbound') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO items (workspace_id, workspace_item_number, title, channel_id, contact_email, frac_index)
		VALUES (?, 1, 'Legacy thread item', ?, 'sender@example.com', ?) RETURNING id
	`, workspaceID, channelID, testutils.NextTestFracIndex()).Scan(&itemID); err != nil {
		t.Fatalf("insert item: %v", err)
	}
	// A pre-upgrade outbound row: generation 0, no recipient, stored bare.
	if _, err := db.ExecWrite(`
		INSERT INTO email_message_tracking
			(channel_id, message_id, dedup_key, from_email, item_id, direction)
		VALUES (?, 'legacy@example.com', 'legacy@example.com', 'sender@example.com', ?, 'outbound')
	`, channelID, itemID); err != nil {
		t.Fatalf("insert legacy tracking: %v", err)
	}

	processor := NewProcessor(db, blobstore.Disabled())
	parent := processor.findParentItem(context.Background(), &ParsedEmail{
		InReplyTo: "<legacy@example.com>",
		From:      EmailAddress{Address: "sender@example.com"},
	})
	if parent == nil || *parent != itemID {
		t.Fatalf("legacy bare Message-ID parent = %v, want %d", parent, itemID)
	}

	// Once the ticket has issued a recipient-bound key the legacy row stops
	// authorizing, even after the outbox row is pruned (WI-2365).
	if _, err := db.ExecWrite(`UPDATE items SET reply_key_issued_at = CURRENT_TIMESTAMP WHERE id = ?`, itemID); err != nil {
		t.Fatalf("mark reply key issued: %v", err)
	}
	if stale := processor.findParentItem(context.Background(), &ParsedEmail{
		InReplyTo: "<legacy@example.com>",
		From:      EmailAddress{Address: "sender@example.com"},
	}); stale != nil {
		t.Fatalf("legacy row still authorized item %d after a reply key was issued", *stale)
	}
}
