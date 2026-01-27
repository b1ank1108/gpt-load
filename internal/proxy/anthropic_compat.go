package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

type anthropicCompatTransformer struct {
	requestedModel string
	messageID      string
	tokenModel     string
	inputTokens    int
}

func newAnthropicCompatTransformer(requestedModel string) *anthropicCompatTransformer {
	return &anthropicCompatTransformer{
		requestedModel: requestedModel,
		messageID:      fmt.Sprintf("msg_%d", time.Now().UnixNano()),
	}
}

func (t *anthropicCompatTransformer) WithStreamingUsage(tokenModel string, inputTokens int) *anthropicCompatTransformer {
	t.tokenModel = tokenModel
	t.inputTokens = inputTokens
	return t
}

func (t *anthropicCompatTransformer) HandleUpstreamError(c *gin.Context, statusCode int, upstreamBody []byte) bool {
	converted := convertOpenAIErrorToAnthropic(statusCode, upstreamBody)
	c.Header("Content-Type", "application/json")
	c.Status(statusCode)
	_, _ = c.Writer.Write(converted)
	return true
}

func (t *anthropicCompatTransformer) HandleSuccess(c *gin.Context, resp *http.Response, isStream bool) error {
	if isStream {
		return t.streamOpenAIToAnthropic(c, resp)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read upstream body: %w", err)
	}

	decompressed, err := utils.DecompressResponse(resp.Header.Get("Content-Encoding"), bodyBytes)
	if err != nil {
		decompressed = bodyBytes
	}

	converted, err := convertOpenAIChatCompletionToAnthropic(decompressed, t.requestedModel)
	if err != nil {
		return err
	}

	c.Header("Content-Type", "application/json")
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(converted)
	return nil
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicRequest struct {
	Model         string             `json:"model"`
	System        json.RawMessage    `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	MaxTokens     int                `json:"max_tokens,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Tools         []anthropicTool    `json:"tools,omitempty"`
	ToolChoice    json.RawMessage    `json:"tool_choice,omitempty"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

func convertAnthropicMessagesToOpenAI(body []byte) ([]byte, string, error) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", fmt.Errorf("parse anthropic request: %w", err)
	}

	openAIMessages := make([]any, 0, len(req.Messages)+1)

	if systemContent := parseAnthropicSystem(req.System); systemContent != "" {
		openAIMessages = append(openAIMessages, map[string]any{
			"role":    "system",
			"content": systemContent,
		})
	}

	for _, msg := range req.Messages {
		role := "user"
		if msg.Role == "assistant" {
			role = "assistant"
		}

		text, blocks, contentIsBlocks, err := parseAnthropicContent(msg.Content)
		if err != nil {
			return nil, req.Model, err
		}

		if !contentIsBlocks {
			openAIMessages = append(openAIMessages, map[string]any{
				"role":    role,
				"content": text,
			})
			continue
		}

		switch role {
		case "assistant":
			content := strings.TrimSpace(joinAnthropicTextBlocks(blocks))
			toolCalls := buildOpenAIToolCallsFromAnthropic(blocks)
			m := map[string]any{
				"role":    "assistant",
				"content": content,
			}
			if len(toolCalls) > 0 {
				m["tool_calls"] = toolCalls
			}
			openAIMessages = append(openAIMessages, m)
		default:
			openAIMessages = append(openAIMessages, buildOpenAIUserAndToolMessagesFromAnthropic(blocks)...)
		}
	}

	openAIReq := map[string]any{
		"model":    req.Model,
		"messages": openAIMessages,
	}

	if req.MaxTokens > 0 {
		openAIReq["max_tokens"] = req.MaxTokens
	}
	if req.Stream {
		openAIReq["stream"] = true
	}
	if req.Temperature != nil {
		openAIReq["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		openAIReq["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		openAIReq["stop"] = req.StopSequences
	}

	if len(req.Tools) > 0 {
		openAIReq["tools"] = mapAnthropicToolsToOpenAI(req.Tools)
	}
	if mappedToolChoice, ok := mapAnthropicToolChoice(req.ToolChoice); ok {
		openAIReq["tool_choice"] = mappedToolChoice
	}

	out, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, req.Model, fmt.Errorf("marshal openai request: %w", err)
	}
	return out, req.Model, nil
}

func parseAnthropicSystem(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}

	switch firstJSONToken(raw) {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	case '[':
		var items []any
		if err := json.Unmarshal(raw, &items); err == nil {
			parts := make([]string, 0, len(items))
			for _, item := range items {
				switch v := item.(type) {
				case string:
					parts = append(parts, v)
				case map[string]any:
					if text, ok := v["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
			return strings.Join(parts, "\n")
		}
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func parseAnthropicContent(raw json.RawMessage) (string, []anthropicContentBlock, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil, false, nil
	}

	switch firstJSONToken(raw) {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", nil, false, fmt.Errorf("parse content string: %w", err)
		}
		return s, nil, false, nil
	case '[':
		var blocks []anthropicContentBlock
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return "", nil, false, fmt.Errorf("parse content blocks: %w", err)
		}
		return "", blocks, true, nil
	default:
		// Fallback for unexpected formats
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", nil, false, fmt.Errorf("parse content: %w", err)
		}
		if s, ok := v.(string); ok {
			return s, nil, false, nil
		}
		return "", nil, false, nil
	}
}

func firstJSONToken(raw json.RawMessage) byte {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return b
		}
	}
	return 0
}

func joinAnthropicTextBlocks(blocks []anthropicContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

func buildOpenAIToolCallsFromAnthropic(blocks []anthropicContentBlock) []any {
	var toolCalls []any
	for _, block := range blocks {
		if block.Type != "tool_use" {
			continue
		}

		id := strings.TrimSpace(block.ID)
		if id == "" {
			id = fmt.Sprintf("toolu_%d", time.Now().UnixNano())
		}

		args := "{}"
		if len(bytes.TrimSpace(block.Input)) > 0 {
			args = string(bytes.TrimSpace(block.Input))
		}

		toolCalls = append(toolCalls, map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      strings.TrimSpace(block.Name),
				"arguments": args,
			},
		})
	}
	return toolCalls
}

func buildOpenAIUserAndToolMessagesFromAnthropic(blocks []anthropicContentBlock) []any {
	var out []any

	var currentText strings.Builder
	flushText := func() {
		text := strings.TrimSpace(currentText.String())
		if text == "" {
			currentText.Reset()
			return
		}
		out = append(out, map[string]any{
			"role":    "user",
			"content": text,
		})
		currentText.Reset()
	}

	for _, block := range blocks {
		switch block.Type {
		case "text":
			currentText.WriteString(block.Text)
		case "tool_result":
			flushText()
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": strings.TrimSpace(block.ToolUseID),
				"content":      stringifyJSON(block.Content),
			})
		}
	}
	flushText()

	if len(out) == 0 {
		out = append(out, map[string]any{"role": "user", "content": ""})
	}
	return out
}

func stringifyJSON(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}

	if firstJSONToken(trimmed) == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
	}

	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return string(trimmed)
	}
	switch vv := v.(type) {
	case string:
		return vv
	default:
		b, err := json.Marshal(vv)
		if err != nil {
			return string(trimmed)
		}
		return string(b)
	}
}

func mapAnthropicToolsToOpenAI(tools []anthropicTool) []any {
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		var parameters any = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
		if len(bytes.TrimSpace(tool.InputSchema)) > 0 {
			var schema any
			if err := json.Unmarshal(tool.InputSchema, &schema); err == nil {
				parameters = schema
			}
		}

		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
			},
		})
	}
	return out
}

func mapAnthropicToolChoice(raw json.RawMessage) (any, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}

	switch vv := v.(type) {
	case string:
		switch vv {
		case "auto", "any":
			return "auto", true
		case "none":
			return "none", true
		default:
			return nil, false
		}
	case map[string]any:
		choiceType, _ := vv["type"].(string)
		switch choiceType {
		case "auto", "any":
			return "auto", true
		case "tool":
			name, _ := vv["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, false
			}
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": name,
				},
			}, true
		default:
			return nil, false
		}
	default:
		return nil, false
	}
}

func convertOpenAIErrorToAnthropic(statusCode int, upstreamBody []byte) []byte {
	message := strings.TrimSpace(string(upstreamBody))
	var parsed map[string]any
	if err := json.Unmarshal(upstreamBody, &parsed); err == nil {
		if errObj, ok := parsed["error"].(map[string]any); ok {
			if msg, ok := errObj["message"].(string); ok && strings.TrimSpace(msg) != "" {
				message = msg
			}
		} else if msg, ok := parsed["message"].(string); ok && strings.TrimSpace(msg) != "" {
			message = msg
		}
	}

	errType := "invalid_request_error"
	switch statusCode {
	case http.StatusUnauthorized:
		errType = "authentication_error"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
	}

	out := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"upstream_error"}}`)
	}
	return b
}

type openAIToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func convertOpenAIChatCompletionToAnthropic(body []byte, requestedModel string) ([]byte, error) {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse openai response: %w", err)
	}

	openaiID, _ := resp["id"].(string)
	model, _ := resp["model"].(string)
	if strings.TrimSpace(model) == "" {
		model = strings.TrimSpace(requestedModel)
	}

	messageID := openaiID
	if strings.TrimSpace(messageID) == "" {
		messageID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	} else if !strings.HasPrefix(messageID, "msg_") {
		messageID = "msg_" + messageID
	}

	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return nil, fmt.Errorf("missing choices in openai response")
	}
	firstChoice, _ := choices[0].(map[string]any)
	finishReason, _ := firstChoice["finish_reason"].(string)

	msgObj, _ := firstChoice["message"].(map[string]any)
	contentText := openAIContentToString(msgObj["content"])
	toolCalls := extractOpenAIToolCalls(msgObj["tool_calls"])

	blocks := make([]any, 0, 1+len(toolCalls))
	if strings.TrimSpace(contentText) != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": contentText})
	}
	for _, call := range toolCalls {
		blocks = append(blocks, map[string]any{
			"type": "tool_use",
			"id":   call.ID,
			"name": call.Name,
			"input": func() any {
				var v any
				if err := json.Unmarshal([]byte(call.Arguments), &v); err == nil {
					return v
				}
				return map[string]any{}
			}(),
		})
	}

	stopReason := mapOpenAIFinishReasonToAnthropic(finishReason, len(toolCalls) > 0)

	inputTokens, outputTokens := 0, 0
	if usage, ok := resp["usage"].(map[string]any); ok {
		inputTokens = asInt(usage["prompt_tokens"])
		outputTokens = asInt(usage["completion_tokens"])
	}

	out := map[string]any{
		"id":            messageID,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       blocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}

	return json.Marshal(out)
}

