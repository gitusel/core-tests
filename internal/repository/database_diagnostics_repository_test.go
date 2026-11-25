package repository

import (
	"path/filepath"
	"testing"

	"windshift/internal/database"
)

func TestCalculateDatabaseCapacityBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		serverMax  int
		main       int
		auxiliary  int
		replicas   int
		headroom   int
		wantNeeded int
		wantSafe   bool
	}{
		{name: "safe single replica", serverMax: 100, main: 30, auxiliary: 5, replicas: 1, headroom: 10, wantNeeded: 45, wantSafe: true},
		{name: "safe two replicas", serverMax: 100, main: 20, auxiliary: 5, replicas: 2, headroom: 10, wantNeeded: 60, wantSafe: true},
		{name: "unsafe aggregate", serverMax: 100, main: 30, auxiliary: 5, replicas: 3, headroom: 10, wantNeeded: 115, wantSafe: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			budget, err := CalculateDatabaseCapacityBudget(test.serverMax, test.main, test.auxiliary, test.replicas, test.headroom)
			if err != nil {
				t.Fatalf("calculate budget: %v", err)
			}
			if budget.RequiredConnections != test.wantNeeded || budget.Safe != test.wantSafe {
				t.Fatalf("budget = %+v, want required=%d safe=%v", budget, test.wantNeeded, test.wantSafe)
			}
			if budget.RemainingConnections != test.serverMax-test.wantNeeded {
				t.Fatalf("remaining = %d, want %d", budget.RemainingConnections, test.serverMax-test.wantNeeded)
			}
		})
	}
}

func TestCalculateDatabaseCapacityBudgetRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	for _, args := range [][5]int{
		{0, 30, 0, 1, 10},
		{100, 0, 0, 1, 10},
		{100, 30, -1, 1, 10},
		{100, 30, 0, 0, 10},
		{100, 30, 0, 1, -1},
	} {
		if _, err := CalculateDatabaseCapacityBudget(args[0], args[1], args[2], args[3], args[4]); err == nil {
			t.Fatalf("CalculateDatabaseCapacityBudget%v returned nil error", args)
		}
	}
}

func TestDatabaseDiagnosticsRepositoryRegistersNamedPools(t *testing.T) {
	mainDB, err := database.NewSQLiteDBWithPoolSizes(filepath.Join(t.TempDir(), "main.db"), 4, 1)
	if err != nil {
		t.Fatalf("create main database: %v", err)
	}
	t.Cleanup(func() { _ = mainDB.Close() })
	sshDB, err := database.NewSQLiteDBWithPoolSizes(filepath.Join(t.TempDir(), "ssh.db"), 2, 1)
	if err != nil {
		t.Fatalf("create SSH database: %v", err)
	}
	t.Cleanup(func() { _ = sshDB.Close() })

	repo := NewDatabaseDiagnosticsRepository(mainDB)
	if err := repo.RegisterPool("ssh", sshDB); err != nil {
		t.Fatalf("register SSH pool: %v", err)
	}
	stats := repo.PoolStats()
	// One row per physical pool: SQLite reports its reader and writer pools
	// separately, so the main and SSH databases contribute two rows each.
	byNameRole := make(map[string]DatabasePoolStats, len(stats))
	for _, pool := range stats {
		key := pool.Name + "/" + pool.Role
		if _, seen := byNameRole[key]; seen {
			t.Fatalf("duplicate pool row %q", key)
		}
		byNameRole[key] = pool
	}
	want := map[string]int{
		"main/reader": 4,
		"main/writer": 1,
		"ssh/reader":  2,
		"ssh/writer":  1,
	}
	if len(byNameRole) != len(want) {
		t.Fatalf("pool count = %d, want %d (%v)", len(byNameRole), len(want), byNameRole)
	}
	for key, maxOpen := range want {
		pool, ok := byNameRole[key]
		if !ok {
			t.Fatalf("missing pool row %q; got %v", key, byNameRole)
		}
		if pool.MaxOpenConnections != maxOpen {
			t.Fatalf("%s MaxOpenConnections = %d, want %d", key, pool.MaxOpenConnections, maxOpen)
		}
	}
}
