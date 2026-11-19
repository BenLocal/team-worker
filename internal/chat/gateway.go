package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const gatewayCaller = "team-worker"

func configuredGatewayAgent() ChatAgent {
	base := strings.TrimRight(os.Getenv("LLM_API_BASE"), "/")
	model := strings.TrimSpace(os.Getenv("TEAM_WORKER_FEISHU_ROUTER_MODEL"))
	if base == "" || model == "" {
		return nil
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil
	}
	trustProxy, _ := strconv.ParseBool(os.Getenv("LLM_TRUST_ENV"))
	thinking, _ := strconv.ParseBool(os.Getenv("TEAM_WORKER_FEISHU_ROUTER_ENABLE_THINKING"))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !trustProxy {
		transport.Proxy = nil
	}
	client := &http.Client{Transport: transport, Timeout: 45 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	apiKey := os.Getenv("LLM_API_KEY")
	var readContext sync.Once
	var limit int
	return func(ctx context.Context, kind string, input agentInput) (agentOutput, error) {
		prompt, _ := teamAssistantPromptFor(kind)
		user, err := json.Marshal(input)
		if err != nil {
			return agentOutput{}, err
		}
		readContext.Do(func() {
			catalogCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			limit = routerContextLimit(catalogCtx, client, strings.TrimSuffix(base, "/v1"), apiKey, model)
		})
		if len([]rune(string(user))) > limit/2 {
			return agentOutput{}, errors.New("Routing input exceeds the model context budget")
		}
		body, err := json.Marshal(map[string]any{
			"model": model, "caller": gatewayCaller, "enable_thinking": thinking,
			"messages":   []map[string]string{{"role": "system", "content": prompt}, {"role": "user", "content": string(user)}},
			"max_tokens": min(4096, limit/4),
		})
		if err != nil {
			return agentOutput{}, err
		}
		var response struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		err = gatewayRequest(ctx, client, http.MethodPost, base+"/chat/completions", apiKey, body, &response)
		if err != nil {
			return agentOutput{}, err
		}
		if len(response.Choices) == 0 || response.Choices[0].FinishReason != "stop" {
			return agentOutput{}, errors.New("Routing agent did not finish")
		}
		var output agentOutput
		if err := json.Unmarshal([]byte(response.Choices[0].Message.Content), &output); err != nil {
			return agentOutput{}, errors.New("Routing agent returned invalid JSON")
		}
		return output, nil
	}
}

func gatewayRequest(ctx context.Context, client *http.Client, method, endpoint, apiKey string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Routing gateway returned HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out)
}

func routerContextLimit(ctx context.Context, client *http.Client, base, key, model string) int {
	var catalog struct {
		Data []struct {
			ID     string `json:"id"`
			Detail struct {
				MaxModelLen int `json:"max_model_len"`
			} `json:"detail"`
		} `json:"data"`
	}
	if gatewayRequest(ctx, client, http.MethodGet, base+"/models", key, nil, &catalog) == nil {
		for _, m := range catalog.Data {
			if m.ID == model && m.Detail.MaxModelLen > 0 {
				return m.Detail.MaxModelLen
			}
		}
	}
	return 32000
}
