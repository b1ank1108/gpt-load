# Anthropic Compat 增强计划

对比 cc-proxy (Deno/TS) 后识别出的改进项，聚焦 OpenAI↔Anthropic 基础兼容层（不含 toolcall compat）。

## 目标文件

核心改动集中在 `internal/proxy/anthropic_compat.go`，涉及：
- `anthropicRequest` 结构体（新增字段）
- `convertAnthropicMessagesToOpenAI()`（请求转换）
- `streamOpenAIToAnthropic()`（SSE 流翻译）
- `convertOpenAIChatCompletionToAnthropic()`（非流式响应转换）

辅助文件：
- `internal/proxy/server.go` — 心跳 goroutine 注入点、ctx 传递
- `internal/proxy/token_estimation.go` — token 重算逻辑

---

## P0-1: 图片/多模态消息转换

### 现状

`buildOpenAIUserAndToolMessagesFromAnthropic()` 中 switch-case 只处理 `text` 和 `tool_result`，`image` 类型的 content block 被静默丢弃。

### 改动要点

在 `anthropic_compat.go` 的 `buildOpenAIUserAndToolMessagesFromAnthropic()` 中新增 `image` case，将 Anthropic base64 image block 转换为 OpenAI `image_url` 格式。

Anthropic 格式：
```json
{
  "type": "image",
  "source": {
    "type": "base64",
    "media_type": "image/png",
    "data": "<base64_data>"
  }
}
```

OpenAI 格式：
```json
{
  "type": "image_url",
  "image_url": {
    "url": "data:image/png;base64,<base64_data>"
  }
}
```

需要同时处理 `anthropicContentBlock` 结构体中缺少 `Source` 字段的问题。当前定义：

```go
// anthropic_compat.go:98-107
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
```

需新增：
```go
Source    *imageSource    `json:"source,omitempty"`
```

同时定义 imageSource 结构体：
```go
type imageSource struct {
    Type      string `json:"type"`
    MediaType string `json:"media_type"`
    Data      string `json:"data"`
}
```

**关键**：`buildOpenAIUserAndToolMessagesFromAnthropic()` 当前输出是 `[]any`，其中 user 消息的 content 是纯 string。带图片时需要改为 content block 数组格式（OpenAI 的 `content: [{type:"text",...},{type:"image_url",...}]`）。这意味着该函数需要重构为能输出 content array 形式的 user 消息。

cc-proxy 参考（`map_claude_to_openai.ts:52-58`）：
```typescript
} else if (block.type === "image") {
  openaiContent.push({
    type: "image_url",
    image_url: {
      url: `data:${block.source.media_type};base64,${block.source.data}`,
    },
  });
}
```

### 非流式路径

`convertOpenAIChatCompletionToAnthropic()` 无需改动——OpenAI 的响应不会包含图片，只需确保请求端正确发送即可。

---

## P0-2: Thinking/Reasoning Block 支持

### 现状

1. 请求端：`anthropicRequest` 没有 `Thinking` 字段，extended thinking 参数被丢弃
2. 流式响应端：`streamOpenAIToAnthropic()` 不识别 `delta.reasoning_content`
3. 非流式响应端：`convertOpenAIChatCompletionToAnthropic()` 不识别 `message.reasoning_content`

### 改动要点

#### 1. 请求端（可选/低优先）

Anthropic 的 thinking 参数（`thinking: {type: "enabled", budget_tokens: N}`）对 OpenAI 上游没有直接对应物。cc-proxy 的做法是注入 prompt hint，但这是一种 hack。**建议暂不实现请求端转换**，仅实现响应端——上游模型（如 DeepSeek R1）自带 reasoning，无需客户端请求 thinking。

如果未来要实现，参考 cc-proxy 的 thinking hint 注入方式：
```typescript
// map_claude_to_openai.ts:10
const THINKING_HINT = "<antml\b:thinking_mode>interleaved</antml><antml\b:max_thinking_length>16000</antml>";
```

#### 2. 流式响应端（核心）

在 `streamOpenAIToAnthropic()` 的 SSE 解析循环中，新增对 `delta.reasoning_content` 的处理：

```go
// 在现有 delta["content"] 处理之后，新增：
if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
    // 确保 thinking block 已打开
    if err := ensureThinkingBlock(); err != nil {
        return err
    }
    if err := writeAnthropicSSE(c.Writer, "content_block_delta", map[string]any{
        "type":  "content_block_delta",
        "index": thinkingBlockIndex,
        "delta": map[string]any{
            "type":     "thinking_delta",
            "thinking": reasoning,
        },
    }); err != nil {
        return err
    }
    flusher.Flush()
}
```

需要新增状态变量：
```go
thinkingBlockOpen  = false
thinkingBlockIndex = 0
```

以及 `ensureThinkingBlock()` / `closeThinkingBlock()` 闭包，与现有的 `ensureTextBlock()` / `closeTextBlock()` 模式一致。

**注意事项**：
- reasoning_content 通常出现在 content 之前，因此 thinking block 应先于 text block
- 当从 reasoning 切换到 content 时，需要 `closeThinkingBlock()` 再 `ensureTextBlock()`
- Anthropic thinking block 格式：`content_block_start` 中 `content_block.type = "thinking"`，delta 中 `delta.type = "thinking_delta"`

#### 3. 非流式响应端

在 `convertOpenAIChatCompletionToAnthropic()` 中检查 `message.reasoning_content`，如果存在则在 content blocks 数组中**前置**一个 thinking block：

```go
if reasoning, ok := message["reasoning_content"].(string); ok && reasoning != "" {
    contentBlocks = append([]any{
        map[string]any{
            "type":     "thinking",
            "thinking": reasoning,
        },
    }, contentBlocks...)
}
```

---

