package model

type FeishuAppInfo struct {
	AppID                string `json:"appId"`
	ManagedByEnvironment bool   `json:"managedByEnvironment"`
}

type ChatAccount struct {
	Provider    string `json:"provider"`
	Tenant      string `json:"tenant"`
	Application string `json:"application"`
}

type ChatBinding struct {
	ChatAccount
	Team          string   `json:"team"`
	TeamCreatedAt string   `json:"teamCreatedAt"`
	BindingID     string   `json:"bindingId"`
	StartCursor   string   `json:"startCursor"`
	NoticeID      string   `json:"noticeId,omitempty"`
	NoticeStart   string   `json:"noticeStart,omitempty"`
	Conversation  string   `json:"conversation"`
	Alias         string   `json:"alias"`
	Senders       []string `json:"senders"`
	Receive       bool     `json:"receive"`
	Notify        bool     `json:"notify"`
	Reports       bool     `json:"reports"`
	Requests      bool     `json:"requests"`
	Preview       bool     `json:"preview"`
	Events        []string `json:"events"`
}

type ChatSettings struct {
	Revision           int64          `json:"revision"`
	Bindings           []ChatBinding  `json:"bindings"`
	Recipient          *ChatRecipient `json:"recipient,omitempty"`
	UpdateRecipient    bool           `json:"updateRecipient,omitempty"`
	FeishuSender       *ChatRecipient `json:"feishuSender,omitempty"`
	UpdateFeishuSender bool           `json:"updateFeishuSender,omitempty"`
	FeishuSenderGrant  string         `json:"feishuSenderGrant,omitempty"`
}

type ChatRecipient struct {
	ChatAccount
	OpenID string `json:"openId"`
}

func (s ChatSettings) AllowsFeishuSender(account ChatAccount, openID string) bool {
	return s.FeishuSenderGrant != "" && s.FeishuSender != nil &&
		s.FeishuSender.ChatAccount == account && s.FeishuSender.OpenID == openID
}

type ChatOrigin struct {
	MessageID, Sender, Conversation, ThreadID string
	Group, Thread                             bool
}

type ChatOutbound struct {
	Conversation, Text, ReplyTo, OpenID string
	Thread                              bool
	Markdown                            bool        `json:",omitempty"`
	CardTitle                           string      `json:",omitempty"`
	HasAssets                           bool        `json:",omitempty"`
	Assets                              []ChatAsset `json:"-"`
}

const (
	ChatAssetPreview    = "preview"
	ChatAssetImage      = "image"
	ChatAssetFile       = "file"
	ChatAssetLink       = "link"
	ChatImageLimit      = 10_000_000
	ChatFileLimit       = 30_000_000
	ChatAssetCount      = 8
	ChatResultBodyLimit = 4*((ChatFileLimit+2*ChatAssetCount)/3) + 256<<10
)

type ChatAsset struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	Data []byte `json:"data,omitempty"`
	Key  string `json:"-"`
}

func (o ChatOrigin) Reply(text string) ChatOutbound {
	return ChatOutbound{Conversation: o.Conversation, ReplyTo: o.MessageID, Thread: o.Thread, Text: text}
}

type ChatRequest struct {
	ID, Account, Body, Team, BindingID, State string
	Origin                                    ChatOrigin
	CreatedAt                                 int64
}

type ChatResultInput struct {
	AgentID       string      `json:"agentId"`
	RunID         string      `json:"runId"`
	Material      string      `json:"material"`
	Final         bool        `json:"final"`
	GroupApproved bool        `json:"groupApproved"`
	Assets        []ChatAsset `json:"assets,omitempty"`
}

type ChatConnection struct {
	ChatAccount
	AppliedRevision int64  `json:"appliedRevision"`
	Connected       bool   `json:"connected"`
	SeenAt          string `json:"seenAt"`
	LastError       string `json:"lastError,omitempty"`
}

type ChatBlockedItem struct {
	RequestID string `json:"requestId"`
	Team      string `json:"team"`
	Reason    string `json:"reason"`
	Content   string `json:"content"`
}
