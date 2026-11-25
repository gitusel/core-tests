//go:build test

package services

import (
	"context"
	"sync"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/sideeffects"
	"windshift/internal/testutils"
)

// The SLA emitter stages a minimal occurrence on the caller's transaction; the
// bounded notification and automation lanes enrich it after commit. These
// tests pin that pipeline end to end: a configured rule produces one
// notification, a configured action produces one execution, and an import
// origin or missing configuration produces nothing.
type slaSideEffectFixture struct {
	db            database.Database
	emitter       *SLASideEffectEmitter
	manager       *recordingNotificationManager
	notifications *NotificationService
	actions       *ActionService
	dispatcher    *SideEffectDispatcher
	captured      *slaActionEventCapture
	workspaceID   int
	itemID        int
	metricID      int
	adminID       int
	configSetID   int
	settingID     int
}

// slaActionEventCapture is a NodeExecutor that records the ActionEvent the
// automation lane handed to its action. It pins the enriched NewValues
// contract (metric_id, cycle_id, threshold_key, …) without depending on what
// a built-in node type happens to do with them.
type slaActionEventCapture struct {
	mu     sync.Mutex
	events []models.ActionEvent
}

func (c *slaActionEventCapture) NodeType() models.ActionNodeType {
	return models.ActionNodeHTTPRequest
}

func (c *slaActionEventCapture) Execute(_ *models.ActionNode, ctx *models.ExecutionContext, _ *models.StepResult) error {
	if ctx == nil || ctx.Event == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, *ctx.Event)
	return nil
}

func (c *slaActionEventCapture) snapshot() []models.ActionEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]models.ActionEvent(nil), c.events...)
}

func newSLASideEffectFixture(t *testing.T) *slaSideEffectFixture {
	t.Helper()
	tdb := testutils.CreateTestDB(t, true)
	t.Cleanup(func() { _ = tdb.Close() })
	db := tdb.GetDatabase()

	var workspaceID int
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES ('SLA FX', 'SLAFX') RETURNING id`).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	var itemID int
	if err := db.QueryRow(`INSERT INTO items (workspace_id, workspace_item_number, title, frac_index) VALUES (?, 7, 'Overdue ticket', ?) RETURNING id`,
		workspaceID, testutils.NextTestFracIndex()).Scan(&itemID); err != nil {
		t.Fatalf("insert item: %v", err)
	}
	var metricID int
	if err := db.QueryRow(`INSERT INTO sla_metrics (workspace_id, name, is_active) VALUES (?, 'First response', true) RETURNING id`, workspaceID).Scan(&metricID); err != nil {
		t.Fatalf("insert metric: %v", err)
	}
	var adminID int
	if err := db.QueryRow(`INSERT INTO users (email, username, first_name, last_name)
		VALUES ('sla-admin@example.com', 'sla-admin', 'SLA', 'Admin') RETURNING id`).Scan(&adminID); err != nil {
		t.Fatalf("insert admin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_workspace_roles (user_id, workspace_id, role_id)
		SELECT ?, ?, id FROM workspace_roles WHERE builtin_key = ?`,
		adminID, workspaceID, models.RoleBuiltinAdministrator); err != nil {
		t.Fatalf("grant workspace administrator: %v", err)
	}
	// One configuration set per fixture, mapped to the workspace, so the
	// notification rule cache resolves rules for this workspace at construction.
	var configSetID int
	if err := db.QueryRow(`INSERT INTO configuration_sets (name, description, is_default) VALUES ('SLA config', '', false) RETURNING id`).Scan(&configSetID); err != nil {
		t.Fatalf("insert configuration set: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_configuration_sets (workspace_id, configuration_set_id) VALUES (?, ?)`,
		workspaceID, configSetID); err != nil {
		t.Fatalf("map workspace configuration set: %v", err)
	}

	return &slaSideEffectFixture{
		db: db, workspaceID: workspaceID, itemID: itemID, metricID: metricID,
		adminID: adminID, configSetID: configSetID, captured: &slaActionEventCapture{},
	}
}

