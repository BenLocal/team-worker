package server

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func TestAnnouncerTellsEachLeaderAboutNewUnassignedTasksOnce(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "announce.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for _, team := range []string{"alpha", "beta"} {
		if _, err := st.CreateTeam(ctx, store.CreateTeamInput{Name: team, LeaderName: team + "-lead", LeaderID: team + "-id"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateTask(ctx, "alpha", store.CreateTaskInput{Subject: "old"}); err != nil {
		t.Fatal(err)
	}
	a := NewAnnouncer(st, slog.New(slog.DiscardHandler))
	a.Tick(ctx)
	inbox := func(team, to string) []model.Message {
		t.Helper()
		msgs, err := st.Peek(ctx, team, to, "")
		if err != nil {
			t.Fatal(err)
		}
		return msgs
	}
	if len(inbox("alpha", "alpha-lead")) != 0 {
		t.Fatal("tasks present at start must not be announced")
	}
	fresh, err := st.CreateTask(ctx, "alpha", store.CreateTaskInput{Subject: "new job", Priority: model.PriorityHigh})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTask(ctx, "alpha", store.CreateTaskInput{Subject: "assigned", Owner: "bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTask(ctx, "beta", store.CreateTaskInput{Subject: "beta job"}); err != nil {
		t.Fatal(err)
	}
	a.Tick(ctx)
	a.Tick(ctx)
	alpha := inbox("alpha", "alpha-lead")
	if len(alpha) != 1 || alpha[0].From != "team-worker" || !strings.Contains(alpha[0].Content, "- "+fresh.ID+" [high] new job") ||
		strings.Contains(alpha[0].Content, "] old") || strings.Contains(alpha[0].Content, "] assigned") {
		t.Fatalf("alpha announcements: %+v", alpha)
	}
	if beta := inbox("beta", "beta-lead"); len(beta) != 1 || !strings.Contains(beta[0].Content, "beta job") {
		t.Fatalf("beta announcements: %+v", beta)
	}
}
