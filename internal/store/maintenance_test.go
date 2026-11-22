package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestMaintainPrunesOldEventsAndDeliveredHooksWithoutOnChange(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }

	if _, err := st.CreateTeam(ctx, CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}

	// Insert 1050 old events (40 days ago) and 5 recent events.
	oldStamp := now.Add(-40 * 24 * time.Hour).Format("2006-01-02T15:04:05.000000-07:00")
	newStamp := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000000-07:00")
	for i := 0; i < 1050; i++ {
		if _, err := st.db.ExecContext(ctx,
			"INSERT INTO events (team, kind, body, created_at) VALUES (?, 'message', '{}', ?)",
			"demo", oldStamp); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := st.db.ExecContext(ctx,
			"INSERT INTO events (team, kind, body, created_at) VALUES (?, 'message', '{}', ?)",
			"demo", newStamp); err != nil {
			t.Fatal(err)
		}
	}

	// Insert 1010 delivered vikunja_hooks and 2 undelivered ones.
	for i := 0; i < 1010; i++ {
		if _, err := st.db.ExecContext(ctx,
			"INSERT INTO vikunja_hooks (team, fingerprint, body, delivered) VALUES (?, ?, '', 1)",
			"demo", fmt.Appendf(nil, "done-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := st.db.ExecContext(ctx,
			"INSERT INTO vikunja_hooks (team, fingerprint, body, delivered) VALUES (?, ?, '{}', 0)",
			"demo", fmt.Appendf(nil, "pending-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	var changes atomic.Int32
	st.OnChange = func() { changes.Add(1) }

	stats, err := st.Maintain(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if changes.Load() != 0 {
		t.Fatalf("Maintain triggered OnChange %d times, want 0", changes.Load())
	}
	if stats.EventsDeleted != 55 {
		t.Fatalf("EventsDeleted = %d, want 55 (1055 total - 1000 kept)", stats.EventsDeleted)
	}
	if stats.VikunjaDeleted != 10 {
		t.Fatalf("VikunjaDeleted = %d, want 10", stats.VikunjaDeleted)
	}

	var remainingPending int
	if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM vikunja_hooks WHERE delivered = 0").Scan(&remainingPending); err != nil {
		t.Fatal(err)
	}
	if remainingPending != 2 {
		t.Fatalf("undelivered vikunja_hooks = %d, want 2", remainingPending)
	}
}