## P1-1: SSE 心跳 Keepalive

### 现状

`streamOpenAIToAnthropic()` 在等待上游首个 token 期间不发送任何数据。如果上游模型思考时间较长（如 o1/o3 的 60s+），中间代理或负载均衡器可能因超时断开连接。

### 改动要点

在 `streamOpenAIToAnthropic()` 开始流式读取前，启动一个心跳 goroutine：

```go
// 在 SSE 读取循环之前
heartbeatTicker := time.NewTicker(5 * time.Second)
defer heartbeatTicker.Stop()

heartbeatDone := make(chan struct{})
go func() {
    defer close(heartbeatDone)
    for {
        select {
        case <-heartbeatTicker.C:
            // SSE 注释行，不会被客户端解析为事件
            _, _ = fmt.Fprintf(c.Writer, ": keepalive\n\n")
            flusher.Flush()
        case <-heartbeatDone:
            return
        }
    }
}()
```

**注意**：需要在循环结束时关闭 heartbeatDone channel。由于 `c.Writer` 不是 goroutine-safe 的，需要用 mutex 保护写入，或者改用 select + channel 方案。

更安全的方案是在 SSE 读取循环中用 select 同时监听 ticker 和数据到达，但这需要将 `reader.ReadString('\n')` 改为非阻塞的 channel 方式——需要评估复杂度。

cc-proxy 参考（`main.ts:435-445`）：5 秒间隔发送 `": keepalive\n\n"`。

---

## P1-2: 客户端断连传播

### 现状

`streamOpenAIToAnthropic()` 的 for 循环中 `reader.ReadString('\n')` 是阻塞调用，不检查客户端是否已断开。如果客户端断连，上游 response body 会继续被读取直到自然结束，浪费资源。

### 改动要点

在 SSE 读取循环中检查 `c.Request.Context()`：

```go
for {
    // 检查客户端是否已断开
    select {
    case <-c.Request.Context().Done():
        return nil // 客户端已断开，静默结束
    default:
    }

    line, err := reader.ReadString('\n')
    // ...
}
```

更好的方案是将 reader 包装为 context-aware 的读取：

```go
ctx := c.Request.Context()

// 在 goroutine 中读取，主循环 select 监听 ctx 和数据
lineCh := make(chan string, 1)
errCh := make(chan error, 1)
go func() {
    for {
        line, err := reader.ReadString('\n')
        if err != nil {
            errCh <- err
            return
        }
        lineCh <- line
    }
}()
```

但这增加了复杂度。**最小改动方案**：在循环顶部加 `select { case <-ctx.Done(): return nil; default: }` 即可。虽然不能中断正在阻塞的 ReadString，但能在下一次循环迭代时及时退出。

---

## P2-1: 文本 Delta 聚合

### 现状

`streamOpenAIToAnthropic()` 每收到一个 OpenAI delta chunk 就立即 emit 一个 Anthropic SSE event + flush。OpenAI 有些模型逐字符流式输出，导致大量极小的 SSE 事件。

### 改动要点

参考 cc-proxy 的 `TextAggregator`（`aggregator.ts`），实现 Go 版本的文本批处理：

```go
type textAggregator struct {
    mu       sync.Mutex
    buf      strings.Builder
    timer    *time.Timer
    interval time.Duration
    flush    func(text string) error
}
```

核心逻辑：
- `Add(text)`: 追加到 buffer，如果没有活跃 timer 则启动一个
- Timer 到期时调用 flush callback 发送累积文本
- 流结束时强制 flush 残留内容

cc-proxy 默认间隔 35ms，可通过环境变量配置。

**注意**：这个改动侵入性较大，需要将 `streamOpenAIToAnthropic()` 中的直接 SSE 写入改为通过 aggregator 中转。建议作为后续优化。

---

## P2-2: 流结束 Token 重算

### 现状

`streamOpenAIToAnthropic()` 中 `outputTokens` 是逐 chunk 调用 `estimateTokens()` 累加的。由于 tokenizer 是上下文敏感的，分段 tokenize 的结果之和 != 整段文本一次性 tokenize 的结果。

### 改动要点

在流式循环中额外累积完整输出文本：

```go
var outputBuffer strings.Builder
// ...
// 在处理 content delta 时：
outputBuffer.WriteString(content)
// ...
// 在流结束后、发送 message_delta 之前：
if outputBuffer.Len() > 0 {
    outputTokens = estimateTokens(outputBuffer.String(), tokenModel)
}
```

cc-proxy 参考（`claude_writer.ts:246-255`）：在 `finish()` 时对完整 `outputBuffer` 重新 tokenize。

改动很小，但能提高 usage 报告的准确性。

---

## cc-proxy 参考文件索引

| 功能 | cc-proxy 文件 | 关键行/函数 |
|------|-------------|------------|
| 图片转换 | `deno-proxy/src/map_claude_to_openai.ts` | L52-58, image block → image_url |
| Thinking hint 注入 | `deno-proxy/src/map_claude_to_openai.ts` | L10 THINKING_HINT, L67-76 injection |
| Reasoning content 处理 | `deno-proxy/src/handle_openai_stream.ts` | L182-186, delta.reasoning_content → feedReasoning() |
| Thinking block 输出 | `deno-proxy/src/claude_writer.ts` | emitThinking(), content_block type="thinking" |
| 文本聚合 | `deno-proxy/src/aggregator.ts` | TextAggregator class, 35ms interval |
| SSE 心跳 | `deno-proxy/src/main.ts` | L435-445, 5s ": keepalive\n\n" |
| 客户端断连 | `deno-proxy/src/main.ts` | L485-489, ReadableStream.cancel() → AbortController |
| Token 重算 | `deno-proxy/src/claude_writer.ts` | L246-255, finish() re-count outputBuffer |