func mapOpenAIFinishReasonToAnthropic(finishReason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_use"
	}
	switch finishReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func openAIContentToString(v any) string {
	switch vv := v.(type) {
	case string:
		return vv
	case []any:
		var b strings.Builder
		for _, item := range vv {
			switch part := item.(type) {
			case string:
				b.WriteString(part)
			case map[string]any:
				if text, ok := part["text"].(string); ok {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func extractOpenAIToolCalls(v any) []openAIToolCall {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := make([]openAIToolCall, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := obj["id"].(string)
		fnObj, _ := obj["function"].(map[string]any)
		name, _ := fnObj["name"].(string)
		args, _ := fnObj["arguments"].(string)
		out = append(out, openAIToolCall{ID: id, Name: name, Arguments: args})
	}
	return out
}

func (t *anthropicCompatTransformer) streamOpenAIToAnthropic(c *gin.Context, resp *http.Response) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported by writer")
	}

	if err := writeAnthropicSSE(c.Writer, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            t.messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         nonEmpty(t.requestedModel, "openai"),
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  t.inputTokens,
				"output_tokens": 0,
			},
			"content": []any{},
		},
	}); err != nil {
		return err
	}
	flusher.Flush()

	var (
		nextBlockIndex        = 0
		textBlockOpen         = false
		textBlockIndex        = 0
		currentToolCallIndex  = -1
		currentToolBlockIndex = -1
		currentToolBlockOpen  = false
		lastFinishReason      = ""
		tokenModel            = strings.TrimSpace(t.tokenModel)
		outputTokens          = 0
	)

	reader := bufio.NewReader(resp.Body)
	var dataLines []string

	closeTextBlock := func() error {
		if !textBlockOpen {
			return nil
		}
		textBlockOpen = false
		return writeAnthropicSSE(c.Writer, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": textBlockIndex,
		})
	}

	ensureTextBlock := func() error {
		if textBlockOpen {
			return nil
		}
		textBlockIndex = nextBlockIndex
		nextBlockIndex++
		textBlockOpen = true
		return writeAnthropicSSE(c.Writer, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": textBlockIndex,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		})
	}

	closeToolBlock := func() error {
		if !currentToolBlockOpen {
			return nil
		}
		currentToolBlockOpen = false
		return writeAnthropicSSE(c.Writer, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": currentToolBlockIndex,
		})
	}

	openToolBlock := func(toolID, toolName string, toolIndex int) error {
		// Switch tool index => close previous tool block to keep sequential content blocks.
		if currentToolBlockOpen && currentToolCallIndex != toolIndex {
			if err := closeToolBlock(); err != nil {
				return err
			}
		}

		if currentToolBlockOpen && currentToolCallIndex == toolIndex {
			return nil
		}

		currentToolCallIndex = toolIndex
		currentToolBlockIndex = nextBlockIndex
		nextBlockIndex++
		currentToolBlockOpen = true

		return writeAnthropicSSE(c.Writer, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": currentToolBlockIndex,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    toolID,
				"name":  toolName,
				"input": map[string]any{},
			},
		})
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read upstream stream: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
			dataLines = dataLines[:0]
			if payload == "" {
				continue
			}
			if payload == "[DONE]" {
				break
			}

			var chunk map[string]any
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}
			if tokenModel == "" {
				if m, ok := chunk["model"].(string); ok && strings.TrimSpace(m) != "" {
					tokenModel = m
				}
			}

			choices, _ := chunk["choices"].([]any)
			if len(choices) == 0 {
				continue
			}
			choice, _ := choices[0].(map[string]any)
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				lastFinishReason = fr
			}

			delta, _ := choice["delta"].(map[string]any)
			if delta == nil {
				continue
			}

			if content, ok := delta["content"].(string); ok && content != "" {
				if err := closeToolBlock(); err != nil {
					return err
				}
				if err := ensureTextBlock(); err != nil {
					return err
				}
				if err := writeAnthropicSSE(c.Writer, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": textBlockIndex,
					"delta": map[string]any{
						"type": "text_delta",
						"text": content,
					},
				}); err != nil {
					return err
				}
				outputTokens += estimateTokens(content, tokenModel)
				flusher.Flush()
			}

			if toolCallsAny, ok := delta["tool_calls"].([]any); ok && len(toolCallsAny) > 0 {
				if err := closeTextBlock(); err != nil {
					return err
				}

				deltas := make([]map[string]any, 0, len(toolCallsAny))
				for _, item := range toolCallsAny {
					if m, ok := item.(map[string]any); ok {
						deltas = append(deltas, m)
					}
				}
				sort.Slice(deltas, func(i, j int) bool {
					return asInt(deltas[i]["index"]) < asInt(deltas[j]["index"])
				})

				for _, td := range deltas {
					toolIndex := asInt(td["index"])
					toolID, _ := td["id"].(string)
					fnObj, _ := td["function"].(map[string]any)
					toolName, _ := fnObj["name"].(string)
					argsPart, _ := fnObj["arguments"].(string)

					if err := openToolBlock(nonEmpty(toolID, fmt.Sprintf("toolu_%d", time.Now().UnixNano())), toolName, toolIndex); err != nil {
						return err
					}

					if argsPart != "" {
						if err := writeAnthropicSSE(c.Writer, "content_block_delta", map[string]any{
							"type":  "content_block_delta",
							"index": currentToolBlockIndex,
							"delta": map[string]any{
								"type":         "input_json_delta",
								"partial_json": argsPart,
							},
						}); err != nil {
							return err
						}
						outputTokens += estimateTokens(argsPart, tokenModel)
					}
					flusher.Flush()
				}
			}

			continue
		}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if err := closeTextBlock(); err != nil {
		return err
	}
	if err := closeToolBlock(); err != nil {
		return err
	}

	stopReason := mapOpenAIFinishReasonToAnthropic(lastFinishReason, currentToolCallIndex >= 0)

	if err := writeAnthropicSSE(c.Writer, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": outputTokens,
		},
	}); err != nil {
		return err
	}
	if err := writeAnthropicSSE(c.Writer, "message_stop", map[string]any{
		"type": "message_stop",
	}); err != nil {
		return err
	}
	flusher.Flush()
	logrus.WithFields(logrus.Fields{
		"input_tokens":  t.inputTokens,
		"output_tokens": outputTokens,
		"token_model":   tokenModel,
	}).Debug("anthropic compat: streaming usage estimated")
	return nil
}

func nonEmpty(v string, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func writeAnthropicSSE(w io.Writer, event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "event: "+event+"\n"); err != nil {
		return err
	}
	for _, line := range strings.Split(string(payload), "\n") {
		if _, err := io.WriteString(w, "data: "+line+"\n"); err != nil {
			return err
		}
	}
	_, err = io.WriteString(w, "\n")
	return err
}
