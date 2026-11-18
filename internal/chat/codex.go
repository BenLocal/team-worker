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

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/sysproc"
)

func configuredChatAgent(dataDir, socket string, st *store.Store) ChatAgent {
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
	return codexTeamAssistant(home, strings.TrimSpace(os.Getenv("TEAM_WORKER_FEISHU_ROUTER_MODEL")), dataDir, socket, st)
}

func codexTeamAssistant(home, model, dataDir, socket string, st *store.Store) ChatAgent {
	return func(ctx context.Context, kind string, input agentInput) (agentOutput, error) {
		if !filepath.IsAbs(home) {
			return agentOutput{}, errors.New("The team assistant Codex home must be an absolute path")
		}
		if info, err := os.Stat(home); err != nil || !info.IsDir() {
			return agentOutput{}, errors.New("The dedicated team assistant Codex home does not exist")
		}
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var key, previousThread string
		if input.SessionKey != "" {
			key = digest(filepath.Clean(home), input.SessionKey)
			var err error
			previousThread, err = st.ChatAssistantSession(ctx, key)
			if err != nil {
				return agentOutput{}, err
			}
		}
		workDir := filepath.Join(home, "workspace")
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			return agentOutput{}, err
		}
		args := []string{"app-server", "--listen", "stdio://"}
		restricted := !codexToolsAllowed(kind, input)
		env := assistantCodexEnv(home, dataDir, socket, restricted)
		if restricted {
			restrictions, err := scopedCodexArgs(ctx, workDir, env)
			if err != nil {
				return agentOutput{}, err
			}
			args = append(args, restrictions...)
		}
		cmd := exec.CommandContext(ctx, "codex", args...)
		cmd.Dir = workDir
		cmd.Env = env
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
		out, err := requestCodexTurn(ctx, inputPipe, outputPipe, workDir, model, kind, previousThread, input)
		if err != nil || key == "" || previousThread != "" {
			return out, err
		}
		return out, st.SaveChatAssistantSession(ctx, key, out.threadID)
	}
}

func scopedCodexArgs(ctx context.Context, workDir string, env []string) ([]string, error) {
	args := []string{"--disable", "shell_tool", "--disable", "unified_exec", "--disable", "plugins", "--disable", "apps",
		"--disable", "multi_agent", "--disable", "multi_agent_v2", "--disable", "goals", "--disable", "skill_mcp_dependency_install",
		"--disable", "view_image", "--disable", "image_generation", "-c", `web_search="disabled"`}
	cmd := exec.CommandContext(ctx, "codex", append([]string{"mcp", "list", "--json"}, args...)...)
	cmd.Dir, cmd.Env = workDir, env
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("Read the assistant MCP configuration: %w", err)
	}
	var servers []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(output, &servers); err != nil {
		return nil, errors.New("Codex returned an invalid MCP server list")
	}
	// Codex merges config tables, so an empty override does not remove configured servers.
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		if strings.Contains(server.Name, ".") || model.ValidateIdentifier(server.Name, "MCP server name", false) != nil {
			return nil, errors.New("Cannot disable a scoped MCP server with an unsupported name")
		}
		args = append(args, "-c", "mcp_servers."+server.Name+".enabled=false")
	}
	return args, nil
}

func assistantCodexEnv(home, dataDir, socket string, scoped bool) []string {
	env := []string{"HOME=" + home, "CODEX_HOME=" + home, "TEAM_WORKER_SERVER_URL=", "TEAM_WORKER_NO_AUTOSTART=1"}
	if !scoped {
		env = append(env, "TEAM_WORKER_DATA_DIR="+dataDir, "TEAM_WORKER_SOCKET="+socket)
	}
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

var errCodexInteraction = errors.New("Codex requested interactive approval or input")
var errCodexToolInInternalTurn = errors.New("Codex attempted to use a tool in an internal turn")

func codexToolsAllowed(kind string, input agentInput) bool {
	return kind == "answer" && !input.Scoped
}

func requestCodexTurn(ctx context.Context, stdin io.Writer, stdout io.Reader, workDir, model, kind, previousThread string, input agentInput) (agentOutput, error) {
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
				return msg, errCodexInteraction
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
	allowTools := codexToolsAllowed(kind, input)
	if !allowTools {
		var config struct {
			Config struct {
				MCPServers map[string]struct {
					Enabled *bool `json:"enabled"`
				} `json:"mcp_servers"`
			} `json:"config"`
		}
		if err := rpc(2, "config/read", map[string]string{"cwd": workDir}, &config); err != nil {
			return agentOutput{}, err
		}
		for _, server := range config.Config.MCPServers {
			if server.Enabled == nil || *server.Enabled {
				return agentOutput{}, errors.New("Scoped Codex turn unexpectedly has enabled MCP servers")
			}
		}
	}
	var start struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	field, prompt := teamAssistantPromptFor(kind)
	thread := map[string]any{
		"cwd":                   workDir,
		"developerInstructions": prompt,
	}
	if input.Scoped {
		thread["approvalPolicy"] = "never"
		thread["sandbox"] = "read-only"
	}
	if model != "" {
		thread["model"] = model
	}
	method := "thread/start"
	if previousThread == "" {
		thread["ephemeral"] = input.SessionKey == ""
	} else {
		method = "thread/resume"
		thread["threadId"] = previousThread
		thread["excludeTurns"] = true
	}
	if err := rpc(3, method, thread, &start); err != nil {
		return agentOutput{}, err
	}
	if start.Thread.ID == "" || (previousThread != "" && start.Thread.ID != previousThread) {
		return agentOutput{}, errors.New("Codex app-server returned an unexpected thread ID")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return agentOutput{}, err
	}
	turnText := string(data)
	// A resumed thread can still carry the instructions from its previous mode.
	switch kind {
	case "answer":
		turnText = assistantAnswerInstructions + "\n\n" + assistantReplyContract + "\n\n" + turnText
	case "route":
		turnText = assistantRouteInstructions + "\n\n" + turnText
	}
	var turn struct {
		Turn codexTurn `json:"turn"`
	}
	fields := []string{field}
	properties := map[string]any{field: map[string]any{"type": "string"}}
	if kind == "route" {
		fields = append(fields, "action", "team", "leader", "subject", "owner", "description", "runtime")
		for _, name := range fields[1:] {
			properties[name] = map[string]any{"type": "string"}
		}
		properties["action"] = map[string]any{"type": "string", "enum": teamAssistantActions}
		properties["alias"] = map[string]any{"type": "string", "enum": append([]string{""}, input.Teams...)}
	}
	if err := rpc(4, "turn/start", map[string]any{
		"threadId": start.Thread.ID, "input": []map[string]string{{"type": "text", "text": turnText}},
		"outputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": fields, "properties": properties},
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
			// Some resumed answer turns return the reply in the previous route format.
			if kind == "answer" && out.Action == "answer" && strings.TrimSpace(out.Summary) == "" {
				out.Summary = out.Description
			}
			out.threadID = start.Thread.ID
			return out, nil
		}
		if event.TurnID != turn.Turn.ID {
			continue
		}
		switch event.Item.Type {
		case "agentMessage":
			if msg.Method == "item/completed" {
				text = event.Item.Text
			}
		case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch", "imageView", "imageGeneration":
			if !allowTools {
				return agentOutput{}, errCodexToolInInternalTurn
			}
		}
	}
}
