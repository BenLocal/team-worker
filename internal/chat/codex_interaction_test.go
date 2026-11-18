package chat

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestCodexCardRequiresOriginalUserAndResumesOnce(t *testing.T) {
	b, _, _ := chatFixture(t)
	ctx := t.Context()
	origin := model.ChatOrigin{MessageID: "om_request", Sender: "ou_owner", Conversation: "oc_original", Thread: true}
	settings, err := b.Store.ChatSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.FeishuSender = &model.ChatRecipient{ChatAccount: b.Account, OpenID: origin.Sender}
	settings.UpdateFeishuSender = true
	settings, err = b.Store.SetChatSettings(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		delivered := make(chan Outbound, 1)
		b.Deliver = func(_ context.Context, out Outbound) (string, error) {
			delivered <- out
			return "om_interaction", nil
		}
		type resumed struct {
			result any
			err    error
		}
		resume := make(chan resumed, 1)
		go func() {
			res, err := b.waitForCodexCard(t.Context(), origin, settings.Revision, codexInteraction{
				title: "Codex 请求补充信息", wait: time.Minute,
				questions: []model.ChatCodexQuestion{
					{ID: "topic", Options: []model.ChatCodexOption{{Label: "Tests"}}, Other: true},
					{ID: "scope"},
				},
			})
			resume <- resumed{res, err}
		}()
		out := <-delivered
		synctest.Wait()
		if out.ReplyTo != origin.MessageID || !out.Thread || out.CodexCard == nil {
			t.Fatalf("Card did not go to the original topic: %+v", out)
		}
		assertClick := func(sender, message, want string, fields map[string]any) {
			t.Helper()
			event := &callback.CardActionTriggerEvent{
				EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: b.Account.Application, EventID: "click"}},
				Event: &callback.CardActionTriggerRequest{
					Host: "im_message", Operator: &callback.Operator{OpenID: sender},
					Context: &callback.Context{OpenMessageID: message, OpenChatID: origin.Conversation},
					Action: &callback.CallBackAction{Tag: "button", Value: map[string]any{
						feishuCodexInteractionKey: out.CodexCard.Token, "choice": "submit",
					}, FormValue: fields},
				},
			}
			resp, err := b.confirmFeishuCard(t.Context(), event)
			if err != nil || resp == nil || resp.Toast == nil || resp.Toast.Type != want {
				t.Fatalf("Unexpected card result: %+v, %v", resp, err)
			}
		}
		fields := map[string]any{"answer_0": "other", "answer_0_other": "Docs", "answer_1": "Only setup"}
		assertClick("ou_other", "om_interaction", "error", fields)
		assertClick(origin.Sender, "om_different", "error", fields)
		assertClick(origin.Sender, "om_interaction", "error", map[string]any{"answer_0": "999"})
		assertClick(origin.Sender, "om_interaction", "success", fields)
		assertClick(origin.Sender, "om_interaction", "error", fields)
		result := <-resume
		if got := string(jobJSON(result.result)); result.err != nil || got != `{"answers":{"scope":{"answers":["Only setup"]},"topic":{"answers":["Docs"]}}}` {
			t.Fatalf("Unexpected resumed result %s, %v", got, result.err)
		}
	})
}

func TestCodexApprovalOnlyAllowsOneTimeDecisions(t *testing.T) {
	i, err := parseCodexInteraction(codexMessage{Method: "item/commandExecution/requestApproval",
		Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","command":"pwd","availableDecisions":["acceptForSession","cancel"]}`)}, "thread", "turn", nil)
	if err != nil || i.approve {
		t.Fatalf("Expected a denial-only approval, got %+v, %v", i, err)
	}
	if _, ok := i.response("accept", nil); ok {
		t.Fatal("An unavailable one-time approval was accepted")
	}
	if result, ok := i.response("deny", nil); !ok || string(jobJSON(result)) != `{"decision":"cancel"}` {
		t.Fatalf("Unexpected decline result: %+v", result)
	}
}
