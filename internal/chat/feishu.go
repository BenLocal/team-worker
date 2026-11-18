package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type quietSDK struct{}

func (quietSDK) Debug(context.Context, ...interface{}) {}
func (quietSDK) Info(context.Context, ...interface{})  {}
func (quietSDK) Warn(context.Context, ...interface{})  {}
func (quietSDK) Error(context.Context, ...interface{}) {}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func feishuInbound(event *larkim.P2MessageReceiveV1, appID, botID string) (Inbound, bool) {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil ||
		event.EventV2Base.Header.AppID != appID || event.Event == nil {
		return Inbound{}, false
	}
	sender, message := event.Event.Sender, event.Event.Message
	if sender == nil || sender.SenderId == nil || value(sender.SenderType) != "user" || message == nil || value(message.MessageType) != "text" {
		return Inbound{}, false
	}
	chatType := value(message.ChatType)
	if chatType != "group" && chatType != "p2p" {
		return Inbound{}, false
	}
	var content struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(value(message.Content)), &content) != nil {
		return Inbound{}, false
	}
	mentioned := false
	for _, mention := range message.Mentions {
		if mention == nil || mention.Id == nil || value(mention.Id.OpenId) != botID || value(mention.Key) == "" {
			continue
		}
		mentioned = true
		content.Text = strings.Replace(content.Text, value(mention.Key), "", 1)
	}
	return Inbound{ID: value(message.MessageId), Sender: value(sender.SenderId.OpenId), Conversation: value(message.ChatId),
		Text: content.Text, Parent: value(message.ParentId), Group: chatType == "group", Mentioned: mentioned,
		Thread: value(message.ThreadId) != "", ThreadID: value(message.ThreadId)}, true
}

func feishuBot(ctx context.Context, client *lark.Client) (string, error) {
	res, err := client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil || res == nil || res.StatusCode != http.StatusOK {
		return "", errors.New("Feishu bot identity request failed")
	}
	var data struct {
		Code int
		Bot  struct {
			OpenID string `json:"open_id"`
		}
	}
	if json.Unmarshal(res.RawBody, &data) != nil || data.Code != 0 || data.Bot.OpenID == "" {
		return "", errors.New("Feishu rejected the bot identity request; check the App ID, App Secret and bot capability")
	}
	return data.Bot.OpenID, nil
}

func feishuReaction(client *lark.Client) func(context.Context, string) error {
	return func(ctx context.Context, messageID string) error {
		emoji := larkim.NewEmojiBuilder().EmojiType("OK").Build()
		body := larkim.NewCreateMessageReactionReqBodyBuilder().ReactionType(emoji).Build()
		request := larkim.NewCreateMessageReactionReqBuilder().MessageId(messageID).Body(body).Build()
		resp, err := client.Im.MessageReaction.Create(ctx, request)
		if err != nil || resp == nil {
			return errors.New("Feishu reaction request failed")
		}
		if resp.StatusCode != http.StatusOK || !resp.Success() {
			return fmt.Errorf("Feishu rejected reaction (HTTP %d, code %d)", resp.StatusCode, resp.Code)
		}
		return nil
	}
}

const feishuConfirmKey = "tw_confirm"

func (b *Bridge) confirmFeishuCard(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.Event == nil {
		return nil, nil
	}
	header := event.EventV2Base.Header
	if header.AppID != b.Account.Application || header.EventID == "" {
		return nil, nil
	}
	click := event.Event
	if click.Host != "im_message" || click.Action == nil || click.Action.Tag != "button" ||
		click.Operator == nil || click.Operator.OpenID == "" || click.Context == nil ||
		click.Context.OpenChatID == "" || click.Context.OpenMessageID == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if token, ok := click.Action.Value[feishuCodexInteractionKey].(string); ok && token != "" && len(token) <= 128 {
		return b.receiveCodexCard(ctx, click, token), nil
	}
	token, ok := click.Action.Value[feishuConfirmKey].(string)
	if !ok || token == "" || len(token) > 128 {
		return nil, nil
	}
	sender, chatID, cardID := click.Operator.OpenID, click.Context.OpenChatID, click.Context.OpenMessageID
	receipt := digest(feishuConfirmKey, header.EventID, sender, chatID, cardID, token)
	incoming := model.ChatRequest{Account: b.accountKey(), Origin: model.ChatOrigin{
		MessageID: receipt, Sender: sender, Conversation: chatID,
	}}
	accepted, err := b.Store.ConfirmChatAction(ctx, b.Account, incoming, token, cardID)
	toast := &callback.Toast{Type: "error", Content: "此确认已使用、已过期，或你无权确认。"}
	if err != nil {
		b.Log.Warn("confirm Feishu card", "err", err)
		toast.Content = "暂时无法提交确认，请稍后重试。"
	} else if accepted {
		toast.Type, toast.Content = "success", "已提交确认，执行结果会回复原消息。"
	}
	return &callback.CardActionTriggerResponse{Toast: toast}, nil
}

