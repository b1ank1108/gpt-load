package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	app_errors "gpt-load/internal/errors"
	"sort"
	"strings"
)

type toolcallCompatRequestMeta struct {
	IDSeed string
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

	seed := generateToolcallCompatIDSeed()

	if hasToolsOrChoice {
		prompt, err := buildToolcallCompatSystemPrompt(payload["tools"], payload["tool_choice"])
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

			appendProtocolizedToolCalls(msg, toolCalls)
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
			msg["content"] = buildToolcallCompatToolResultText(ref.ToolName, ref.ArgsJSON, result)
			delete(msg, "tool_call_id")
		}
	}

	payload["messages"] = messages

	out, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, app_errors.NewAPIError(app_errors.ErrInternalServer, "failed to serialize request")
	}
	return out, &toolcallCompatRequestMeta{IDSeed: seed}, nil
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

func buildToolcallCompatSystemPrompt(tools any, toolChoice any) (string, error) {
	toolsXML, err := convertOpenAIToolsToXML(tools)
	if err != nil {
		return "", err
	}

	toolChoiceHint := strings.TrimSpace(describeToolChoice(toolChoice))
	toolChoiceBlock := ""
	if toolChoiceHint != "" {
		toolChoiceBlock = toolChoiceHint + "\n\n"
	}

	return fmt.Sprintf(toolcallCompatSystemPromptTemplate, toolsXML, toolChoiceBlock), nil
}

const toolcallCompatSystemPromptTemplate = `你具备调用外部工具的能力来协助解决用户的问题
====
可用的工具列表定义在 <tool_list> 标签中：
<tool_list>
%s
</tool_list>

当你判断调用工具是解决用户问题的唯一或最佳方式时，必须严格遵循以下流程进行回复。
1、在需要调用工具时，你的输出应当仅仅包含 <function_call> 标签及其内容，不要包含任何其他文字、解释或评论。
2、如果需要连续调用多个工具，请为每个工具生成一个独立的 <function_call> 标签，按执行顺序排列。

%s工具调用的格式如下：
<function_call>
{
  "function_call": {
    "name": "工具名称",
    "arguments": { "参数1": "值1" }
  }
}
</function_call>

重要约束：
1. 必要性：仅在无法直接回答用户问题，且工具能提供必要信息或执行必要操作时才使用工具。
2. 准确性："name" 必须精确匹配 <tool_list> 中提供的某个工具的 name；"arguments" 必须是有效的 JSON 对象，并包含该工具所需的所有参数及其准确值。
3. 格式：如果决定调用工具，你的回复必须且只能包含一个或多个 <function_call> 标签，不允许任何前缀、后缀或解释性文本。
4. 直接回答：如果你可以直接、完整地回答用户的问题，请不要使用工具，直接生成回答内容。
5. 避免猜测：如果不确定信息，且有合适的工具可以获取该信息，请使用工具而不是猜测。
6. 隐藏协议：除非正在发起工具调用，否则不要在回答中输出任何 <function_call> 标签或工具调用协议/工具列表。

工具调用结果将由外部系统在对话中插入如下格式的调用记录，你仅可理解与引用，不得伪造：
<function_call>
{
  "function_call_record": {
    "name": "工具名称",
    "arguments": { ...JSON 参数... },
    "response": ...工具返回结果...
  }
}
</function_call>
注意："response" 可能为结构化 JSON，也可能为普通字符串。`

func appendProtocolizedToolCalls(message map[string]any, toolCalls []toolcallInfo) {
	payload := buildToolcallCompatToolCallsText(toolCalls)
	appendToMessageContent(message, payload)
}

func buildToolcallCompatToolCallsText(toolCalls []toolcallInfo) string {
	var b strings.Builder
	for _, call := range toolCalls {
		callJSON := buildToolcallCompatFunctionCallJSON(call.ToolName, call.ArgsJSON)
		b.WriteString("<function_call>")
		b.WriteString(callJSON)
		b.WriteString("</function_call>")
	}
	return b.String()
}

