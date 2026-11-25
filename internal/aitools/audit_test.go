package aitools

import (
	"encoding/json"
	"testing"

	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/testutils"
)

// newAuditTestDB creates an isolated initialized test DB plus a single user
// row, returning the DB handle and the user's allocated row ID. audit_logs has
// a FK on users(id), so a real user must exist before any audit row can land.
func newAuditTestDB(t *testing.T, fullName string) (database.Database, int) {
	t.Helper()
	tdb := testutils.CreateTestDB(t, true)
	if tdb.Engine == "sqlite" {
		t.Cleanup(func() { _ = tdb.Close() })
	}
	db := tdb.DB
	var uid int64
	err := db.QueryRow(`
		INSERT INTO users (username, email, first_name, last_name, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, '', '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
	`, fullName, fullName+"@example.test", fullName).Scan(&uid)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return db, int(uid)
}

// newAuditTestEnv wires an Env for an agent-driven action write plus a
// workspace the action can live in (actions.workspace_id has an FK).
func newAuditTestEnv(t *testing.T, fullName, source string) (*Env, int) {
	t.Helper()
	db, userID := newAuditTestDB(t, fullName)
	var workspaceID int
	if err := db.QueryRow(`
		INSERT INTO workspaces (name, key, active) VALUES (?, ?, true) RETURNING id
	`, fullName+" workspace", "AUD").Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	return &Env{DB: db, UserID: userID, Username: fullName, Source: source}, workspaceID
}

// auditRow is one persisted audit_logs row, decoded for assertions.
type auditRow struct {
	UserID       int
	Username     string
	ActionType   string
	ResourceType string
	ResourceID   int
	ResourceName string
	Source       string
	Success      bool
	Details      map[string]any
}

// latestActionAudit reads the newest audit row for an automation action type,
// which is the row the definition service wrote in the action's transaction.
func latestActionAudit(t *testing.T, db database.Database, actionType string) auditRow {
	t.Helper()
	var (
		row     auditRow
		details string
	)
	if err := db.QueryRow(`
		SELECT user_id, username, action_type, resource_type, resource_id,
		       resource_name, COALESCE(details, ''), success, source
		FROM audit_logs
		WHERE action_type = ?
		ORDER BY id DESC LIMIT 1
	`, actionType).Scan(&row.UserID, &row.Username, &row.ActionType, &row.ResourceType,
		&row.ResourceID, &row.ResourceName, &details, &row.Success, &row.Source); err != nil {
		t.Fatalf("query audit_logs for %q: %v", actionType, err)
	}
	if err := json.Unmarshal([]byte(details), &row.Details); err != nil {
		t.Fatalf("unmarshal details: %v (raw: %s)", err, details)
	}
	return row
}

// TestActionWriteMeta_PersistsAgentAuditRow drives the real agent action-create
// path: Env.actionWriteMeta builds the transaction-coupled write metadata and
// the definition service persists it with the revision. The resulting audit row
// must be distinguishable from a cookie-auth write via both the Source column
// and details.source. The HTTP handler already audits these mutations; this
// test pins the equivalent behavior for the chat / MCP path.
func TestActionWriteMeta_PersistsAgentAuditRow(t *testing.T) {
	env, workspaceID := newAuditTestEnv(t, "Alice", SourceAIChat)

	repo := repository.NewActionRepository(env.DB)
	definitions := services.NewActionDefinitionService(env.DB, repo, nil)
	action := &models.Action{
		WorkspaceID:   workspaceID,
		Name:          "My Automation",
		IsEnabled:     true,
		TriggerType:   models.ActionTriggerManual,
		TriggerConfig: "{}",
		CreatedBy:     &env.UserID,
	}
	created, validation, err := definitions.Create(action, nil, nil,
		env.actionWriteMeta(logger.ActionAutomationCreate, map[string]any{"workspace_id": workspaceID}))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(validation) > 0 {
		t.Fatalf("Create() validation errors = %v", validation)
	}

	row := latestActionAudit(t, env.DB, logger.ActionAutomationCreate)
	if row.UserID != env.UserID {
		t.Errorf("user_id: want %d, got %d", env.UserID, row.UserID)
	}
	if row.Username != "Alice" {
		t.Errorf("username: want Alice, got %q", row.Username)
	}
	if row.ResourceType != logger.ResourceAutomation {
		t.Errorf("resource_type: want %q, got %q", logger.ResourceAutomation, row.ResourceType)
	}
	if row.ResourceID != created.ID {
		t.Errorf("resource_id: want %d, got %d", created.ID, row.ResourceID)
	}
	if row.ResourceName != "My Automation" {
		t.Errorf("resource_name: want %q, got %q", "My Automation", row.ResourceName)
	}
	if !row.Success {
		t.Errorf("success: want true, got false")
	}
	if row.Source != SourceAIChat {
		t.Errorf("source column: want %q, got %q", SourceAIChat, row.Source)
	}
	// workspace_id round-trips through JSON as a float64.
	if ws, ok := row.Details["workspace_id"].(float64); !ok || int(ws) != workspaceID {
		t.Errorf("details.workspace_id: want %d, got %v", workspaceID, row.Details["workspace_id"])
	}
	if got := row.Details["source"]; got != SourceAIChat {
		t.Errorf("details.source: want %q, got %v", SourceAIChat, got)
	}
}