// start wires the lanes after the test has configured its rules and actions.
// The transaction-effects factory is process-global, so it is restored on
// cleanup.
func (f *slaSideEffectFixture) start(t *testing.T) {
	t.Helper()
	f.manager = &recordingNotificationManager{}
	f.notifications = NewNotificationService(f.db, f.manager, nil, NotificationServiceConfig{
		RefreshInterval: time.Hour, EventWorkers: 2,
	})
	f.actions = NewActionService(f.db, DefaultActionServiceConfig(), nil)
	f.actions.RegisterNodeExecutor(f.captured)
	f.actions.InvalidateWorkspaceCache(f.workspaceID)
	assets := NewAssetActionService(f.db, DefaultActionServiceConfig(), nil)
	dispatcher, err := NewSideEffectDispatcher(f.db, f.notifications, f.actions, assets, DefaultSideEffectBudget())
	if err != nil {
		t.Fatalf("create side-effect dispatcher: %v", err)
	}
	f.dispatcher = dispatcher
	dispatcher.Start(context.Background())
	database.SetTxEffectsFactory(dispatcher.NewTransactionEffects)
	f.emitter = NewSLASideEffectEmitter()

	t.Cleanup(func() {
		database.SetTxEffectsFactory(nil)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		dispatcher.Shutdown(shutdownCtx)
		if err := f.notifications.CloseContext(shutdownCtx); err != nil {
			t.Errorf("close notification service: %v", err)
		}
		f.actions.Stop()
		assets.Stop()
	})
}

// enableNotification attaches an enabled rule for eventType to the fixture's
// configuration set, notifying the workspace administrators.
func (f *slaSideEffectFixture) enableNotification(t *testing.T, eventType string) {
	t.Helper()
	var settingID int
	if f.settingID == 0 {
		if err := f.db.QueryRow(`INSERT INTO notification_settings (name, is_active) VALUES ('SLA setting', true) RETURNING id`).Scan(&settingID); err != nil {
			t.Fatalf("insert notification setting: %v", err)
		}
		f.settingID = settingID
		if _, err := f.db.Exec(`INSERT INTO configuration_set_notification_settings (configuration_set_id, notification_setting_id) VALUES (?, ?)`,
			f.configSetID, settingID); err != nil {
			t.Fatalf("attach notification setting: %v", err)
		}
	} else {
		settingID = f.settingID
	}
	if _, err := f.db.Exec(`INSERT INTO notification_event_rules (notification_setting_id, event_type, is_enabled, notify_workspace_admins)
		VALUES (?, ?, true, true)`, settingID, eventType); err != nil {
		t.Fatalf("insert notification rule: %v", err)
	}
}

// enableAction inserts an enabled action for trigger, gives it one node that
// the fixture's capture executor handles, and returns the action id.
func (f *slaSideEffectFixture) enableAction(t *testing.T, trigger string) int {
	t.Helper()
	var id int
	if err := f.db.QueryRow(`INSERT INTO actions (workspace_id, name, trigger_type, is_enabled)
		VALUES (?, 'Page on-call', ?, true) RETURNING id`, f.workspaceID, trigger).Scan(&id); err != nil {
		t.Fatalf("insert action: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO action_nodes (action_id, node_type, node_config) VALUES (?, ?, '{}')`,
		id, string(models.ActionNodeHTTPRequest)); err != nil {
		t.Fatalf("insert action probe node: %v", err)
	}
	return id
}

func (f *slaSideEffectFixture) cycle(breachedAt time.Time) *models.ItemSLACycle {
	return &models.ItemSLACycle{
		ID: 42, ItemID: f.itemID, MetricID: f.metricID, CycleNo: 1,
		Status: models.SLACycleOngoing, GoalDurationMs: 3_600_000, ElapsedMs: 4_000_000,
		BreachedAt: &breachedAt,
	}
}

func (f *slaSideEffectFixture) emitBreach(t *testing.T, cycle *models.ItemSLACycle, deadline time.Time) {
	t.Helper()
	if err := database.WithTx(f.db, func(tx database.Tx) error {
		return f.emitter.EmitBreach(context.Background(), tx, cycle, f.workspaceID, deadline)
	}); err != nil {
		t.Fatalf("EmitBreach: %v", err)
	}
}

func (f *slaSideEffectFixture) emitWarning(t *testing.T, cycle *models.ItemSLACycle, thresholdKey string, deadline time.Time) {
	t.Helper()
	if err := database.WithTx(f.db, func(tx database.Tx) error {
		return f.emitter.EmitWarning(context.Background(), tx, cycle, f.workspaceID, thresholdKey, deadline)
	}); err != nil {
		t.Fatalf("EmitWarning: %v", err)
	}
}

// laneSettled reports whether a lane has accounted for every candidate it
// admitted: nothing queued or active, and each admitted candidate either
// finished (processed/failed/timed out) or was dropped.
func laneSettled(stats sideeffects.LaneStats) bool {
	if stats.Queued != 0 || stats.Active != 0 {
		return false
	}
	accounted := stats.Processed + stats.Failed + stats.TimedOut + stats.ShutdownDiscarded
	for _, count := range stats.Dropped {
		accounted += count
	}
	return accounted >= stats.Admitted
}

// drain waits until both lanes have fully accounted for every admitted
// candidate. Idle queue/active alone is not enough: a worker decrements the
// queue as soon as it pulls a candidate, before the handler finishes.
func (f *slaSideEffectFixture) drain(t *testing.T) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if laneSettled(f.notifications.lane.Stats()) && laneSettled(f.dispatcher.Stats()) {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("side-effect lanes did not drain: notification=%+v automation=%+v",
				f.notifications.lane.Stats(), f.dispatcher.Stats())
		}
	}
}