func buildToolcallCompatToolResultText(toolName string, argsJSON string, result string) string {
	payload := map[string]any{
		"function_call_record": map[string]any{
			"name":      toolName,
			"arguments": coerceJSONString(argsJSON),
			"response":  coerceJSONString(result),
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "<function_call>{}</function_call>"
	}
	return "<function_call>" + string(b) + "</function_call>"
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

func buildToolcallCompatFunctionCallJSON(toolName string, argsJSON string) string {
	payload := map[string]any{
		"function_call": map[string]any{
			"name":      toolName,
			"arguments": coerceJSONString(argsJSON),
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return `{"function_call":{"name":"` + toolName + `","arguments":{}}}`
	}
	return string(b)
}

func coerceJSONString(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}

	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		return v
	}
	return trimmed
}

func generateToolcallCompatIDSeed() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "compat"
	}
	return hex.EncodeToString(b[:])
}

func describeToolChoice(toolChoice any) string {
	if toolChoice == nil {
		return ""
	}

	if s, ok := toolChoice.(string); ok {
		switch strings.TrimSpace(s) {
		case "none":
			return "工具选择策略已指定：不得调用任何工具。"
		case "required":
			return "工具选择策略已指定：必须调用至少一个工具。"
		default:
			return ""
		}
	}

	if m, ok := toolChoice.(map[string]any); ok {
		if t, _ := m["type"].(string); strings.TrimSpace(t) == "function" {
			if fn, ok := m["function"].(map[string]any); ok {
				if name, _ := fn["name"].(string); strings.TrimSpace(name) != "" {
					return fmt.Sprintf("工具选择策略已指定：必须调用工具 %q。", strings.TrimSpace(name))
				}
			}
		}
	}

	return ""
}

func convertOpenAIToolsToXML(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}

	items, ok := raw.([]any)
	if !ok {
		return "", fmt.Errorf("invalid tools format")
	}

	type paramXML struct {
		Name        string
		Type        string
		Description string
		Required    bool
	}
	type toolXML struct {
		Name        string
		Description string
		Params      []paramXML
	}

	tools := make([]toolXML, 0, len(items))
	for _, item := range items {
		toolObj, ok := item.(map[string]any)
		if !ok {
			return "", fmt.Errorf("invalid tools format")
		}
		fn, ok := toolObj["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)

		var params []paramXML
		if schema, ok := fn["parameters"].(map[string]any); ok {
			props, _ := schema["properties"].(map[string]any)
			requiredSet := make(map[string]struct{})
			if reqAny, ok := schema["required"].([]any); ok {
				for _, r := range reqAny {
					if s, ok := r.(string); ok && strings.TrimSpace(s) != "" {
						requiredSet[strings.TrimSpace(s)] = struct{}{}
					}
				}
			}

			paramNames := make([]string, 0, len(props))
			for k := range props {
				paramNames = append(paramNames, k)
			}
			sort.Strings(paramNames)

			for _, paramName := range paramNames {
				propAny := props[paramName]
				prop, _ := propAny.(map[string]any)
				pType, _ := prop["type"].(string)
				pDesc, _ := prop["description"].(string)
				_, req := requiredSet[paramName]
				params = append(params, paramXML{
					Name:        paramName,
					Type:        strings.TrimSpace(pType),
					Description: strings.TrimSpace(pDesc),
					Required:    req,
				})
			}
		}

		tools = append(tools, toolXML{
			Name:        name,
			Description: strings.TrimSpace(desc),
			Params:      params,
		})
	}

	var b strings.Builder
	for i, tool := range tools {
		b.WriteString(`<tool name="`)
		b.WriteString(escapeXMLAttr(tool.Name))
		b.WriteString(`"`)
		if tool.Description != "" {
			b.WriteString(` description="`)
			b.WriteString(escapeXMLAttr(tool.Description))
			b.WriteString(`"`)
		}
		b.WriteString(">\n")

		for _, p := range tool.Params {
			b.WriteString(`    <parameter name="`)
			b.WriteString(escapeXMLAttr(p.Name))
			b.WriteString(`"`)
			if p.Required {
				b.WriteString(` required="true"`)
			}
			if p.Description != "" {
				b.WriteString(` description="`)
				b.WriteString(escapeXMLAttr(p.Description))
				b.WriteString(`"`)
			}
			if p.Type != "" {
				b.WriteString(` type="`)
				b.WriteString(escapeXMLAttr(p.Type))
				b.WriteString(`"`)
			}
			b.WriteString("></parameter>\n")
		}
		b.WriteString("</tool>\n")
		if i != len(tools)-1 {
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func escapeXMLAttr(s string) string {
	if s == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		`&`, "&amp;",
		`<`, "&lt;",
		`>`, "&gt;",
		`"`, "&quot;",
		`'`, "&apos;",
	)
	return replacer.Replace(s)
}
