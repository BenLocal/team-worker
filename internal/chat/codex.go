package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/sysproc"
)

func configuredChatAgent(dataDir string) ChatAgent {
	home := strings.TrimSpace(os.Getenv("TEAM_WORKER_FEISHU_ROUTER_CODEX_HOME"))
	if home == "" {
		local := filepath.Join(dataDir, "feishu-codex")
		if info, err := os.Stat(filepath.Join(local, "config.toml")); err == nil && info.Mode().IsRegular() {
			home = local
		}
	}
	if home == "" {
		return configuredGatewayAgent()
	}
	return codexRouter(home, strings.TrimSpace(os.Getenv("TEAM_WORKER_FEISHU_ROUTER_MODEL")))
}

func codexRouter(home, model string) ChatAgent {
	return func(ctx context.Context, kind string, input agentInput) (agentOutput, error) {
		if !filepath.IsAbs(home) {
			return agentOutput{}, errors.New("The routing Codex home must be an absolute path")
		}
		if info, err := os.Stat(home); err != nil || !info.IsDir() {
			return agentOutput{}, errors.New("The dedicated routing Codex home does not exist")
		}
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		workDir, err := os.MkdirTemp("", "tw-feishu-codex-")
		if err != nil {
			return agentOutput{}, err
		}
		defer os.RemoveAll(workDir)
		cmd := exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://",
			"--disable", "shell_tool", "--disable", "unified_exec", "--disable", "plugins", "--disable", "apps",
			"--disable", "multi_agent", "--disable", "multi_agent_v2", "--disable", "goals",
			"--disable", "view_image", "--disable", "image_generation", "-c", `web_search="disabled"`,
			"-c", "mcp_servers={}")
		cmd.Dir = workDir
		cmd.Env = routingCodexEnv(home)
		cmd.SysProcAttr = sysproc.Detached()
		cmd.Cancel = func() error { return sysproc.TerminateGroup(cmd.Process.Pid) }
		cmd.WaitDelay = 2 * time.Second
		inputPipe, err := cmd.StdinPipe()
		if err != nil {
			return agentOutput{}, err
		}
		defer inputPipe.Close()
		outputPipe, err := cmd.StdoutPipe()
		if err != nil {
			return agentOutput{}, err
		}
		defer outputPipe.Close()
		if err := cmd.Start(); err != nil {
			return agentOutput{}, fmt.Errorf("Start the routing Codex app-server: %w", err)
		}
		defer func() {
			_ = inputPipe.Close()
			cancel()
			_ = cmd.Wait()
		}()
		return requestCodexTurn(ctx, inputPipe, outputPipe, workDir, model, kind, input)
	}
}

func routingCodexEnv(home string) []string {
	env := []string{"HOME=" + home, "CODEX_HOME=" + home}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if slices.Contains([]string{"PATH", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR"}, key) {
			env = append(env, entry)
		}
	}
	return env
}

type codexMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Params json.RawMessage `json:"params"`
}

type codexTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func requestCodexTurn(ctx context.Context, stdin io.Writer, stdout io.Reader, workDir, model, kind string, input agentInput) (agentOutput, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := make(chan codexMessage)
	go func() {
		defer close(stream)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		for scanner.Scan() {
			var msg codexMessage
			if json.Unmarshal(scanner.Bytes(), &msg) != nil {
				continue
			}
			select {
			case stream <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	encoder := json.NewEncoder(stdin)
	send := func(id int, method string, params any) error {
		request := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
		if id != 0 {
			request["id"] = id
		}
		return encoder.Encode(request)
	}
	next := func() (codexMessage, error) {
		select {
		case <-ctx.Done():
			return codexMessage{}, ctx.Err()
		case msg, ok := <-stream:
			if !ok {
				return msg, errors.New("Codex app-server closed its output")
			}
			if msg.Method != "" && len(msg.ID) > 0 {
				return msg, errors.New("Routing Codex requested client-side interaction")
			}
			return msg, nil
		}
	}
	var early []codexMessage
	rpc := func(id int, method string, params, out any) error {
		if err := send(id, method, params); err != nil {
			return err
		}
		for {
			msg, err := next()
			if err != nil {
				return err
			}
			if string(msg.ID) != fmt.Sprint(id) {
				if method == "turn/start" && (msg.Method == "item/started" || msg.Method == "item/completed" || msg.Method == "turn/completed") {
					if len(early) == 128 {
						return errors.New("Too many Codex events before turn acknowledgement")
					}
					early = append(early, msg)
				}
				continue
			}
			if len(msg.Error) != 0 && string(msg.Error) != "null" {
				return fmt.Errorf("Codex app-server rejected %s", method)
			}
			if out != nil {
				return json.Unmarshal(msg.Result, out)
			}
			return nil
		}
	}
	if err := rpc(1, "initialize", map[string]any{"clientInfo": map[string]string{
		"name": "team-worker", "version": "1",
	}}, nil); err != nil {
		return agentOutput{}, err
	}
	if err := send(0, "initialized", map[string]any{}); err != nil {
		return agentOutput{}, err
	}
	var config struct {
		Config struct {
			MCPServers map[string]json.RawMessage `json:"mcp_servers"`
		} `json:"config"`
	}
	if err := rpc(2, "config/read", map[string]string{"cwd": workDir}, &config); err != nil {
		return agentOutput{}, err
	}
	if len(config.Config.MCPServers) > 0 {
		return agentOutput{}, errors.New("The routing Codex configuration must have no MCP servers")
	}
	var start struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	field, prompt := routingPrompt(kind)
	thread := map[string]any{
		"ephemeral": true, "cwd": workDir, "approvalPolicy": "never", "sandbox": "read-only",
		"baseInstructions":      "You classify and summarize support messages. Use only the supplied data. Do not use tools.",
		"developerInstructions": prompt,
	}
	if model != "" {
		thread["model"] = model
	}
	if err := rpc(3, "thread/start", thread, &start); err != nil {
		return agentOutput{}, err
	}
	if start.Thread.ID == "" {
		return agentOutput{}, errors.New("Codex app-server returned no thread ID")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return agentOutput{}, err
	}
	var turn struct {
		Turn codexTurn `json:"turn"`
	}
	property := map[string]any{"type": "string"}
	if kind == "route" {
		property["enum"] = append([]string{""}, input.Teams...)
	}
	if err := rpc(4, "turn/start", map[string]any{
		"threadId": start.Thread.ID, "input": []map[string]string{{"type": "text", "text": string(data)}},
		"outputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{field},
			"properties": map[string]any{field: property}},
	}, &turn); err != nil {
		return agentOutput{}, err
	}
	if turn.Turn.ID == "" {
		return agentOutput{}, errors.New("Codex app-server returned no turn ID")
	}
	var text string
	for {
		var msg codexMessage
		if len(early) > 0 {
			msg, early = early[0], early[1:]
		} else {
			var err error
			msg, err = next()
			if err != nil {
				return agentOutput{}, err
			}
		}
		if msg.Method != "item/started" && msg.Method != "item/completed" && msg.Method != "turn/completed" {
			continue
		}
		var event struct {
			ThreadID string                      `json:"threadId"`
			TurnID   string                      `json:"turnId"`
			Turn     codexTurn                   `json:"turn"`
			Item     struct{ Type, Text string } `json:"item"`
		}
		if err := json.Unmarshal(msg.Params, &event); err != nil {
			return agentOutput{}, err
		}
		if event.ThreadID != start.Thread.ID {
			continue
		}
		if msg.Method == "turn/completed" && event.Turn.ID == turn.Turn.ID {
			if event.Turn.Status != "completed" || text == "" {
				return agentOutput{}, errors.New("Codex app-server did not complete a routing answer")
			}
			var out agentOutput
			if err := json.Unmarshal([]byte(text), &out); err != nil {
				return out, errors.New("Codex app-server returned invalid routing JSON")
			}
			return out, nil
		}
		if event.TurnID != turn.Turn.ID {
			continue
		}
		switch event.Item.Type {
		case "userMessage", "reasoning", "plan":
		case "agentMessage":
			if msg.Method == "item/completed" {
				text = event.Item.Text
			}
		default:
			return agentOutput{}, errors.New("Routing Codex attempted to use a tool")
		}
	}
}
