package store

import (
	"context"
	"sync"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestMessageCursorAndIdempotentLeaderDelivery(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	newTeam(t, s)
	leader, err := s.Leader(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	tail, err := s.MessageLogAfter(ctx, "demo", "latest", 0, true)
	if err != nil || len(tail.Items) != 0 {
		t.Fatalf("tail = %+v, %v", tail, err)
	}
	input := SendInput{From: "chat-bridge", To: leader.InboxName(), Content: "Run regression", ExpectedLeader: leader.AgentID, IdempotencyKey: "event-key"}
	var wg sync.WaitGroup
	ids := make(chan string, 2)
	for range 2 {
		wg.Go(func() {
			msg, err := s.Send(ctx, "demo", input)
			if err != nil {
				t.Error(err)
			}
			ids <- msg.RequestID
		})
	}
	wg.Wait()
	first, second := <-ids, <-ids
	if first == "" || first != second {
		t.Fatalf("repeat delivered different messages: %q, %q", first, second)
	}
	modified := input
	modified.Content = "Another request"
	if _, err := s.Send(ctx, "demo", modified); KindOf(err) != KindConflict {
		t.Fatalf("want idempotency conflict, got %v", err)
	}
	modified.IdempotencyKey, modified.ExpectedLeader = "other-event", "former-leader"
	if _, err := s.Send(ctx, "demo", modified); KindOf(err) != KindConflict {
		t.Fatalf("want stale leader conflict, got %v", err)
	}
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Regress"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted)}); err != nil {
		t.Fatal(err)
	}
	page, err := s.MessageLogAfter(ctx, "demo", tail.NextCursor, 1, true)
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.Items[0].Message == nil || page.Items[0].Message.RequestID != first {
		t.Fatalf("message page = %+v, %v", page, err)
	}
	events, err := s.MessageLogAfter(ctx, "demo", page.NextCursor, 1, true)
	if err != nil || len(events.Items) != 1 || events.HasMore || events.Items[0].Event["type"] != "TaskCompleted" {
		t.Fatalf("event page = %+v, %v", events, err)
	}
	history, err := s.MessageLog(ctx, "demo", 10, "")
	if err != nil || len(history) != 1 || history[0].RequestID != first {
		t.Fatalf("legacy history = %+v, %v", history, err)
	}
	inbox, err := s.Peek(ctx, "demo", leader.Name, leader.User)
	if err != nil || len(inbox) != 1 || inbox[0].RequestID != first {
		t.Fatalf("inbox = %+v, %v", inbox, err)
	}
}

func TestChatBindingRevisionAndConversationChanges(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	newTeam(t, s)
	account := model.ChatAccount{Provider: "example-chat", Tenant: "example:org", Application: "test-app"}
	settings := model.ChatSettings{Bindings: []model.ChatBinding{{
		ChatAccount: account, Team: "demo", Conversation: "!demo-room:example.org", Alias: "demo", Senders: []string{"@sender:example.org"}, Receive: true,
	}}}
	saved, err := s.SetChatSettings(ctx, settings)
	if err != nil || saved.Revision != 1 || saved.Bindings[0].BindingID == "" {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	if _, err := s.SetChatSettings(ctx, settings); KindOf(err) != KindConflict {
		t.Fatalf("stale revision was accepted: %v", err)
	}
	stored, err := s.ChatSettings(ctx)
	if err != nil || stored.Revision != 1 || stored.Bindings[0].Conversation != "!demo-room:example.org" {
		t.Fatalf("read = %+v, %v", stored, err)
	}
	firstID := stored.Bindings[0].BindingID
	stored.Bindings[0].Notify = true
	stored, err = s.SetChatSettings(ctx, stored)
	if err != nil || stored.Bindings[0].BindingID != firstID {
		t.Fatalf("same conversation lost its binding: %+v, %v", stored, err)
	}
	stored.Bindings[0].Conversation = "another-room"
	stored, err = s.SetChatSettings(ctx, stored)
	if err != nil || stored.Bindings[0].BindingID == firstID {
		t.Fatalf("new conversation inherited its binding: %+v, %v", stored, err)
	}
}

func TestRestoringSnapshotResetsChatBindingAtRestoredLogTail(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	newTeam(t, s)
	send := func(content string) model.Message {
		t.Helper()
		msg, err := s.Send(ctx, "demo", SendInput{From: "leader", To: "operator", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	send("Saved in snapshot")
	snapshot, err := s.CreateSnapshot(ctx, "demo", "before-test")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.SetChatSettings(ctx, model.ChatSettings{Bindings: []model.ChatBinding{{
		ChatAccount: model.ChatAccount{Provider: "feishu", Tenant: "test", Application: "bot"},
		Team:        "demo", Conversation: "test-room", Alias: "demo", Notify: true, Receive: true, Senders: []string{"authorized-sender"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	send("Queued before restore")
	if _, err := s.RestoreSnapshot(ctx, "demo", snapshot.ID, true); err != nil {
		t.Fatal(err)
	}
	dryRun, err := s.ChatSettings(ctx)
	if err != nil || dryRun.Revision != initial.Revision {
		t.Fatalf("dry run changed chat configuration: %+v, %v", dryRun, err)
	}
	if _, err := s.RestoreSnapshot(ctx, "demo", snapshot.ID, false); err != nil {
		t.Fatal(err)
	}
	newMessage := send("Arrived after restore, before the bridge polled")
	settings, err := s.ChatSettings(ctx)
	if err != nil || settings.Revision != initial.Revision+1 || len(settings.Bindings) != 1 ||
		settings.Bindings[0].BindingID == initial.Bindings[0].BindingID || !settings.Bindings[0].Notify {
		t.Fatalf("restore did not renew the existing chat binding: %+v, %v", settings, err)
	}
	page, err := s.MessageLogAfter(ctx, "demo", settings.Bindings[0].StartCursor, 10, true)
	if err != nil || len(page.Items) != 1 || page.Items[0].Message == nil || page.Items[0].Message.RequestID != newMessage.RequestID {
		t.Fatalf("post-restore page = %+v, %v", page, err)
	}
	leader, err := s.Leader(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	input := SendInput{From: "feishu-bridge", To: leader.InboxName(), Content: "Send after restore", ExpectedLeader: leader.AgentID,
		ChatBindingID: initial.Bindings[0].BindingID, ChatSenderID: "authorized-sender"}
	if _, err := s.Send(ctx, "demo", input); KindOf(err) != KindConflict {
		t.Fatalf("restore did not invalidate waiting sends: %v", err)
	}
	input.ChatBindingID = settings.Bindings[0].BindingID
	if _, err := s.Send(ctx, "demo", input); err != nil {
		t.Fatalf("new binding did not allow authorized sender: %v", err)
	}
	input.ChatSenderID = "revoked-sender"
	if _, err := s.Send(ctx, "demo", input); KindOf(err) != KindConflict {
		t.Fatalf("unauthorized sender was allowed: %v", err)
	}
	if deleted, err := s.DeleteTeam(ctx, "demo"); err != nil || !deleted {
		t.Fatalf("team cleanup = %v, %v", deleted, err)
	}
	if _, err := s.RestoreSnapshot(ctx, "demo", snapshot.ID, false); err != nil {
		t.Fatal(err)
	}
	settings, err = s.ChatSettings(ctx)
	if err != nil || len(settings.Bindings) != 0 {
		t.Fatalf("a recreated team inherited its old chat binding: %+v, %v", settings, err)
	}
}