func (f *slaSideEffectFixture) notificationsSeen() []models.Notification {
	f.manager.mu.Lock()
	defer f.manager.mu.Unlock()
	var all []models.Notification
	for _, batch := range f.manager.batches {
		all = append(all, batch...)
	}
	return all
}

func (f *slaSideEffectFixture) executionLogs(t *testing.T, actionID int) []struct {
	trigger string
	status  string
} {
	t.Helper()
	rows, err := f.db.Query(`SELECT trigger_event, status FROM action_execution_logs WHERE action_id = ? ORDER BY id`, actionID)
	if err != nil {
		t.Fatalf("query execution logs: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var logs []struct {
		trigger string
		status  string
	}
	for rows.Next() {
		var entry struct {
			trigger string
			status  string
		}
		if err := rows.Scan(&entry.trigger, &entry.status); err != nil {
			t.Fatalf("scan execution log: %v", err)
		}
		logs = append(logs, entry)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate execution logs: %v", err)
	}
	return logs
}

func (f *slaSideEffectFixture) actionExecutionCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM action_execution_logs`).Scan(&count); err != nil {
		t.Fatalf("count execution logs: %v", err)
	}
	return count
}

func TestSLASideEffectEmitterEmitsConfiguredNotification(t *testing.T) {
	fixture := newSLASideEffectFixture(t)
	fixture.enableNotification(t, models.EventSLABreached)
	fixture.start(t)
	deadline := time.Date(2025, 1, 8, 13, 0, 0, 0, time.UTC)

	fixture.emitBreach(t, fixture.cycle(deadline), deadline)
	fixture.drain(t)

	notifications := fixture.notificationsSeen()
	if len(notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifications))
	}
	notification := notifications[0]
	if notification.UserID != fixture.adminID {
		t.Fatalf("recipient = %d, want workspace administrator %d", notification.UserID, fixture.adminID)
	}
	if notification.SourceType != models.EventSLABreached || notification.ItemID == nil || *notification.ItemID != fixture.itemID {
		t.Fatalf("notification source = %q item %v, want %q item %d",
			notification.SourceType, notification.ItemID, models.EventSLABreached, fixture.itemID)
	}
	if notification.WorkspaceID == nil || *notification.WorkspaceID != fixture.workspaceID {
		t.Fatalf("notification workspace = %v, want %d", notification.WorkspaceID, fixture.workspaceID)
	}
	// The rendered copy names the metric and the item key, proving the worker
	// enriched the committed occurrence with live item/metric data.
	if want := "SLA breached (First response) for: SLAFX-7"; notification.Message != want {
		t.Fatalf("message = %q, want %q", notification.Message, want)
	}
	if notification.DeliveryKey == "" {
		t.Fatal("notification has no occurrence delivery key")
	}
	if got := fixture.actionExecutionCount(t); got != 0 {
		t.Fatalf("action execution logs = %d, want 0 without a rule", got)
	}
}

// TestSLASideEffectEmitterKeysNotificationsByFiringDeadline pins the WI-2273
// dedupe contract: one deadline yields one delivery key, while a re-armed
// cycle's later deadline gets a distinct key. Two workers drain the lane, so
// arrivals are unordered and the keys are compared as counts.
func TestSLASideEffectEmitterKeysNotificationsByFiringDeadline(t *testing.T) {
	fixture := newSLASideEffectFixture(t)
	fixture.enableNotification(t, models.EventSLABreached)
	fixture.start(t)
	first := time.Date(2025, 1, 8, 13, 0, 0, 0, time.UTC)
	second := first.Add(2 * time.Hour)

	fixture.emitBreach(t, fixture.cycle(first), first)
	fixture.emitBreach(t, fixture.cycle(first), first)
	fixture.emitBreach(t, fixture.cycle(second), second)
	fixture.drain(t)

	counts := keyCounts(fixture.notificationsSeen())
	if len(counts) != 2 {
		t.Fatalf("distinct delivery keys = %d (%v), want one per deadline", len(counts), counts)
	}
	repeated, single := 0, 0
	for key, count := range counts {
		if key == "" {
			t.Fatalf("occurrence notification has no delivery key: %v", counts)
		}
		switch count {
		case 2:
			repeated++
		case 1:
			single++
		default:
			t.Fatalf("key %q delivered %d times, want 2 (duplicate observation) or 1 (re-armed deadline): %v", key, count, counts)
		}
	}
	if repeated != 1 || single != 1 {
		t.Fatalf("keying = %d repeated, %d single, want 1/1: %v", repeated, single, counts)
	}
}

// keyCounts tallies delivery keys across stored notifications.
func keyCounts(notifications []models.Notification) map[string]int {
	counts := make(map[string]int, len(notifications))
	for _, notification := range notifications {
		counts[notification.DeliveryKey]++
	}
	return counts
}

func TestSLASideEffectEmitterGatedWithoutConfiguration(t *testing.T) {
	fixture := newSLASideEffectFixture(t)
	fixture.start(t)

	fixture.emitBreach(t, fixture.cycle(time.Now().UTC()), time.Now().UTC())
	fixture.drain(t)

	if got := len(fixture.notificationsSeen()); got != 0 {
		t.Fatalf("notifications = %d, want 0", got)
	}
	if got := fixture.actionExecutionCount(t); got != 0 {
		t.Fatalf("action execution logs = %d, want 0", got)
	}
}

func TestSLASideEffectEmitterEmitsConfiguredAction(t *testing.T) {
	fixture := newSLASideEffectFixture(t)
	actionID := fixture.enableAction(t, string(models.ActionTriggerSLABreached))
	fixture.start(t)

	fixture.emitBreach(t, fixture.cycle(time.Now().UTC()), time.Now().UTC())
	fixture.drain(t)

	logs := fixture.executionLogs(t, actionID)
	if len(logs) != 1 {
		t.Fatalf("action execution logs = %d, want 1", len(logs))
	}
	if logs[0].trigger != string(models.ActionTriggerSLABreached) {
		t.Fatalf("trigger_event = %q, want %q", logs[0].trigger, models.ActionTriggerSLABreached)
	}
	if logs[0].status != string(models.ActionStatusCompleted) {
		t.Fatalf("execution status = %q, want completed", logs[0].status)
	}
	if got := len(fixture.notificationsSeen()); got != 0 {
		t.Fatalf("notifications = %d, want 0 without a notification rule", got)
	}
	// The enriched trigger must carry the SLA identity the action's conditions
	// and templates read, not just the trigger name.
	events := fixture.captured.snapshot()
	if len(events) != 1 {
		t.Fatalf("captured action events = %d, want 1", len(events))
	}
	event := events[0]
	if event.EventType != models.ActionTriggerSLABreached {
		t.Fatalf("captured event type = %q, want %q", event.EventType, models.ActionTriggerSLABreached)
	}
	if event.ItemID != fixture.itemID || event.WorkspaceID != fixture.workspaceID {
		t.Fatalf("captured event target = ws:%d item:%d, want ws:%d item:%d",
			event.WorkspaceID, event.ItemID, fixture.workspaceID, fixture.itemID)
	}
	if got := event.NewValues["metric_id"]; got != fixture.metricID {
		t.Fatalf("metric_id = %v, want %d", got, fixture.metricID)
	}
	// cycle_id is the ItemSLACycle's int64 ID, preserved unchanged.
	if got := event.NewValues["cycle_id"]; got != int64(42) {
		t.Fatalf("cycle_id = %v (%T), want int64(42)", got, got)
	}
	if _, present := event.NewValues["threshold_key"]; present {
		t.Fatalf("breach event must not carry a warning threshold: %v", event.NewValues["threshold_key"])
	}
}

func TestSLASideEffectEmitterWarningUsesWarningTriggers(t *testing.T) {
	fixture := newSLASideEffectFixture(t)
	fixture.enableNotification(t, models.EventSLAWarning)
	actionID := fixture.enableAction(t, string(models.ActionTriggerSLAWarning))
	fixture.start(t)
	deadline := time.Date(2025, 1, 8, 13, 0, 0, 0, time.UTC)

	fixture.emitWarning(t, fixture.cycle(deadline), "75pct", deadline)
	fixture.drain(t)

	notifications := fixture.notificationsSeen()
	if len(notifications) != 1 {
		t.Fatalf("warning notifications = %d, want 1", len(notifications))
	}
	if notifications[0].SourceType != models.EventSLAWarning {
		t.Fatalf("warning source type = %q", notifications[0].SourceType)
	}

	logs := fixture.executionLogs(t, actionID)
	if len(logs) != 1 {
		t.Fatalf("warning action execution logs = %d, want 1", len(logs))
	}
	if logs[0].trigger != string(models.ActionTriggerSLAWarning) {
		t.Fatalf("warning action trigger = %q", logs[0].trigger)
	}
	events := fixture.captured.snapshot()
	if len(events) != 1 {
		t.Fatalf("captured warning events = %d, want 1", len(events))
	}
	if got := events[0].NewValues["threshold_key"]; got != "75pct" {
		t.Fatalf("threshold_key = %v, want 75pct", got)
	}
	if got := events[0].NewValues["metric_id"]; got != fixture.metricID {
		t.Fatalf("metric_id = %v, want %d", got, fixture.metricID)
	}
}

// TestSLASideEffectEmitterKeysWarningsPerThreshold pins that distinct warning
// thresholds for one cycle are distinct effects, while a repeat of the same
// threshold is the same effect.
func TestSLASideEffectEmitterKeysWarningsPerThreshold(t *testing.T) {
	fixture := newSLASideEffectFixture(t)
	fixture.enableNotification(t, models.EventSLAWarning)
	fixture.start(t)
	deadline := time.Date(2025, 1, 8, 13, 0, 0, 0, time.UTC)

	fixture.emitWarning(t, fixture.cycle(deadline), "75pct", deadline)
	fixture.emitWarning(t, fixture.cycle(deadline), "75pct", deadline)
	fixture.emitWarning(t, fixture.cycle(deadline), "50pct", deadline)
	fixture.drain(t)

	counts := keyCounts(fixture.notificationsSeen())
	if len(counts) != 2 {
		t.Fatalf("distinct delivery keys = %d (%v), want one per threshold", len(counts), counts)
	}
	repeated, single := 0, 0
	for _, count := range counts {
		switch count {
		case 2:
			repeated++
		case 1:
			single++
		default:
			t.Fatalf("unexpected delivery count %d in %v", count, counts)
		}
	}
	if repeated != 1 || single != 1 {
		t.Fatalf("threshold keying = %d repeated, %d single, want 1/1: %v", repeated, single, counts)
	}
}

func TestSLASideEffectEmitterPreservesImportOrigin(t *testing.T) {
	fixture := newSLASideEffectFixture(t)
	fixture.enableNotification(t, models.EventSLABreached)
	actionID := fixture.enableAction(t, string(models.ActionTriggerSLABreached))
	fixture.start(t)
	deadline := time.Date(2025, 1, 8, 13, 0, 0, 0, time.UTC)
	cycle := fixture.cycle(deadline)
	cycle.Origin = models.SLAOriginImport

	fixture.emitBreach(t, cycle, deadline)

	// Publishing is synchronous with the commit, so a staged occurrence would
	// already be admitted by the time WithTx returned.
	if admitted := fixture.notifications.lane.Stats().Admitted; admitted != 0 {
		t.Fatalf("notification lane admitted %d candidates for an import", admitted)
	}
	if admitted := fixture.dispatcher.Stats().Admitted; admitted != 0 {
		t.Fatalf("automation lane admitted %d candidates for an import", admitted)
	}
	fixture.drain(t)
	if got := len(fixture.notificationsSeen()); got != 0 {
		t.Fatalf("import produced %d notifications", got)
	}
	if got := fixture.actionExecutionCount(t); got != 0 {
		t.Fatalf("import produced %d action executions", got)
	}
	if logs := fixture.executionLogs(t, actionID); len(logs) != 0 {
		t.Fatalf("import executed action %d: %+v", actionID, logs)
	}
}
