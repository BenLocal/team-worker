package chat

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func chatFixture(t *testing.T) (*Bridge, *[]Outbound, model.Agent) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := t.Context()
	account := model.ChatAccount{Provider: "feishu", Tenant: "app-scoped", Application: "cli_example"}
	var bindings []model.ChatBinding
	for _, name := range []string{"alpha", "beta", "restricted"} {
		if _, err := st.CreateTeam(ctx, store.CreateTeamInput{Name: name, LeaderName: "leader"}); err != nil {
			t.Fatal(err)
		}
		sender := "sender"
		if name == "restricted" {
			sender = "other"
		}
		bindings = append(bindings, model.ChatBinding{ChatAccount: account, Team: name, Alias: name, Senders: []string{sender},
			Receive: true, Notify: true, Reports: true})
	}
	leader, err := st.Leader(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.RegisterAgent(ctx, model.Agent{Team: "alpha", Name: leader.Name, AgentID: leader.AgentID, RunID: "fixture-run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetChatSettings(ctx, model.ChatSettings{Bindings: bindings,
		Recipient: &model.ChatRecipient{ChatAccount: account, OpenID: "ou_example"}}); err != nil {
		t.Fatal(err)
	}
	var outbound []Outbound
	b := &Bridge{Store: st, Account: account, Log: slog.Default(), Deliver: func(_ context.Context, out Outbound) (string, error) {
		outbound = append(outbound, out)
		return "om_" + out.ReplyTo, nil
	}}
	return b, &outbound, agent
}