// TestActionWriteMeta_MCPSource pins that the MCP adapter's Source is recorded
// distinctly from the chat path on an agent-driven action update, and that the
// update audit carries the revision the definition service created.
func TestActionWriteMeta_MCPSource(t *testing.T) {
	env, workspaceID := newAuditTestEnv(t, "Bob", SourceMCP)

	repo := repository.NewActionRepository(env.DB)
	definitions := services.NewActionDefinitionService(env.DB, repo, nil)
	action := &models.Action{
		WorkspaceID:   workspaceID,
		Name:          "x",
		IsEnabled:     true,
		TriggerType:   models.ActionTriggerManual,
		TriggerConfig: "{}",
		CreatedBy:     &env.UserID,
	}
	created, validation, err := definitions.Create(action, nil, nil,
		env.actionWriteMeta(logger.ActionAutomationCreate, map[string]any{"workspace_id": workspaceID}))
	if err != nil || len(validation) > 0 {
		t.Fatalf("Create() = (%v, %v)", validation, err)
	}

	created.Name = "x renamed"
	updated, validation, err := definitions.Save(created, nil, nil, true, true, models.ActionActorUpdate{},
		env.actionWriteMeta(logger.ActionAutomationUpdate, map[string]any{"workspace_id": workspaceID}))
	if err != nil || len(validation) > 0 {
		t.Fatalf("Save() = (%v, %v)", validation, err)
	}

	row := latestActionAudit(t, env.DB, logger.ActionAutomationUpdate)
	if row.Source != SourceMCP {
		t.Errorf("source column: want %q, got %q", SourceMCP, row.Source)
	}
	if got := row.Details["source"]; got != SourceMCP {
		t.Errorf("details.source: want %q, got %v", SourceMCP, got)
	}
	if row.ResourceName != "x renamed" {
		t.Errorf("resource_name: want %q, got %q", "x renamed", row.ResourceName)
	}
	if row.ResourceID != updated.ID {
		t.Errorf("resource_id: want %d, got %d", updated.ID, row.ResourceID)
	}
	if rev, ok := row.Details["revision_number"].(float64); !ok || int(rev) != 2 {
		t.Errorf("details.revision_number: want 2 (create then update), got %v", row.Details["revision_number"])
	}
	if _, ok := row.Details["revision_id"]; !ok {
		t.Errorf("details.revision_id missing: %v", row.Details)
	}
}

// TestAuditWrite_WritesRow pins the generic write-audit helper every mutating
// aitool calls (WI-357): the tool name lands as the action type, the entity
// type/id as the resource, and details.source tags the initiating surface.
func TestAuditWrite_WritesRow(t *testing.T) {
	db, userID := newAuditTestDB(t, "Carol")

	env := &Env{DB: db, UserID: userID, Username: "Carol", Source: SourceMCP}
	env.AuditWrite(logger.ResourceItem, 55, "update_item", "Fix the flux capacitor")

	var (
		gotUserID       int
		gotActionType   string
		gotResourceType string
		gotResourceID   int
		gotResourceName string
		gotDetails      string
		gotSuccess      bool
		gotSource       string
	)
	row := db.QueryRow(`
		SELECT user_id, action_type, resource_type, resource_id,
		       resource_name, COALESCE(details, ''), success, source
		FROM audit_logs
		WHERE action_type = 'update_item'
		ORDER BY id DESC LIMIT 1
	`)
	if err := row.Scan(&gotUserID, &gotActionType, &gotResourceType,
		&gotResourceID, &gotResourceName, &gotDetails, &gotSuccess, &gotSource); err != nil {
		t.Fatalf("query audit_logs: %v", err)
	}

	if gotUserID != userID {
		t.Errorf("user_id: want %d, got %d", userID, gotUserID)
	}
	if gotResourceType != logger.ResourceItem {
		t.Errorf("resource_type: want %q, got %q", logger.ResourceItem, gotResourceType)
	}
	if gotResourceID != 55 {
		t.Errorf("resource_id: want 55, got %d", gotResourceID)
	}
	if gotResourceName != "Fix the flux capacitor" {
		t.Errorf("resource_name: want %q, got %q", "Fix the flux capacitor", gotResourceName)
	}
	if !gotSuccess {
		t.Errorf("success: want true, got false")
	}
	if gotSource != SourceMCP {
		t.Errorf("source: want %q, got %q", SourceMCP, gotSource)
	}
}

