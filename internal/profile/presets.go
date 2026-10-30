package profile

import (
	"fmt"
	"sort"
	"strings"
)

// Source says where a preset comes from.
const (
	SourceBuiltin = "builtin"
	SourceLocal   = "local"
)

// claudeCompatible is a preset for an Anthropic-compatible Claude Code endpoint.
func claudeCompatible(description, authEnv, baseURL, model string, extra map[string]string) Preset {
	env := map[string]string{
		"ANTHROPIC_MODEL":                model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   model,
	}
	for k, v := range extra {
		env[k] = v
	}
	return Preset{Description: description, AuthEnv: authEnv, ClientOverrides: map[string]Profile{
		"claude": {Agent: "claude", Model: model, BaseURL: baseURL, Env: env},
	}}
}

// Builtin returns the built-in preset catalog, the same as ClawTeam's.
func Builtin() map[string]Preset {
	minimaxExtra := map[string]string{"API_TIMEOUT_MS": "3000000", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
	presets := map[string]Preset{
		"anthropic-official": {Description: "Official Claude Code setup using direct Anthropic auth.",
			AuthEnv: "ANTHROPIC_API_KEY", ClientOverrides: map[string]Profile{
				"claude": {Agent: "claude", Model: "sonnet"}}},
		"openai-official": {Description: "Official Codex setup using OpenAI auth.",
			AuthEnv: "OPENAI_API_KEY", ClientOverrides: map[string]Profile{
				"codex": {Agent: "codex", Model: "gpt-5.4"}}},
		"google-ai-studio": {Description: "Official Gemini CLI setup using a Gemini API key from Google AI Studio.",
			AuthEnv: "GEMINI_API_KEY", ClientOverrides: map[string]Profile{
				"gemini": {Agent: "gemini", Model: "gemini-2.5-pro"}}},
		"moonshot-cn": {Description: "Moonshot China endpoints for Claude-compatible Kimi and native Kimi CLI.",
			AuthEnv: "MOONSHOT_API_KEY", ClientOverrides: map[string]Profile{
				"claude": {Agent: "claude", Model: "kimi-k2.5", BaseURL: "https://api.moonshot.cn/anthropic",
					Env: map[string]string{
						"ANTHROPIC_MODEL":                "kimi-k2.5",
						"ANTHROPIC_DEFAULT_OPUS_MODEL":   "kimi-k2.5",
						"ANTHROPIC_DEFAULT_SONNET_MODEL": "kimi-k2.5",
						"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "kimi-k2.5",
						"CLAUDE_CODE_SUBAGENT_MODEL":     "kimi-k2.5",
						"ENABLE_TOOL_SEARCH":             "false",
					}},
				"kimi": {Agent: "kimi", Model: "kimi-k2-thinking-turbo", BaseURL: "https://api.moonshot.cn/v1"}}},
		"deepseek": claudeCompatible("DeepSeek via Anthropic-compatible Claude Code endpoint.",
			"DEEPSEEK_API_KEY", "https://api.deepseek.com/anthropic", "DeepSeek-V3.2", nil),
		"zhipu-cn": claudeCompatible("Zhipu GLM via the mainland China Anthropic-compatible endpoint.",
			"ZHIPU_API_KEY", "https://open.bigmodel.cn/api/anthropic", "glm-5", nil),
		"zhipu-global": claudeCompatible("Zhipu GLM via the global Anthropic-compatible endpoint.",
			"ZHIPU_API_KEY", "https://api.z.ai/api/anthropic", "glm-5", nil),
		"bailian": {Description: "Alibaba Bailian Claude-compatible endpoint.",
			AuthEnv: "DASHSCOPE_API_KEY", ClientOverrides: map[string]Profile{
				"claude": {Agent: "claude", BaseURL: "https://dashscope.aliyuncs.com/apps/anthropic"}}},
		"bailian-coding": {Description: "Alibaba Bailian coding endpoint for Claude Code.",
			AuthEnv: "DASHSCOPE_API_KEY", ClientOverrides: map[string]Profile{
				"claude": {Agent: "claude", BaseURL: "https://coding.dashscope.aliyuncs.com/apps/anthropic"}}},
		"minimax-cn": claudeCompatible("MiniMax China Anthropic-compatible Claude Code endpoint.",
			"MINIMAX_API_KEY", "https://api.minimaxi.com/anthropic", "MiniMax-M2.7", minimaxExtra),
		"minimax-global": claudeCompatible("MiniMax global Anthropic-compatible Claude Code endpoint.",
			"MINIMAX_API_KEY", "https://api.minimax.io/anthropic", "MiniMax-M2.7", minimaxExtra),
		"openrouter": {Description: "OpenRouter preset spanning Claude Code, Codex, and Gemini CLI.",
			AuthEnv: "OPENROUTER_API_KEY", ClientOverrides: map[string]Profile{
				"claude": {Agent: "claude", Model: "anthropic/claude-sonnet-4.6", BaseURL: "https://openrouter.ai/api",
					Env: map[string]string{
						"ANTHROPIC_MODEL":                "anthropic/claude-sonnet-4.6",
						"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "anthropic/claude-haiku-4.5",
						"ANTHROPIC_DEFAULT_SONNET_MODEL": "anthropic/claude-sonnet-4.6",
						"ANTHROPIC_DEFAULT_OPUS_MODEL":   "anthropic/claude-opus-4.6",
					}},
				"codex":  {Agent: "codex", Model: "openai/gpt-5.4", BaseURL: "https://openrouter.ai/api/v1"},
				"gemini": {Agent: "gemini", Model: "google/gemini-2.5-pro", BaseURL: "https://openrouter.ai/api"}}},
		"gemini-vertex": {Description: "Gemini CLI using Vertex ADC and local gcloud credentials.",
			Env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "true", "GOOGLE_CLOUD_LOCATION": "global"},
			ClientOverrides: map[string]Profile{
				"gemini": {Agent: "gemini", Model: "gemini-2.5-flash"}}},
	}
	for k, v := range presets {
		presets[k] = v.normalized()
	}
	return presets
}