func feishuDelivery(client *lark.Client) func(context.Context, Outbound) (string, error) {
	return func(ctx context.Context, out Outbound) (string, error) {
		replacer := strings.NewReplacer("<", "‹", ">", "›")
		text := replacer.Replace(out.Text)
		msgType := "text"
		var body any = map[string]string{"text": text}
		if out.Markdown || out.CardTitle != "" || out.ConfirmToken != "" || out.CodexCard != nil {
			msgType = "interactive"
			elements := []map[string]any{{"tag": "markdown", "content": text}}
			if out.ConfirmToken != "" {
				elements = []map[string]any{
					{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": out.Text}},
					{
						"tag": "button", "type": "primary",
						"text": map[string]string{"tag": "plain_text", "content": "确认执行"},
						"behaviors": []map[string]any{{
							"type": "callback", "value": map[string]string{feishuConfirmKey: out.ConfirmToken},
						}},
					},
				}
			}
			if out.CodexCard != nil {
				elements = feishuCodexElements(out.Text, out.CodexCard)
			}
			for _, asset := range out.Assets {
				elements = append(elements, map[string]any{"tag": "markdown", "content": feishuAssetMarkdown(asset)})
			}
			card := map[string]any{
				"schema": "2.0",
				"body":   map[string]any{"elements": elements},
			}
			if out.CardTitle != "" {
				card["header"] = map[string]any{"template": "blue", "title": map[string]string{"tag": "plain_text", "content": out.CardTitle}}
			}
			body = card
		} else if len(out.Assets) == 1 {
			asset := out.Assets[0]
			msgType = asset.Kind
			body = map[string]string{asset.Kind + "_key": asset.Key}
		}
		content, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		var status, code int
		if out.ReplyTo != "" {
			body := larkim.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(string(content)).ReplyInThread(out.Thread).Build()
			res, err := client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().MessageId(out.ReplyTo).Body(body).Build())
			if err != nil || res == nil {
				return "", errors.New("Feishu reply result is unknown")
			}
			status, code = res.StatusCode, res.Code
			if res.Success() && res.Data != nil && value(res.Data.MessageId) != "" {
				return value(res.Data.MessageId), nil
			}
		} else {
			recipientType, recipient := "chat_id", out.Conversation
			if out.OpenID != "" {
				recipientType, recipient = "open_id", out.OpenID
			}
			body := larkim.NewCreateMessageReqBodyBuilder().MsgType(msgType).Content(string(content)).ReceiveId(recipient).Build()
			res, err := client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(recipientType).Body(body).Build())
			if err != nil || res == nil {
				return "", errors.New("Feishu send result is unknown")
			}
			status, code = res.StatusCode, res.Code
			if res.Success() && res.Data != nil && value(res.Data.MessageId) != "" {
				return value(res.Data.MessageId), nil
			}
		}
		if status >= 500 || (status == http.StatusOK && code == 0) {
			return "", errors.New("Feishu delivery result is unknown")
		}
		return "", &DeliveryError{
			Retryable: status == 429 || code == 230020 || code == 99991400,
			Message:   fmt.Sprintf("Feishu rejected delivery (HTTP %d, code %d)", status, code),
		}
	}
}

