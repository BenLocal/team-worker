package chat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestFeishuUploadsAssetsAndSendsQuotedReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
		case "/open-apis/im/v1/images", "/open-apis/im/v1/files":
			reader, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			fields := map[string]string{}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Error(err)
					return
				}
				data, err := io.ReadAll(part)
				if err != nil {
					t.Error(err)
					return
				}
				fields[part.FormName()] = string(data)
				if part.FileName() != "" {
					fields["multipart_filename"] = part.FileName()
				}
			}
			if r.URL.Path == "/open-apis/im/v1/images" {
				if !reflect.DeepEqual(fields, map[string]string{"image_type": "message", "image": "chart bytes", "multipart_filename": "chart.png"}) {
					t.Errorf("image form: %v", fields)
				}
				fmt.Fprint(w, `{"code":0,"data":{"image_key":"img_example"}}`)
			} else {
				name := fields["file_name"]
				if !slices.Contains([]string{"report.pdf", "report.docx", "report.xlsx"}, name) ||
					!reflect.DeepEqual(fields, map[string]string{"file_type": "stream", "file": "file bytes", "file_name": name, "multipart_filename": name}) {
					t.Errorf("file form: %v", fields)
				}
				fmt.Fprint(w, `{"code":0,"data":{"file_key":"file_example"}}`)
			}
		case "/open-apis/im/v1/messages/om_original/reply":
			var body struct {
				Thread  bool   `json:"reply_in_thread"`
				Type    string `json:"msg_type"`
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Thread {
				t.Errorf("reply lost its original thread: %+v, %v", body, err)
			}
			switch body.Type {
			case "interactive":
				var card struct {
					Body struct {
						Elements []struct{ Content string }
					}
				}
				if err := json.Unmarshal([]byte(body.Content), &card); err != nil {
					t.Error(err)
				}
				var got []string
				for _, element := range card.Body.Elements {
					got = append(got, element.Content)
				}
				want := []string{"Summary", "![chart.png](img_example)", `[Cloud \[report\]](https://example.com/wiki/report)`}
				if !slices.Equal(got, want) {
					t.Errorf("card contents = %q, want %q", got, want)
				}
			case "image":
				if body.Content != `{"image_key":"img_example"}` {
					t.Errorf("image reply: %s", body.Content)
				}
			case "file":
				if body.Content != `{"file_key":"file_example"}` {
					t.Errorf("file reply: %s", body.Content)
				}
			default:
				t.Errorf("unexpected reply type %q", body.Type)
			}
			fmt.Fprint(w, `{"code":0,"data":{"message_id":"om_reply"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := lark.NewClient("cli_asset_fixture", "example-only", lark.WithOpenBaseUrl(server.URL), lark.WithLogger(quietSDK{}))
	ctx := t.Context()
	bridge := Bridge{Upload: feishuUpload(client)}
	deliver := feishuDelivery(client)
	send := func(out Outbound) {
		t.Helper()
		out.ReplyTo, out.Thread = "om_original", true
		if err := bridge.prepareReplyAssets(ctx, &out); err != nil {
			t.Fatal(err)
		}
		if id, err := deliver(ctx, out); id != "om_reply" || err != nil {
			t.Fatalf("reply: %q %v", id, err)
		}
	}
	send(Outbound{Text: "Summary", CardTitle: "Final result", Assets: []model.ChatAsset{
		{Kind: model.ChatAssetPreview, Name: "chart.png", Data: []byte("chart bytes")},
		{Kind: model.ChatAssetLink, Name: "Cloud [report]", URL: "https://example.com/wiki/report"},
	}})
	send(Outbound{Assets: []model.ChatAsset{{Kind: model.ChatAssetImage, Name: "chart.png", Data: []byte("chart bytes")}}})
	for _, name := range []string{"report.pdf", "report.docx", "report.xlsx"} {
		send(Outbound{Assets: []model.ChatAsset{{Kind: model.ChatAssetFile, Name: name, Data: []byte("file bytes")}}})
	}
}

func TestFeishuSeparatesPrivateNoticesAndQuotedReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
		case "/open-apis/im/v1/messages":
			var body struct {
				ReceiveID string `json:"receive_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if r.URL.Query().Get("receive_id_type") != "open_id" || body.ReceiveID != "ou_example" {
				t.Errorf("Unexpected private delivery target %s %s", r.URL.RawQuery, body.ReceiveID)
			}
			fmt.Fprint(w, `{"code":0,"data":{"message_id":"om_notice"}}`)
		case "/open-apis/im/v1/messages/om_original/reply":
			var body struct {
				Thread  bool   `json:"reply_in_thread"`
				Type    string `json:"msg_type"`
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if !body.Thread {
				t.Error("Reply should retain the original thread")
			}
			if body.Type == "interactive" {
				var card struct {
					Schema string
					Header struct{ Title struct{ Tag, Content string } }
					Body   struct {
						Elements []struct{ Tag, Content string }
					}
				}
				if err := json.Unmarshal([]byte(body.Content), &card); err != nil || card.Schema != "2.0" ||
					card.Header.Title.Tag != "plain_text" || card.Header.Title.Content != "Final result" ||
					len(card.Body.Elements) != 1 || card.Body.Elements[0].Tag != "markdown" ||
					card.Body.Elements[0].Content != "**Status**\n- Online" {
					t.Errorf("Unexpected reply card: %s (%v)", body.Content, err)
				}
			} else if body.Type != "text" || body.Content != `{"text":"Reply"}` {
				t.Errorf("Unexpected plain reply: %s %s", body.Type, body.Content)
			}
			fmt.Fprint(w, `{"code":0,"data":{"message_id":"om_reply"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := lark.NewClient("cli_delivery_fixture", "example-only", lark.WithOpenBaseUrl(server.URL), lark.WithLogger(quietSDK{}))
	deliver := feishuDelivery(client)
	if id, err := deliver(t.Context(), Outbound{Text: "Notice", OpenID: "ou_example"}); err != nil || id != "om_notice" {
		t.Fatalf("Private notice: %s %v", id, err)
	}
	if id, err := deliver(t.Context(), Outbound{Text: "Reply", ReplyTo: "om_original", Thread: true}); err != nil || id != "om_reply" {
		t.Fatalf("Quoted reply: %s %v", id, err)
	}
	if id, err := deliver(t.Context(), Outbound{Text: "**Status**\n- Online", CardTitle: "Final result", ReplyTo: "om_original", Thread: true}); err != nil || id != "om_reply" {
		t.Fatalf("Quoted card: %s %v", id, err)
	}
}

func TestFeishuEventUsesVerifiedMentionAndApplication(t *testing.T) {
	var event larkim.P2MessageReceiveV1
	input := `{
		"schema":"2.0",
		"header":{"app_id":"cli_fixture","event_type":"im.message.receive_v1"},
		"event":{
			"sender":{"sender_type":"user","sender_id":{"open_id":"test-user"}},
			"message":{"message_id":"test-message","chat_id":"test-group","chat_type":"group","thread_id":"test-thread",
				"message_type":"text","content":"{\"text\":\"@_user_1 /tw send test report\"}",
				"mentions":[{"key":"@_user_1","id":{"open_id":"test-bot"}}]}
		}
	}`
	if err := json.Unmarshal([]byte(input), &event); err != nil {
		t.Fatal(err)
	}
	msg, ok := feishuInbound(&event, "cli_fixture", "test-bot")
	if !ok || !msg.Mentioned || !msg.Group || !msg.Thread || msg.ThreadID != "test-thread" ||
		msg.Text != " /tw send test report" || msg.Sender != "test-user" {
		t.Fatalf("normalized message = %+v, accepted = %t", msg, ok)
	}
	if _, ok := feishuInbound(&event, "cli_other", "test-bot"); ok {
		t.Fatal("event from another application accepted")
	}
	if msg, _ := feishuInbound(&event, "cli_fixture", "another-bot"); msg.Mentioned {
		t.Fatal("a mention of another bot authorized a group command")
	}
}
