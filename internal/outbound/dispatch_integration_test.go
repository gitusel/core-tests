//go:build test

package outbound

import (
	"context"
	"testing"

	"windshift/internal/models"
	"windshift/internal/testutils"
)

// TestDispatchSelectsGraphChannelAndIgnoresLegacySMTPFlag checks the routing
// the local deploy depends on: a default type=graph channel is the Graph
// sender, and graph_send_provider on an SMTP channel does not divert mail.
func TestDispatchSelectsGraphChannelAndIgnoresLegacySMTPFlag(t *testing.T) {
	db := testutils.CreateTestDB(t, true)

	insert := func(name, typ string, isDefault int, config string) {
		t.Helper()
		if _, err := db.Exec(
			`INSERT INTO channels (name, type, direction, status, is_default, config) VALUES (?, ?, 'outbound', 'enabled', ?, ?)`,
			name, typ, isDefault, config,
		); err != nil {
			t.Fatalf("insert %s channel: %v", typ, err)
		}
	}

	t.Run("legacy smtp flag stays on smtp", func(t *testing.T) {
		// Seed data already has one outbound SMTP row; type+direction is unique.
		if _, err := db.Exec(
			`UPDATE channels SET name = ?, status = 'enabled', is_default = 1, config = ? WHERE type = 'smtp' AND direction = 'outbound'`,
			"Notification Mail",
			`{"graph_send_provider":"graph","graph_mailbox_upn":"box@contoso.example","graph_tenant_id":"t","graph_client_id":"c","graph_client_secret":"s","smtp_host":"smtp.example"}`,
		); err != nil {
			t.Fatalf("update smtp channel: %v", err)
		}
		got := dispatchOnce(t, db)
		if got != "smtp" {
			t.Fatalf("selected %q, want smtp; graph_send_provider must not divert the default SMTP channel", got)
		}
	})

	t.Run("graph channel is selected", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE channels SET is_default = 0`); err != nil {
			t.Fatalf("clear default: %v", err)
		}
		insert("Graph", "graph", 1, `{"graph_tenant_id":"t","graph_client_id":"c","graph_client_secret":"s","graph_from_email":"box@contoso.example"}`)
		got := dispatchOnce(t, db)
		if got != "graph" {
			t.Fatalf("selected %q, want graph", got)
		}
	})
}

func dispatchOnce(t *testing.T, db *testutils.TestDB) string {
	t.Helper()
	var selected string
	reg := NewRegistry()
	reg.Register("smtp", func(cfg *models.ChannelConfig) (Sender, error) {
		selected = "smtp"
		if cfg.GraphFromEmail != "" {
			t.Errorf("smtp adapter saw graph_from_email %q", cfg.GraphFromEmail)
		}
		return recordingSender{}, nil
	})
	reg.Register("graph", func(cfg *models.ChannelConfig) (Sender, error) {
		selected = "graph"
		if cfg.GraphFromEmail == "" {
			t.Error("graph adapter received empty graph_from_email")
		}
		return recordingSender{}, nil
	})
	d := NewDispatch(db, "http://localhost:8080", reg)
	if err := d.SendTransactional("a@example.com", "en", "unused", nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return selected
}

type recordingSender struct{}

func (recordingSender) IsConfigured() bool { return true }
func (recordingSender) SendTransactional(string, string, string, any) error { return nil }
func (recordingSender) SendTransactionalContext(context.Context, string, string, string, any) error {
	return nil
}
func (recordingSender) SendThreadedEmail(ThreadedEmailParams) error { return nil }
func (recordingSender) SendThreadedEmailContext(context.Context, ThreadedEmailParams) error {
	return nil
}
func (recordingSender) RenderEmail(string, any) (string, string, string, error) {
	return "", "", "", nil
}
func (recordingSender) RenderEmailLocale(string, string, any) (string, string, string, error) {
	return "", "", "", nil
}
func (recordingSender) SendBatchedNotifications(string, string, string, string, []models.Notification) error {
	return nil
}
func (recordingSender) SendBatchedNotificationsContext(context.Context, string, string, string, string, []models.Notification) error {
	return nil
}
