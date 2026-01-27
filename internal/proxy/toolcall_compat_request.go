package proxy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	app_errors "gpt-load/internal/errors"
	"strings"
)

type toolcallCompatRequestMeta struct {
	TriggerSignal string
}

type toolcallBackref struct {
	ToolName string
	ArgsJSON string
}

func preprocessToolcallCompatChatCompletionsRequest(body []byte) ([]byte, *toolcallCompatRequestMeta, *app_errors.APIError) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error())
	}

	rawMessages, hasMessages := payload["messages"]
	messages, ok := rawMessages.([]any)
	if !ok {
		if hasMessages {
			return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid messages format")
		}
		return body, nil, nil
	}

	hasToolsOrChoice := payload["tools"] != nil || payload["tool_choice"] != nil

	hasCompatSignals := hasToolsOrChoice
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role == "tool" {
			hasCompatSignals = true
			break
		}
		if m["tool_calls"] != nil {
			hasCompatSignals = true
			break
		}
	}

	if !hasCompatSignals {
		return body, nil, nil
	}

	trigger := generateToolcallCompatTriggerSignal()

	if hasToolsOrChoice {
		prompt, err := buildToolcallCompatSystemPrompt(payload["tools"], payload["tool_choice"], trigger)
		if err != nil {
			return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error())
		}
		delete(payload, "tools")
		delete(payload, "tool_choice")

		systemMsg := map[string]any{
			"role":    "system",
			"content": prompt,
		}
		messages = append([]any{systemMsg}, messages...)
	}

	toolCallsByID := make(map[string]toolcallBackref)
	for i := range messages {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		if role == "assistant" && msg["tool_calls"] != nil {
			toolCalls, backrefs, err := parseOpenAIToolCalls(msg["tool_calls"])
			if err != nil {
				return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error())
			}
			for id, ref := range backrefs {
				toolCallsByID[id] = ref
			}

			appendProtocolizedToolCalls(msg, trigger, toolCalls)
			delete(msg, "tool_calls")
			continue
		}

		if role == "tool" {
			callID, ok := msg["tool_call_id"].(string)
			if !ok || strings.TrimSpace(callID) == "" {
				return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "tool message missing tool_call_id")
			}
			ref, ok := toolCallsByID[callID]
			if !ok {
				return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("tool_call_id not found: %s", callID))
			}
			result := stringifyContent(msg["content"])
			msg["role"] = "user"
			msg["content"] = buildToolcallCompatToolResultText(callID, ref.ToolName, ref.ArgsJSON, result)
			delete(msg, "tool_call_id")
		}
	}

	payload["messages"] = messages

	out, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, app_errors.NewAPIError(app_errors.ErrInternalServer, "failed to serialize request")
	}
	return out, &toolcallCompatRequestMeta{TriggerSignal: trigger}, nil
}

type toolcallInfo struct {
	ToolName string
	ArgsJSON string
}

func parseOpenAIToolCalls(raw any) ([]toolcallInfo, map[string]toolcallBackref, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("invalid tool_calls format")
	}

	infos := make([]toolcallInfo, 0, len(items))
	backrefs := make(map[string]toolcallBackref, len(items))
	for _, item := range items {
		call, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("invalid tool_call entry")
		}

		id, _ := call["id"].(string)
		fn, ok := call["function"].(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("invalid tool_call function")
		}
		name, _ := fn["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, nil, fmt.Errorf("tool_call missing function name")
		}

		args := ""
		if v, ok := fn["arguments"].(string); ok {
			args = v
		} else if fn["arguments"] != nil {
			args = stringifyContent(fn["arguments"])
		}

		infos = append(infos, toolcallInfo{
			ToolName: name,
			ArgsJSON: args,
		})
		if strings.TrimSpace(id) != "" {
			backrefs[id] = toolcallBackref{ToolName: name, ArgsJSON: args}
		}
	}
	return infos, backrefs, nil
}

func buildToolcallCompatSystemPrompt(tools any, toolChoice any, trigger string) (string, error) {
	toolBlob := ""
	if tools != nil {
		b, err := json.Marshal(tools)
		if err != nil {
			return "", fmt.Errorf("invalid tools format")
		}
		toolBlob = string(b)
	}

	choiceBlob := ""
	if toolChoice != nil {
		b, err := json.Marshal(toolChoice)
		if err != nil {
			return "", fmt.Errorf("invalid tool_choice format")
		}
		choiceBlob = string(b)
	}

	var b strings.Builder
	b.WriteString("You are in tool-call compatibility mode.\n")
	b.WriteString("When you need to call a tool, respond with the following exact format:\n")
	b.WriteString(trigger)
	b.WriteString("\n")
	b.WriteString("<function_calls><function_call><tool>TOOL_NAME</tool><args_json><![CDATA[JSON_ARGS]]></args_json></function_call></function_calls>\n")
	b.WriteString("Do not include any additional text after the trigger and XML.\n")

	if toolBlob != "" {
		b.WriteString("\nAvailable tools (OpenAI `tools` JSON):\n")
		b.WriteString(toolBlob)
		b.WriteString("\n")
	}
	if choiceBlob != "" {
		b.WriteString("\nTool choice (OpenAI `tool_choice` JSON):\n")
		b.WriteString(choiceBlob)
		b.WriteString("\n")
	}

	return b.String(), nil
}

func appendProtocolizedToolCalls(message map[string]any, trigger string, toolCalls []toolcallInfo) {
	payload := buildToolcallCompatToolCallsText(trigger, toolCalls)
	appendToMessageContent(message, payload)
}

func buildToolcallCompatToolCallsText(trigger string, toolCalls []toolcallInfo) string {
	var b strings.Builder
	b.WriteString(trigger)
	b.WriteString("\n<function_calls>")
	for _, call := range toolCalls {
		b.WriteString("<function_call><tool>")
		b.WriteString(call.ToolName)
		b.WriteString("</tool><args_json><![CDATA[")
		b.WriteString(escapeCDATA(call.ArgsJSON))
		b.WriteString("]]></args_json></function_call>")
	}
	b.WriteString("</function_calls>")
	return b.String()
}

func buildToolcallCompatToolResultText(callID string, toolName string, argsJSON string, result string) string {
	var b strings.Builder
	b.WriteString("<function_results><function_result>")
	b.WriteString("<tool_call_id>")
	b.WriteString(callID)
	b.WriteString("</tool_call_id><tool>")
	b.WriteString(toolName)
	b.WriteString("</tool><args_json><![CDATA[")
	b.WriteString(escapeCDATA(argsJSON))
	b.WriteString("]]></args_json><result><![CDATA[")
	b.WriteString(escapeCDATA(result))
	b.WriteString("]]></result></function_result></function_results>")
	return b.String()
}

func appendToMessageContent(message map[string]any, extra string) {
	current := stringifyContent(message["content"])
	current = strings.TrimRight(current, "\n")
	if current != "" {
		current += "\n\n"
	}
	message["content"] = current + extra
}

func stringifyContent(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

func escapeCDATA(s string) string {
	if s == "" {
		return ""
	}
	return strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
}

func generateToolcallCompatTriggerSignal() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("TOOLCALL_COMPAT_TRIGGER_%d", len(b))
	}
	return "TOOLCALL_COMPAT_TRIGGER_" + hex.EncodeToString(b[:])
}

func toolcallCompatHasTrigger(content string, trigger string) bool {
	return bytes.Contains([]byte(content), []byte(trigger))
}