func feishuCodexElements(detail string, card *model.ChatCodexCard) []map[string]any {
	plain := func(text string) map[string]string { return map[string]string{"tag": "plain_text", "content": text} }
	button := func(label, choice, style string) map[string]any {
		return map[string]any{
			"tag": "button", "text": plain(label), "type": style,
			"behaviors": []map[string]any{{"type": "callback", "value": map[string]string{
				feishuCodexInteractionKey: card.Token, "choice": choice,
			}}},
		}
	}
	if len(card.Questions) == 0 {
		elements := []map[string]any{{"tag": "div", "text": plain(detail)}}
		if card.Approve {
			elements = append(elements, button("仅批准本次", "accept", "primary"))
		}
		return append(elements, button("拒绝", "deny", "default"))
	}
	var fields []map[string]any
	for n, q := range card.Questions {
		name := codexFieldName(n)
		text := q.Question
		if q.Header != "" {
			text = q.Header + "\n" + text
		}
		fields = append(fields, map[string]any{"tag": "markdown", "content": feishuMarkdownLabel.Replace(text)})
		if len(q.Options) == 0 {
			fields = append(fields, map[string]any{"tag": "input", "name": name, "required": true, "placeholder": plain("请输入")})
			continue
		}
		var options []map[string]any
		for index, option := range q.Options {
			label := option.Label
			if option.Description != "" {
				label += " · " + option.Description
			}
			options = append(options, map[string]any{"text": plain(label), "value": strconv.Itoa(index)})
		}
		if q.Other {
			options = append(options, map[string]any{"text": plain("其他，请在下方填写"), "value": "other"})
		}
		fields = append(fields, map[string]any{"tag": "select_static", "name": name, "required": true, "placeholder": plain("请选择"), "options": options})
		if q.Other {
			fields = append(fields, map[string]any{"tag": "input", "name": name + "_other", "placeholder": plain("选了其他时请填写")})
		}
	}
	submit := button("提交", "submit", "primary")
	submit["name"], submit["form_action_type"] = "submit_answers", "submit"
	return []map[string]any{{"tag": "form", "name": "codex_input", "elements": append(fields, submit)}}
}

var feishuMarkdownLabel = strings.NewReplacer(
	"\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "~", "\\~",
	"`", "\\`", "<", "‹", ">", "›",
)

func feishuAssetMarkdown(asset model.ChatAsset) string {
	label := feishuMarkdownLabel.Replace(asset.Name)
	if asset.Kind == model.ChatAssetPreview {
		return "![" + label + "](" + asset.Key + ")"
	}
	link := strings.NewReplacer("(", "%28", ")", "%29").Replace(asset.URL)
	return "[" + label + "](" + link + ")"
}

type feishuUploadReader struct {
	*bytes.Reader
	name string
}

func (r feishuUploadReader) Name() string { return r.name }

func feishuUpload(client *lark.Client) func(context.Context, model.ChatAsset) (string, error) {
	return func(ctx context.Context, asset model.ChatAsset) (string, error) {
		reader := feishuUploadReader{bytes.NewReader(asset.Data), asset.Name}
		var status, code int
		if asset.Kind == model.ChatAssetFile {
			body := larkim.NewCreateFileReqBodyBuilder().FileType("stream").FileName(asset.Name).File(reader).Build()
			res, err := client.Im.File.Create(ctx, larkim.NewCreateFileReqBuilder().Body(body).Build())
			if err == nil && res != nil {
				status, code = res.StatusCode, res.Code
				if status == http.StatusOK && res.Success() && res.Data != nil && value(res.Data.FileKey) != "" {
					return value(res.Data.FileKey), nil
				}
			}
		} else {
			body := larkim.NewCreateImageReqBodyBuilder().ImageType("message").Image(reader).Build()
			res, err := client.Im.Image.Create(ctx, larkim.NewCreateImageReqBuilder().Body(body).Build())
			if err == nil && res != nil {
				status, code = res.StatusCode, res.Code
				if status == http.StatusOK && res.Success() && res.Data != nil && value(res.Data.ImageKey) != "" {
					return value(res.Data.ImageKey), nil
				}
			}
		}
		return "", &DeliveryError{
			Retryable: status == 0 || status >= 500 || status == 429 || code == 99991400 || (status == http.StatusOK && code == 0),
			Message:   fmt.Sprintf("Feishu rejected attachment upload (HTTP %d, code %d)", status, code),
		}
	}
}

