package services

import (
	"path/filepath"
	"testing"

	"windshift/internal/database"
)

// newMilestoneReleaseTestService provides an isolated database for tests that
// exercise milestone release persistence directly.
func newMilestoneReleaseTestService(t *testing.T) (*PlanningService, database.Database) {
	t.Helper()
	db, err := database.NewSQLiteDBWithPoolSizes(filepath.Join(t.TempDir(), "windshift.db"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Initialize(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return NewPlanningService(db), db
}

// insertGlobalMilestone inserts one planning-status global milestone row and
// returns its id.
func insertGlobalMilestone(t *testing.T, db database.Database, name string) int {
	t.Helper()
	var id int
	if err := db.QueryRow(`INSERT INTO milestones(name, description, status, is_global) VALUES (?, '', 'planning', true) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
