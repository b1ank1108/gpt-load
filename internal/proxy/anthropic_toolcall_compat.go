package proxy

import (
	"bufio"
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

type anthropicCompatWithToolcallCompatTransformer struct {
	base   *anthropicCompatTransformer
	idSeed string
	trigger string
}

func newAnthropicCompatWithToolcallCompatTransformer(base *anthropicCompatTransformer, idSeed string, trigger string) *anthropicCompatWithToolcallCompatTransformer {
	return &anthropicCompatWithToolcallCompatTransformer{
		base:    base,
		idSeed:  idSeed,
		trigger: trigger,
	}
}

func (t *anthropicCompatWithToolcallCompatTransformer) HandleUpstreamError(c *gin.Context, statusCode int, upstreamBody []byte) bool {
	return t.base.HandleUpstreamError(c, statusCode, upstreamBody)
}

func (t *anthropicCompatWithToolcallCompatTransformer) HandleSuccess(c *gin.Context, resp *http.Response, isStream bool) error {
	if isStream {
		return t.streamOpenAIToAnthropicWithToolcallCompat(c, resp)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read upstream body: %w", err)
	}

	decompressed, err := utils.DecompressResponse(resp.Header.Get("Content-Encoding"), bodyBytes)
	if err != nil {
		decompressed = bodyBytes
	}

	converted, err := t.convertOpenAIToAnthropicWithToolcallCompat(decompressed)
	if err != nil {
		return err
	}

	c.Header("Content-Type", "application/json")
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(converted)
	return nil
}

func (t *anthropicCompatWithToolcallCompatTransformer) convertOpenAIToAnthropicWithToolcallCompat(body []byte) ([]byte, error) {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse openai response: %w", err)
	}

	openaiID, _ := resp["id"].(string)
	model, _ := resp["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(t.base.requestedModel)
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

	if len(toolCalls) == 0 {
		plain, calls, triggered, parsed := extractToolcallCompatInvokeCalls(contentText, t.trigger)
		if triggered {
			contentText = plain
			if parsed {
				seed := toolcallCompatIDSeed(t.idSeed)
				toolCalls = make([]openAIToolCall, 0, len(calls))
				for i, call := range calls {
					toolCalls = append(toolCalls, openAIToolCall{
						ID:        fmt.Sprintf("call_%s_%d", seed, i+1),
						Name:      strings.TrimSpace(call.ToolName),
						Arguments: strings.TrimSpace(call.ArgsJSON),
					})
				}
			}
		}
	}

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

func (t *anthropicCompatWithToolcallCompatTransformer) streamOpenAIToAnthropicWithToolcallCompat(c *gin.Context, resp *http.Response) error {
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
			"id":            t.base.messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         nonEmpty(t.base.requestedModel, "openai"),
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  t.base.inputTokens,
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
		tokenModel            = strings.TrimSpace(t.base.tokenModel)
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

	emitTextDelta := func(text string) error {
		if text == "" {
			return nil
		}
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
				"text": text,
			},
		}); err != nil {
			return err
		}
		outputTokens += estimateTokens(text, tokenModel)
		flusher.Flush()
		return nil
	}

	emitToolUse := func(toolID, toolName, argsJSON string, toolIndex int) error {
		if err := closeTextBlock(); err != nil {
			return err
		}
		if err := openToolBlock(toolID, toolName, toolIndex); err != nil {
			return err
		}
		argsJSON = strings.TrimSpace(argsJSON)
		if argsJSON != "" {
			if err := writeAnthropicSSE(c.Writer, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": currentToolBlockIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": argsJSON,
				},
			}); err != nil {
				return err
			}
			outputTokens += estimateTokens(argsJSON, tokenModel)
			flusher.Flush()
		}
		return closeToolBlock()
	}

	finishStream := func() error {
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
			"input_tokens":  t.base.inputTokens,
			"output_tokens": outputTokens,
			"token_model":   tokenModel,
		}).Debug("anthropic compat: streaming usage estimated")
		return nil
	}

	triggerSignal := strings.TrimSpace(t.trigger)
	if triggerSignal == "" {
		return fmt.Errorf("toolcall compat: missing trigger signal")
	}
	detector := newToolcallCompatTriggerDetector(triggerSignal)
	protocolTriggered := false
	protocolFallback := false
	protocolBuffer := ""

	finalizeProtocol := func() error {
		invokes, ok := extractAllInvokeXML(protocolBuffer)
		if ok {
			calls := make([]toolcallCompatInvokeCall, 0, len(invokes))
			for _, invokeXML := range invokes {
				call, parsed := parseToolcallCompatInvokeXML(invokeXML)
				if parsed {
					calls = append(calls, call)
				}
			}
			if len(calls) > 0 {
				seed := toolcallCompatIDSeed(t.idSeed)
				for i, call := range calls {
					toolID := fmt.Sprintf("call_%s_%d", seed, i+1)
					if err := emitToolUse(toolID, strings.TrimSpace(call.ToolName), call.ArgsJSON, i); err != nil {
						return err
					}
				}
				lastFinishReason = "tool_calls"
				return finishStream()
			}
		}
		if err := emitTextDelta(protocolBuffer); err != nil {
			return err
		}
		return finishStream()
	}

	const maxProtocolBufferBytes = 512 * 1024

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
			if fr, ok := choice["finish_reason"].(string); ok && strings.TrimSpace(fr) != "" {
				lastFinishReason = strings.TrimSpace(fr)
			}

			delta, _ := choice["delta"].(map[string]any)
			if delta == nil {
				continue
			}

			content, _ := delta["content"].(string)
			if content != "" {
				if protocolFallback {
					if err := emitTextDelta(content); err != nil {
						return err
					}
					} else if protocolTriggered {
						protocolBuffer += content
						if len(protocolBuffer) > maxProtocolBufferBytes {
							protocolFallback = true
							protocolTriggered = false
						if err := emitTextDelta(protocolBuffer); err != nil {
							return err
							}
							protocolBuffer = ""
						}
					} else {
						detected, emitText := detector.Process(content)
						if emitText != "" {
						if err := emitTextDelta(emitText); err != nil {
							return err
						}
					}
					if detected {
						protocolTriggered = true
						protocolBuffer = detector.TakeRemainder()
							if len(protocolBuffer) > maxProtocolBufferBytes {
								protocolFallback = true
								protocolTriggered = false
							if err := emitTextDelta(protocolBuffer); err != nil {
								return err
								}
								protocolBuffer = ""
							}
						}
					}
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

	if !protocolTriggered && !protocolFallback {
		if remainder := detector.FlushRemainder(); remainder != "" {
			if err := emitTextDelta(remainder); err != nil {
				return err
			}
		}
	}

	if protocolTriggered {
		return finalizeProtocol()
	}

	return finishStream()
}