func TestAuditWrite_StandardAgentAddsCorrelationWithoutToolPayloads(t *testing.T) {
	db, userID := newAuditTestDB(t, "StudioAgent")
	env := &Env{
		DB:       db,
		UserID:   userID,
		Username: "StudioAgent",
		Source:   SourceStandardAgent,
		AuditDetails: map[string]interface{}{
			"agent_run_id":              71,
			"agent_profile_id":          9,
			"root_initiator_user_id":    12,
			"immediate_trigger_user_id": 13,
			"acting_user_id":            userID,
			"workspace_id":              2,
			"item_id":                   848,
		},
	}
	env.AuditWrite(logger.ResourceItem, 848, "update_item", "Agent update")

	var details, source string
	if err := db.QueryRow(`
		SELECT details, source FROM audit_logs
		WHERE action_type = 'update_item'
		ORDER BY id DESC LIMIT 1
	`).Scan(&details, &source); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(details), &parsed); err != nil {
		t.Fatalf("unmarshal details: %v", err)
	}
	if source != SourceStandardAgent ||
		int(parsed["agent_run_id"].(float64)) != 71 ||
		int(parsed["root_initiator_user_id"].(float64)) != 12 {
		t.Fatalf("missing Standard correlation: source=%q details=%v", source, parsed)
	}
	for _, forbidden := range []string{"arguments", "result", "tool_arguments", "tool_result"} {
		if _, ok := parsed[forbidden]; ok {
			t.Fatalf("audit details persisted forbidden %q payload: %v", forbidden, parsed)
		}
	}
}

// TestAuditWrite_StandardAgentCorrelationSurvivesActionRevision keeps the
// adapter-supplied correlation ids on the transactional action audit path: an
// agent-driven automation write persists both the revision metadata and the
// run attribution, and never the raw tool payloads.
func TestAuditWrite_StandardAgentCorrelationSurvivesActionRevision(t *testing.T) {
	env, workspaceID := newAuditTestEnv(t, "StudioAgent", SourceStandardAgent)
	env.AuditDetails = map[string]any{"agent_run_id": 71, "root_initiator_user_id": 12}

	repo := repository.NewActionRepository(env.DB)
	definitions := services.NewActionDefinitionService(env.DB, repo, nil)
	action := &models.Action{
		WorkspaceID:   workspaceID,
		Name:          "Agent automation",
		IsEnabled:     true,
		TriggerType:   models.ActionTriggerManual,
		TriggerConfig: "{}",
		CreatedBy:     &env.UserID,
	}
	if _, validation, err := definitions.Create(action, nil, nil,
		env.actionWriteMeta(logger.ActionAutomationCreate, map[string]any{"workspace_id": workspaceID})); err != nil || len(validation) > 0 {
		t.Fatalf("Create() = (%v, %v)", validation, err)
	}

	row := latestActionAudit(t, env.DB, logger.ActionAutomationCreate)
	if row.Source != SourceStandardAgent || row.Details["source"] != SourceStandardAgent {
		t.Fatalf("missing Standard source: column=%q details=%v", row.Source, row.Details)
	}
	if int(row.Details["agent_run_id"].(float64)) != 71 || int(row.Details["root_initiator_user_id"].(float64)) != 12 {
		t.Fatalf("missing Standard correlation: %v", row.Details)
	}
	for _, forbidden := range []string{"arguments", "result", "tool_arguments", "tool_result"} {
		if _, ok := row.Details[forbidden]; ok {
			t.Fatalf("audit details persisted forbidden %q payload: %v", forbidden, row.Details)
		}
	}
}
