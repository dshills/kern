package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// conversation is the provider-neutral transcript: system prompt, tool
// definitions and an ordered list of turns.
type conversation struct {
	system string
	tools  []toolDef
	turns  []turn
}

type turn struct {
	role   string     // user | assistant | tool
	text   string     // user or assistant text
	calls  []toolCall // assistant tool calls
	call   *toolCall  // tool result's call
	result string     // tool result
}

type toolCall struct {
	id   string
	name string
	args string // JSON object
}

type usage struct{ input, output int }

func newConversation(system string, tools []toolDef) *conversation {
	return &conversation{system: system, tools: tools}
}

func (c *conversation) user(text string) { c.turns = append(c.turns, turn{role: "user", text: text}) }
func (c *conversation) toolResult(call toolCall, result string) {
	cc := call
	c.turns = append(c.turns, turn{role: "tool", call: &cc, result: result})
}

// llmClient performs one model step: it appends the assistant turn to the
// conversation and returns the text, the tool calls and the token usage.
type llmClient interface {
	step(ctx context.Context, conv *conversation) (string, []toolCall, usage, error)
}

func newClient(provider, model string) (llmClient, error) {
	switch provider {
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is not set")
		}
		return &openaiClient{key: key, model: model, url: "https://api.openai.com/v1/responses"}, nil
	case "wafer":
		// Wafer serverless serves OpenAI's Responses API too
		// (https://docs.wafer.ai/serverless); WAFER_API_URL may name any of
		// its endpoints, and the base before /v1 is kept
		key, base := os.Getenv("WAFER_API_KEY"), os.Getenv("WAFER_API_URL")
		if key == "" {
			return nil, fmt.Errorf("WAFER_API_KEY is not set")
		}
		if base == "" {
			return nil, fmt.Errorf("WAFER_API_URL is not set")
		}
		// strip a /v1 path segment and what follows it
		if i := strings.Index(base, "/v1/"); i >= 0 {
			base = base[:i]
		} else {
			base = strings.TrimSuffix(base, "/v1")
		}
		return &openaiClient{key: key, model: model, url: strings.TrimRight(base, "/") + "/v1/responses"}, nil
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set")
		}
		return &anthropicClient{key: key, model: model}, nil
	}
	return nil, fmt.Errorf("unknown provider %q", provider)
}

func paramsSchema(t toolDef) map[string]any {
	props := map[string]any{}
	var req []string
	for k, d := range t.Params {
		props[k] = map[string]any{"type": "string", "description": d}
		req = append(req, k)
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(req) > 0 {
		s["required"] = req
	}
	return s
}

// httpClient bounds every request so a stalled connection is retried
// instead of consuming the trial's budget.
var httpClient = &http.Client{Timeout: 180 * time.Second}

func post(ctx context.Context, url string, headers map[string]string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			last = err
		} else {
			rb, rerr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if rerr != nil {
				last = rerr
			} else if resp.StatusCode == 200 {
				return rb, nil
			}
			if rerr == nil {
				last = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(rb), 300))
				if resp.StatusCode < 500 && resp.StatusCode != 429 {
					return nil, last
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(2<<attempt) * time.Second):
		}
	}
	return nil, last
}

// ---- OpenAI Responses API ----
// (function tools with reasoning models require the Responses API; the
// whole conversation is resent each step, stateless)

type openaiClient struct{ key, model, url string }