// Listed is a preset with its source.
type Listed struct {
	Preset Preset `json:"preset"`
	Source string `json:"source"`
}

// List combines the built-ins with local presets; a local preset replaces
// a built-in of the same name.
func List(c Config) map[string]Listed {
	out := map[string]Listed{}
	for name, p := range Builtin() {
		out[name] = Listed{Preset: p, Source: SourceBuiltin}
	}
	for name, p := range c.Presets {
		out[name] = Listed{Preset: p.normalized(), Source: SourceLocal}
	}
	return out
}

// Find returns a preset by name, local first.
func Find(c Config, name string) (Listed, error) {
	l, ok := List(c)[name]
	if !ok {
		return Listed{}, fmt.Errorf("Unknown preset '%s'", name)
	}
	return l, nil
}

// Editable returns a copy of the local preset, else of the built-in, else an
// empty one, so editing a built-in name creates a local override.
func Editable(c Config, name string) Preset {
	if p, ok := c.Presets[name]; ok {
		return p.Clone()
	}
	if p, ok := Builtin()[name]; ok {
		return p.Clone()
	}
	return Preset{}.normalized()
}

// NormalizeClient lower-cases a client name and maps aliases.
func NormalizeClient(client string) string {
	c := strings.ToLower(strings.TrimSpace(client))
	switch c {
	case "claude-code":
		return "claude"
	case "codex-cli":
		return "codex"
	}
	return c
}

// Clients lists a preset's client names in order.
func Clients(p Preset) []string {
	out := make([]string, 0, len(p.ClientOverrides))
	for k := range p.ClientOverrides {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Generate derives a client profile from a preset: the client override, with
// the preset's description, endpoint and auth variable filling empty fields
// and the preset's env under the override's. The default name is <client>-<preset>.
func Generate(c Config, presetName, client, name string) (string, Profile, error) {
	l, err := Find(c, presetName)
	if err != nil {
		return "", Profile{}, err
	}
	client = NormalizeClient(client)
	override, ok := l.Preset.ClientOverrides[client]
	if !ok {
		return "", Profile{}, fmt.Errorf("Preset '%s' does not define a client override for '%s'", presetName, client)
	}
	p := override.Clone()
	if len(p.Command) == 0 && p.Agent == "" {
		p.Agent = client
	}
	if p.Description == "" {
		p.Description = l.Preset.Description
	}
	if p.BaseURL == "" {
		p.BaseURL = l.Preset.BaseURL
	}
	if p.APIKeyEnv == "" {
		p.APIKeyEnv = l.Preset.AuthEnv
	}
	if len(l.Preset.Env) > 0 {
		env := map[string]string{}
		for k, v := range l.Preset.Env {
			env[k] = v
		}
		for k, v := range p.Env {
			env[k] = v
		}
		p.Env = env
	}
	if name == "" {
		name = client + "-" + presetName
	}
	return name, p, nil
}