func TestBridgeReturnsConcurrentResultsToTheirOrigins(t *testing.T) {
	ctx := t.Context()
	b, outbound, leader := chatFixture(t)
	group := Inbound{ID: "group-request", Sender: "sender", Conversation: "unbound-room", Text: "/tw send alpha group question", Group: true, Thread: true, Mentioned: true}
	dm := Inbound{ID: "private-request", Sender: "sender", Conversation: "private-room", Text: "Ask alpha for the private result"}
	denied := Inbound{ID: "unauthorized", Sender: "sender", Conversation: "private-room", Text: "/tw send restricted no permission"}
	unmentioned := Inbound{ID: "ordinary", Sender: "sender", Conversation: "unbound-room", Text: "/tw send alpha ignored", Group: true}
	for _, inbound := range []Inbound{group, group, dm, denied, unmentioned} {
		if err := b.Accept(ctx, inbound); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	inbox, err := b.Store.Peek(ctx, "alpha", "leader", "")
	if err != nil || len(inbox) != 1 {
		t.Fatalf("explicit route should work with the agent offline: %+v %v", inbox, err)
	}
	b.Agent = func(_ context.Context, mode string, in agentInput) (agentOutput, error) {
		if mode == "route" {
			if !slices.Equal(in.Teams, []string{"alpha", "beta"}) || in.Request != dm.Text {
				t.Fatalf("unexpected routing input %+v", in)
			}
			return agentOutput{Alias: "alpha"}, nil
		}
		if in.Result != "approved group material" && in.Result != "private material" {
			t.Fatalf("unexpected summary material %+v", in)
		}
		return agentOutput{Summary: "Summary of " + in.Result}, nil
	}
	if err := b.processAgentJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	inbox, err = b.Store.Peek(ctx, "alpha", "leader", "")
	if err != nil || len(inbox) != 2 {
		t.Fatalf("expected exactly two requests: %+v %v", inbox, err)
	}
	submit := model.ChatResultInput{AgentID: leader.AgentID, RunID: leader.RunID, Material: "approved group material", Final: true}
	submit.Assets = []model.ChatAsset{
		{Kind: model.ChatAssetPreview, Name: "chart.png", Data: []byte("inline chart")},
		{Kind: model.ChatAssetImage, Name: "screenshot.png", Data: []byte("separate screenshot")},
		{Kind: model.ChatAssetFile, Name: "report.pdf", Data: []byte("downloadable report")},
		{Kind: model.ChatAssetLink, Name: "Cloud report", URL: "https://example.com/wiki/report"},
	}
	b.Upload = func(_ context.Context, asset model.ChatAsset) (string, error) {
		if len(asset.Data) == 0 || asset.Kind == model.ChatAssetLink {
			return "", errors.New("Invalid upload")
		}
		return "key-" + asset.Name, nil
	}
	groupID, privateID := digest(b.accountKey(), group.ID), digest(b.accountKey(), dm.ID)
	if err := b.Store.SubmitChatResult(ctx, "alpha", groupID, submit); store.KindOf(err) != store.KindInvalid {
		t.Fatalf("unapproved group material: %v", err)
	}
	submit.GroupApproved = true
	submit.RunID = "old-run"
	if err := b.Store.SubmitChatResult(ctx, "alpha", groupID, submit); store.KindOf(err) != store.KindConflict {
		t.Fatalf("stale leader run accepted: %v", err)
	}
	submit.RunID = leader.RunID
	submit.Assets[3].URL = "javascript:alert(1)"
	if err := b.Store.SubmitChatResult(ctx, "alpha", groupID, submit); store.KindOf(err) != store.KindInvalid {
		t.Fatalf("accepted a non-HTTPS document URL: %v", err)
	}
	submit.Assets[3].URL = "https://example.com/wiki/report"
	for range 2 {
		if err := b.Store.SubmitChatResult(ctx, "alpha", groupID, submit); err != nil {
			t.Fatal(err)
		}
	}
	submit.Material = "private material"
	submit.Assets = nil
	if err := b.Store.SubmitChatResult(ctx, "alpha", privateID, submit); err != nil {
		t.Fatal(err)
	}
	if err := b.processAgentJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.Send(ctx, "alpha", store.SendInput{From: "leader", To: "operator", Content: "unsolicited personal material"}); err != nil {
		t.Fatal(err)
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	assertDelivery := func(text, reply, recipient string) {
		t.Helper()
		count := 0
		for _, out := range *outbound {
			if strings.Contains(out.Text, text) && out.ReplyTo == reply && out.OpenID == recipient {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("expected one %q delivery to %q/%q: %+v", text, reply, recipient, *outbound)
		}
	}
	assertDelivery("Summary of approved group material", group.ID, "")
	assertDelivery("Summary of private material", dm.ID, "")
	assertDelivery("已转给 alpha 团队的 Leader。", group.ID, "")
	deliveredAssets := map[string]string{}
	for _, out := range *outbound {
		if strings.Contains(out.Text, groupID) || strings.Contains(out.Text, privateID) {
			t.Fatalf("Outgoing message included an internal request ID: %+v", out)
		}
		for _, asset := range out.Assets {
			if out.ReplyTo != group.ID || !out.Thread || out.OpenID != "" || len(asset.Data) != 0 {
				t.Fatalf("wrong asset destination or delivery payload: %+v", out)
			}
			if _, duplicate := deliveredAssets[asset.Name]; duplicate {
				t.Fatalf("duplicate asset: %s", asset.Name)
			}
			if (out.CardTitle != "") != (asset.Kind == model.ChatAssetPreview || asset.Kind == model.ChatAssetLink) {
				t.Fatalf("wrong message type for asset: %+v", out)
			}
			deliveredAssets[asset.Name] = asset.Key + asset.URL
		}
	}
	for name, want := range map[string]string{
		"chart.png": "key-chart.png", "screenshot.png": "key-screenshot.png", "report.pdf": "key-report.pdf", "Cloud report": "https://example.com/wiki/report",
	} {
		if deliveredAssets[name] != want {
			t.Errorf("asset %s delivery = %q, want %q", name, deliveredAssets[name], want)
		}
	}
	assertDelivery("发来一条汇报", "", "ou_example")
	if len(*outbound) != 8 {
		t.Fatalf("unexpected deliveries: %+v", *outbound)
	}
	operator, err := b.Store.Peek(ctx, "alpha", "operator", "")
	if err != nil || len(operator) != 1 {
		t.Fatalf("notifications consumed the team inbox: %+v %v", operator, err)
	}
}

func TestBridgeClarifiesThenBlocksRevokedReply(t *testing.T) {
	ctx := t.Context()
	b, outbound, leader := chatFixture(t)
	now := time.Now()
	b.Store.Now = func() time.Time { return now }
	r := Inbound{ID: "ambiguous", Sender: "sender", Conversation: "room", Text: "How are things?", Group: true, Mentioned: true}
	if err := b.Accept(ctx, r); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	for range 2 {
		if err := b.sync(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	if len(*outbound) != 1 || !strings.Contains((*outbound)[0].Text, "确定目标团队") {
		t.Fatalf("expected a single waiting message: %+v", *outbound)
	}
	b.Agent = func(_ context.Context, mode string, in agentInput) (agentOutput, error) {
		if mode == "summary" {
			return agentOutput{Summary: in.Result}, nil
		}
		return agentOutput{}, nil
	}
	if err := b.processAgentJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(*outbound) != 2 || !strings.Contains((*outbound)[1].Text, "哪个团队") {
		t.Fatalf("expected a clarification: %+v", *outbound)
	}
	followup := Inbound{ID: "quoted-reply", Sender: "other", Conversation: "room", Group: true, Parent: "om_" + r.ID, Text: "alpha"}
	if err := b.Accept(ctx, followup); err != nil {
		t.Fatal(err)
	}
	inbox, err := b.Store.Peek(ctx, "alpha", "leader", "")
	if err != nil || len(inbox) != 0 {
		t.Fatalf("another user resolved the request: %+v %v", inbox, err)
	}
	followup.Sender, followup.ID = "sender", "authorized-quote"
	for range 2 {
		if err := b.Accept(ctx, followup); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	inbox, err = b.Store.Peek(ctx, "alpha", "leader", "")
	if err != nil || len(inbox) != 1 || !strings.Contains(inbox[0].Content, r.Text) {
		t.Fatalf("original question was not delivered: %+v %v", inbox, err)
	}
	in := model.ChatResultInput{AgentID: leader.AgentID, RunID: leader.RunID, Material: "approved update", GroupApproved: true}
	if err := b.Store.SubmitChatResult(ctx, "alpha", inbox[0].RequestID, in); err != nil {
		t.Fatal(err)
	}
	if err := b.processAgentJobs(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := b.Store.ChatSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Bindings[0].Senders = nil
	if _, err := b.Store.SetChatSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	blocked, err := b.Store.BlockedChatRequests(ctx)
	if err != nil || !slices.ContainsFunc(blocked, func(item model.ChatBlockedItem) bool { return strings.Contains(item.Content, "approved update") }) {
		t.Fatalf("blocked reply audit missing: %+v %v", blocked, err)
	}
	if len(*outbound) != 3 {
		t.Fatalf("a revoked reply was sent: %+v", *outbound)
	}
}

func TestBridgeExpiresUndeliveredRequestAfterRestart(t *testing.T) {
	ctx := t.Context()
	b, outbound, _ := chatFixture(t)
	now := time.Now()
	b.Store.Now = func() time.Time { return now }
	r := Inbound{ID: "unfinished", Sender: "sender", Conversation: "private-room", Text: "route this later"}
	if err := b.Accept(ctx, r); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	restarted := &Bridge{Store: b.Store, Account: b.Account, Log: b.Log, Deliver: b.Deliver}
	if err := restarted.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := restarted.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(*outbound) != 1 || !strings.Contains((*outbound)[0].Text, "请求已过期") {
		t.Fatalf("expected exactly one expiry notice: %+v", *outbound)
	}
	request, err := b.Store.ChatRequest(ctx, digest(b.accountKey(), r.ID))
	if err != nil || request.State != "expired" {
		t.Fatalf("request was not expired: %+v %v", request, err)
	}
}

func TestFeishuGlobalSenderCoversFutureTeamsAndCanBeCleared(t *testing.T) {
	ctx := t.Context()
	b, outbound, _ := chatFixture(t)
	settings, err := b.Store.ChatSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sender := &model.ChatRecipient{ChatAccount: b.Account, OpenID: "ou_owner"}
	settings.FeishuSender, settings.UpdateFeishuSender = sender, true
	settings, err = b.Store.SetChatSettings(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Recipient == nil || settings.Recipient.OpenID != "ou_example" {
		t.Fatal("Enabling global inbound access removed private notifications")
	}
	if _, err := b.Store.CreateTeam(ctx, store.CreateTeamInput{Name: "new-team", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	b.Agent = func(_ context.Context, kind string, in agentInput) (agentOutput, error) {
		if kind != "route" || !slices.Contains(in.Teams, "new-team") || !slices.Contains(in.Teams, "restricted") {
			t.Fatalf("Global routing candidates: %+v", in)
		}
		return agentOutput{Alias: "new-team"}, nil
	}
	for _, inbound := range []Inbound{
		{ID: "by-name", Sender: sender.OpenID, Conversation: "private", Text: "/tw send alpha status"},
		{ID: "new-natural", Sender: sender.OpenID, Conversation: "group", Group: true, Mentioned: true, Text: "Ask the new team"},
		{ID: "legacy-sender", Sender: "sender", Conversation: "private", Text: "/tw send alpha denied"},
	} {
		if err := b.Accept(ctx, inbound); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.processAgentJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, team := range []string{"alpha", "new-team"} {
		inbox, err := b.Store.Peek(ctx, team, "leader", "")
		if err != nil || len(inbox) != 1 {
			t.Fatalf("%s inbox: %+v %v", team, inbox, err)
		}
	}
	if len(*outbound) != 3 {
		t.Fatalf("Expected two receipts and a legacy-sender rejection: %+v", *outbound)
	}
	pending := Inbound{ID: "pending", Sender: sender.OpenID, Conversation: "private", Text: "Ask something later"}
	if err := b.Accept(ctx, pending); err != nil {
		t.Fatal(err)
	}
	settings.FeishuSender, settings.UpdateFeishuSender = nil, true
	if _, err := b.Store.SetChatSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	request, err := b.Store.ChatRequest(ctx, digest(b.accountKey(), pending.ID))
	if err != nil || request.State != "blocked" {
		t.Fatalf("Request should be blocked after clearing access: %+v %v", request, err)
	}
	if err := b.Accept(ctx, Inbound{ID: "cleared", Sender: sender.OpenID, Conversation: "private", Text: "/tw send new-team still denied"}); err != nil {
		t.Fatal(err)
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	inbox, err := b.Store.Peek(ctx, "new-team", "leader", "")
	if err != nil || len(inbox) != 1 || len(*outbound) != 4 {
		t.Fatalf("Disabled Feishu access produced unexpected delivery: %+v %+v %v", inbox, *outbound, err)
	}
}