func (o *openaiClient) step(ctx context.Context, conv *conversation) (string, []toolCall, usage, error) {
	var input []map[string]any
	for _, t := range conv.turns {
		switch t.role {
		case "user":
			// the explicit type, optional for OpenAI, is required by Wafer
			input = append(input, map[string]any{"type": "message", "role": "user", "content": t.text})
		case "assistant":
			if t.text != "" {
				input = append(input, map[string]any{"type": "message", "role": "assistant", "content": t.text})
			}
			for _, c := range t.calls {
				input = append(input, map[string]any{"type": "function_call", "call_id": c.id, "name": c.name, "arguments": c.args})
			}
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": t.call.id, "output": t.result})
		}
	}
	var tools []map[string]any
	for _, td := range conv.tools {
		tools = append(tools, map[string]any{"type": "function", "name": td.Name, "description": td.Description, "parameters": paramsSchema(td)})
	}
	body := map[string]any{"model": o.model, "instructions": conv.system, "input": input, "tools": tools, "store": false}
	rb, err := post(ctx, o.url, map[string]string{"Authorization": "Bearer " + o.key}, body)
	if err != nil {
		return "", nil, usage{}, err
	}
	var resp struct {
		Output []struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			Args    string `json:"arguments"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rb, &resp); err != nil {
		return "", nil, usage{}, fmt.Errorf("openai: unexpected response %s", truncate(string(rb), 300))
	}
	var text string
	var calls []toolCall
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					text += c.Text
				}
			}
		case "function_call":
			calls = append(calls, toolCall{id: item.CallID, name: item.Name, args: item.Args})
		}
	}
	conv.turns = append(conv.turns, turn{role: "assistant", text: text, calls: calls})
	return text, calls, usage{resp.Usage.Input, resp.Usage.Output}, nil
}

// ---- Anthropic messages ----

type anthropicClient struct{ key, model string }

func (a *anthropicClient) step(ctx context.Context, conv *conversation) (string, []toolCall, usage, error) {
	var msgs []map[string]any
	for _, t := range conv.turns {
		switch t.role {
		case "user":
			msgs = append(msgs, map[string]any{"role": "user", "content": t.text})
		case "assistant":
			var content []map[string]any
			if t.text != "" {
				content = append(content, map[string]any{"type": "text", "text": t.text})
			}
			for _, c := range t.calls {
				var input map[string]any
				_ = json.Unmarshal([]byte(c.args), &input)
				if input == nil {
					input = map[string]any{}
				}
				content = append(content, map[string]any{"type": "tool_use", "id": c.id, "name": c.name, "input": input})
			}
			msgs = append(msgs, map[string]any{"role": "assistant", "content": content})
		case "tool":
			msgs = append(msgs, map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": t.call.id, "content": t.result}}})
		}
	}
	// consecutive user turns (several tool results) are merged by the API when adjacent in one message; merge them here
	msgs = mergeUserTurns(msgs)
	var tools []map[string]any
	for _, td := range conv.tools {
		tools = append(tools, map[string]any{"name": td.Name, "description": td.Description, "input_schema": paramsSchema(td)})
	}
	body := map[string]any{"model": a.model, "max_tokens": 8192, "system": conv.system, "messages": msgs, "tools": tools}
	rb, err := post(ctx, "https://api.anthropic.com/v1/messages", map[string]string{"x-api-key": a.key, "anthropic-version": "2023-06-01"}, body)
	if err != nil {
		return "", nil, usage{}, err
	}
	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rb, &resp); err != nil {
		return "", nil, usage{}, fmt.Errorf("anthropic: unexpected response %s", truncate(string(rb), 300))
	}
	var text string
	var calls []toolCall
	for _, c := range resp.Content {
		switch c.Type {
		case "text":
			text += c.Text
		case "tool_use":
			calls = append(calls, toolCall{id: c.ID, name: c.Name, args: string(c.Input)})
		}
	}
	conv.turns = append(conv.turns, turn{role: "assistant", text: text, calls: calls})
	return text, calls, usage{resp.Usage.Input, resp.Usage.Output}, nil
}

func mergeUserTurns(msgs []map[string]any) []map[string]any {
	var out []map[string]any
	for _, m := range msgs {
		if len(out) > 0 && out[len(out)-1]["role"] == "user" && m["role"] == "user" {
			prev := out[len(out)-1]
			pc, ok1 := prev["content"].([]map[string]any)
			mc, ok2 := m["content"].([]map[string]any)
			if ok1 && ok2 {
				prev["content"] = append(pc, mc...)
				continue
			}
		}
		out = append(out, m)
	}
	return out
}