func runFeishu(ctx context.Context, st *store.Store, log *slog.Logger, board, dataDir, socket string, app config.FeishuApp) error {
	client := lark.NewClient(app.AppID, app.AppSecret, lark.WithLogger(quietSDK{}), lark.WithReqTimeout(10*time.Second))
	botID, err := feishuBot(ctx, client)
	if err != nil {
		return err
	}

	bridge := &Bridge{Store: st, Account: model.ChatAccount{Provider: "feishu", Tenant: "app-scoped", Application: app.AppID},
		Board: board, Log: log, React: feishuReaction(client), Deliver: feishuDelivery(client), Upload: feishuUpload(client),
		Agent:  configuredChatAgent(dataDir, socket, st),
		RunCLI: assistantCLI(dataDir, socket)}
	if err := st.RecoverChatJobs(ctx, bridge.accountKey()); err != nil {
		return err
	}
	events := dispatcher.NewEventDispatcher("", "")
	events.OnP2CardActionTrigger(bridge.confirmFeishuCard)
	events.OnP2MessageReceiveV1(func(eventCtx context.Context, event *larkim.P2MessageReceiveV1) error {
		if msg, ok := feishuInbound(event, app.AppID, botID); ok {
			return bridge.Accept(eventCtx, msg)
		}
		return nil
	})
	var online atomic.Bool
	httpClient := &http.Client{Timeout: 10 * time.Second}
	ws := larkws.NewClient(app.AppID, app.AppSecret, larkws.WithEventHandler(events), larkws.WithLogger(quietSDK{}),
		larkws.WithHttpClient(httpClient),
		larkws.WithOnReady(func() { online.Store(true) }),
		larkws.WithOnReconnected(func() { online.Store(true) }),
		larkws.WithOnReconnecting(func() { online.Store(false) }),
		larkws.WithOnDisconnected(func() { online.Store(false) }))
	runCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Go(func() { bridge.Run(runCtx, online.Load) })
	workers.Go(func() { bridge.runAgent(runCtx) })
	err = ws.Start(runCtx)
	cancel()
	workers.Wait()
	if ctx.Err() == nil && err != nil {
		return errors.New("Feishu long connection failed; check event subscription settings")
	}
	return nil
}

// RunFeishu supervises the built-in connector and reloads changed credentials.
func RunFeishu(ctx context.Context, st *store.Store, log *slog.Logger, dataDir, socket, board string) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var active config.FeishuApp
	var cancel context.CancelFunc
	var done <-chan struct{}
	stop := func() {
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
		cancel, done = nil, nil
	}
	defer stop()

	for {
		app, err := config.ReadFeishuApp(dataDir)
		if err != nil {
			log.Warn("read Feishu credentials", "err", err)
			app = config.FeishuApp{}
		}
		if app != active {
			stop()
			active = app
			if app.AppID != "" {
				account := model.ChatAccount{Provider: "feishu", Tenant: "app-scoped", Application: app.AppID}
				runCtx, end := context.WithCancel(ctx)
				wait := make(chan struct{})
				cancel, done = end, wait
				go func() {
					defer close(wait)
					for runCtx.Err() == nil {
						err := runFeishu(runCtx, st, log, board, dataDir, socket, app)
						status := model.ChatConnection{ChatAccount: account}
						if err != nil {
							status.LastError = err.Error()
							log.Warn("built-in Feishu integration", "err", err)
						}
						if runCtx.Err() != nil {
							reportCtx, finish := context.WithTimeout(context.WithoutCancel(runCtx), 2*time.Second)
							if err := st.ReportChatConnection(reportCtx, status); err != nil {
								log.Warn("report Feishu disconnect", "err", err)
							}
							finish()
							break
						}
						if err := st.ReportChatConnection(runCtx, status); err != nil && runCtx.Err() == nil {
							log.Warn("report Feishu connection", "err", err)
						}
						timer := time.NewTimer(15 * time.Second)
						select {
						case <-runCtx.Done():
						case <-timer.C:
						}
						timer.Stop()
					}
				}()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
