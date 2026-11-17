package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestProtocolPublicationSurvivesRestartAndKeepsRun(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "protocol.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close() }()
	newTeam(t, st, "alice")
	a, err := st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", RunID: "run-one", PID: 123, Target: "%7"})
	if err != nil {
		t.Fatal(err)
	}
	v := strings.Repeat("a", 64)
	if err = st.PublishProtocol(ctx, v); err != nil {
		t.Fatal(err)
	}
	read := func(action, run, version string) *model.ProtocolState {
		t.Helper()
		p, e := st.RecordProtocol(ctx, "demo", "alice", model.ProtocolRequest{Action: action, RunID: run, Version: version})
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	if _, err = st.RecordProtocol(ctx, "demo", "alice", model.ProtocolRequest{Action: "ack", RunID: a.RunID, Version: v}); KindOf(err) != KindConflict {
		t.Fatalf("ack before read: %v", err)
	}
	for _, in := range []model.ProtocolRequest{{Action: "read", RunID: "old-run", Version: v}, {Action: "read", RunID: a.RunID, Version: strings.Repeat("b", 64)}} {
		if _, err = st.RecordProtocol(ctx, "demo", "alice", in); KindOf(err) != KindConflict {
			t.Fatalf("stale report: %v", err)
		}
	}
	p := read("read", a.RunID, v)
	p = read("ack", a.RunID, v)
	stamp := p.AckedAt
	if stamp == "" || p.ReadAt == "" {
		t.Fatalf("missing receipts: %+v", p)
	}
	if p = read("ack", a.RunID, v); p.AckedAt != stamp {
		t.Fatal("retry changed acknowledgment")
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.PublishProtocol(ctx, v); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAgent(ctx, "demo", "alice")
	if err != nil || got.RunID != a.RunID || got.PID != a.PID || got.Target != a.Target || got.Protocol.AckedAt != stamp {
		t.Fatalf("publication disturbed run: %+v %v", got, err)
	}
	msgs, err := st.Peek(ctx, "demo", "alice", "")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("publication repeated across restart: %+v %v", msgs, err)
	}
	if _, err = st.RegisterAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAgent(ctx, "demo", "alice")
	if err != nil || got.Protocol.AckedAt != stamp {
		t.Fatal("same run registration lost receipt", err)
	}
	next := strings.Repeat("b", 64)
	if err = st.PublishProtocol(ctx, next); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAgent(ctx, "demo", "alice")
	if err != nil || got.Protocol.Version != next || got.Protocol.ReadAt != "" || got.Protocol.AckedAt != "" {
		t.Fatalf("new revision kept old receipt: %+v %v", got, err)
	}
	if _, err = st.AgentExitedRun(ctx, "demo", "alice", a.RunID, 0); err != nil {
		t.Fatal(err)
	}
	a.RunID = "run-two"
	got, err = st.RegisterAgent(ctx, a)
	if err != nil || got.Protocol.Version != next || got.Protocol.AckedAt != "" {
		t.Fatalf("new run inherited receipt: %+v %v", got, err)
	}
}
