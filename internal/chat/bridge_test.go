package chat

import (
	"context"
	"encoding/json"
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
			return agentOutput{assistantAction: assistantAction{Alias: "alpha"}}, nil
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

func TestBridgeAnswersTeamCountInOriginalConversation(t *testing.T) {
	ctx := t.Context()
	b, outbound, _ := chatFixture(t)
	const question = "看下现在有几个team"
	b.RunCLI = func(ctx context.Context, args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"team", "discover"}) {
			t.Fatalf("Unexpected CLI arguments: %q", args)
		}
		teams, err := b.Store.ListTeams(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(teams)
	}
	b.Agent = func(_ context.Context, kind string, in agentInput) (agentOutput, error) {
		if kind != "route" || in.Request != question || !slices.Equal(in.Teams, []string{"alpha", "beta"}) {
			t.Fatalf("Unexpected router input: %s %+v", kind, in)
		}
		var out agentOutput
		err := json.Unmarshal([]byte(`{"action":"list_teams","alias":""}`), &out)
		return out, err
	}
	msg := Inbound{ID: "list", Sender: "sender", Conversation: "group", Text: question, Group: true, Thread: true, Mentioned: true}
	for range 2 {
		if err := b.Accept(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.processAgentJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(*outbound) != 1 {
		t.Fatalf("Expected one answer: %+v", *outbound)
	}
	out := (*outbound)[0]
	if out.ReplyTo != msg.ID || !out.Thread || !strings.Contains(out.Text, "2 个团队") ||
		!strings.Contains(out.Text, "alpha") || !strings.Contains(out.Text, "beta") || strings.Contains(out.Text, "restricted") {
		t.Fatalf("Expected the count and visible teams in the original conversation, got %+v", out)
	}
	inbox, err := b.Store.Peek(ctx, "alpha", "leader", "")
	if err != nil || len(inbox) != 0 {
		t.Fatalf("Team lookup reached a Leader inbox: %+v %v", inbox, err)
	}
}

func TestAssistantSessionScope(t *testing.T) {
	first := model.ChatRequest{Account: "application", Origin: model.ChatOrigin{
		MessageID: "first-message", Sender: "sender", Conversation: "chat", Thread: true, ThreadID: "topic"}}
	second := first
	second.Origin.MessageID = "followup"
	want := assistantSessionKey(first, 1)
	if assistantSessionKey(second, 1) != want {
		t.Fatal("A follow-up in the same topic must reuse the session")
	}
	changes := []func(*model.ChatRequest){
		func(r *model.ChatRequest) { r.Account = "other-application" },
		func(r *model.ChatRequest) { r.Origin.Sender = "other-sender" },
		func(r *model.ChatRequest) { r.Origin.Conversation = "other-chat" },
		func(r *model.ChatRequest) { r.Origin.ThreadID = "other-topic" },
	}
	for i, change := range changes {
		request := second
		change(&request)
		if assistantSessionKey(request, 1) == want {
			t.Fatalf("Session was shared across scope change %d", i)
		}
	}
	if assistantSessionKey(second, 2) == want {
		t.Fatal("Updated chat permissions must begin a new session")
	}
}

func TestAssistantWritesRequireOriginalSenderConfirmation(t *testing.T) {
	for _, test := range []struct {
		name   string
		action assistantAction
		args   []string
		result string
	}{
		{"create team", assistantAction{Action: "create_team", Team: "new team"},
			[]string{"team", "spawn-team", "--agent-name=leader", "--", "new-team"}, `{"team":"new-team","status":"created"}`},
		{"assign task", assistantAction{Action: "create_task", Alias: "alpha", Subject: "--literal title", Owner: "leader"},
			[]string{"task", "create", "--owner=leader", "--description=", "--", "alpha", "--literal title"},
			`{"id":"task-1","owner":"leader","subject":"--literal title"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			b, outbound, _ := chatFixture(t)
			settings, err := b.Store.ChatSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			settings.FeishuSender = &model.ChatRecipient{ChatAccount: b.Account, OpenID: "ou_owner"}
			settings.UpdateFeishuSender = true
			if _, err := b.Store.SetChatSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			b.Agent = func(context.Context, string, agentInput) (agentOutput, error) {
				return agentOutput{assistantAction: test.action}, nil
			}
			calls := 0
			b.RunCLI = func(_ context.Context, args []string) ([]byte, error) {
				calls++
				if !slices.Equal(args, test.args) {
					t.Fatalf("CLI got %q, want %q", args, test.args)
				}
				return []byte(test.result), nil
			}
			accept := func(id, sender, conversation, text string) {
				t.Helper()
				if err := b.Accept(ctx, Inbound{ID: id, Sender: sender, Conversation: conversation, Text: text}); err != nil {
					t.Fatal(err)
				}
			}
			sync := func() {
				t.Helper()
				if err := b.sync(ctx, true); err != nil {
					t.Fatal(err)
				}
			}
			accept("request", "ou_owner", "original-chat", test.name)
			if err := b.processAgentJobs(ctx); err != nil {
				t.Fatal(err)
			}
			sync()
			if calls != 0 || len(*outbound) != 1 || !strings.Contains((*outbound)[0].Text, string(jobJSON(test.args))) {
				t.Fatalf("Expected only the exact operation preview before confirmation, got %d calls and %+v", calls, *outbound)
			}
			_, suffix, ok := strings.Cut((*outbound)[0].Text, "/tw confirm ")
			if !ok || suffix == "" {
				t.Fatalf("Preview missing confirmation command: %+v", *outbound)
			}
			confirmation := "/tw confirm " + suffix
			accept("wrong-room", "ou_owner", "other-chat", confirmation)
			accept("wrong-sender", "ou_other", "original-chat", confirmation)
			sync()
			if calls != 0 {
				t.Fatal("Another sender or conversation confirmed the operation")
			}
			accept("confirmed", "ou_owner", "original-chat", confirmation)
			accept("confirmed", "ou_owner", "original-chat", confirmation)
			sync()
			accept("token-reused", "ou_owner", "original-chat", confirmation)
			sync()
			if calls != 1 {
				t.Fatalf("Confirmed operation executed %d times", calls)
			}
			if !slices.ContainsFunc(*outbound, func(out Outbound) bool {
				return out.ReplyTo == "request" && strings.Contains(out.Text, "已") && !strings.Contains(out.Text, "确认码")
			}) {
				t.Fatalf("Operation result did not return to its original message: %+v", *outbound)
			}
		})
	}
}

func TestAssistantAnswersTeamStatusFromCurrentBoard(t *testing.T) {
	ctx := t.Context()
	b, outbound, _ := chatFixture(t)
	const question = "alpha现在的状态"
	b.Agent = func(_ context.Context, kind string, in agentInput) (agentOutput, error) {
		if kind != "route" || in.Request != question {
			t.Fatalf("Unexpected router input: %s %+v", kind, in)
		}
		return agentOutput{assistantAction: assistantAction{Action: "team_status", Alias: "alpha"}}, nil
	}
	live := true
	b.RunCLI = func(_ context.Context, args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"board", "show", "--messages=1", "--", "alpha"}) {
			t.Fatalf("Unexpected CLI arguments: %q", args)
		}
		return json.Marshal(model.Board{
			Team:    model.Team{Name: "alpha"},
			Members: []model.BoardMember{{Agent: &model.Agent{Alive: &live}}, {}},
			Stats:   model.TaskStats{InProgress: 2, Waiting: 1, Pending: 1, Blocked: 1, Completed: 2},
			Tasks: []model.Task{
				{Subject: "Earlier completed task", Status: model.StatusCompleted, UpdatedAt: "2026-01-01T09:00:00Z"},
				{Subject: "Validate the current fix", Status: model.StatusInProgress, Owner: "tester",
					TaskTracking: model.TaskTracking{Progress: []model.TaskProgress{{Summary: "Two cases passed"}}}},
				{Subject: "Collect approval", Status: model.StatusInProgress, ExecutionState: "waiting",
					TaskTracking: model.TaskTracking{Progress: []model.TaskProgress{{Reason: "Waiting for review"}}}},
				{Subject: "Canary deployment", Status: model.StatusBlocked, BlockedBy: []string{"task-1"}},
				{Subject: "Most recent completion", Status: model.StatusCompleted, UpdatedAt: "2026-01-02T09:00:00Z"},
			},
			Messages: []model.Message{{Content: "Private inbox content"}},
		})
	}
	msg := Inbound{ID: "status", Sender: "sender", Conversation: "private", Text: question}
	if err := b.Accept(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if err := b.processAgentJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(*outbound) != 1 || (*outbound)[0].ReplyTo != msg.ID {
		t.Fatalf("Expected one reply to the original message, got %+v", *outbound)
	}
	text := (*outbound)[0].Text
	for _, want := range []string{"alpha 当前状态", "1/2", "进行中 1", "Validate the current fix", "Two cases passed",
		"Collect approval", "Waiting for review", "Canary deployment", "Most recent completion"} {
		if !strings.Contains(text, want) {
			t.Errorf("Team status is missing %q: %s", want, text)
		}
	}
	for _, unwanted := range []string{"Earlier completed task", "Private inbox content", "请回复团队别名"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("Team status included %q: %s", unwanted, text)
		}
	}
}

func TestAssistantAnswersOpenQuestionsAndKeepsCompoundRequestsTogether(t *testing.T) {
	for _, test := range []struct {
		request string
		action  assistantAction
		want    string
	}{
		{"alpha 最后交付的东西有验过吗？", assistantAction{Action: "answer", Alias: "alpha"}, "最后一项跑了回归测试。"},
		{"帮我建一个叫 test team 的队，Agent 都用 Codex", assistantAction{Action: "create_team", Team: "test team", Runtime: "codex"},
			"团队名可以用 test-team。建队和启动 Codex 成员是两步。"},
	} {
		t.Run(test.request, func(t *testing.T) {
			ctx := t.Context()
			b, outbound, _ := chatFixture(t)
			b.Agent = func(_ context.Context, kind string, input agentInput) (agentOutput, error) {
				if input.Request != test.request || !slices.Equal(input.Teams, []string{"alpha", "beta"}) {
					t.Fatalf("Wrong scope: %+v", input)
				}
				switch kind {
				case "route":
					return agentOutput{assistantAction: test.action}, nil
				case "answer":
					if test.action.Alias != "" && (!strings.Contains(input.Context, "Passed regression tests") ||
						strings.Contains(input.Context, "Private inbox content")) {
						t.Fatalf("Wrong facts for the answer: %s", input.Context)
					}
					return agentOutput{Summary: test.want}, nil
				default:
					t.Fatalf("Unexpected mode %q", kind)
					return agentOutput{}, nil
				}
			}
			b.RunCLI = func(_ context.Context, args []string) ([]byte, error) {
				if test.action.Alias == "" || !slices.Equal(args, []string{"board", "show", "--messages=1", "--", "alpha"}) {
					t.Fatalf("Unexpected command: %q", args)
				}
				return json.Marshal(model.Board{
					Team: model.Team{Name: "alpha"},
					Tasks: []model.Task{{Subject: "Deliver fix", Status: model.StatusCompleted,
						Result: &model.TaskResult{Verification: "Passed regression tests"}}},
					Messages: []model.Message{{Content: "Private inbox content"}},
				})
			}
			msg := Inbound{ID: "question", Sender: "sender", Conversation: "private", Text: test.request}
			if err := b.Accept(ctx, msg); err != nil {
				t.Fatal(err)
			}
			if err := b.processAgentJobs(ctx); err != nil {
				t.Fatal(err)
			}
			if err := b.sync(ctx, true); err != nil {
				t.Fatal(err)
			}
			if len(*outbound) != 1 || (*outbound)[0].ReplyTo != msg.ID || (*outbound)[0].Text != test.want {
				t.Fatalf("Expected a direct answer, got %+v", *outbound)
			}
		})
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
		return agentOutput{assistantAction: assistantAction{Action: "route"}}, nil
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
		return agentOutput{assistantAction: assistantAction{Alias: "new-team"}}, nil
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
