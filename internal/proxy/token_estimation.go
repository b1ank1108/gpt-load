package proxy

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

var (
	tiktokenInitOnce   sync.Once
	tiktokenByEncoding sync.Map // map[string]*tiktoken.Tiktoken
)

func initTiktokenOfflineLoader() {
	tiktokenInitOnce.Do(func() {
		tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
	})
}

func encodingForModel(model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return tiktoken.MODEL_CL100K_BASE
	}
	if enc, ok := tiktoken.MODEL_TO_ENCODING[m]; ok {
		return enc
	}
	for prefix, enc := range tiktoken.MODEL_PREFIX_TO_ENCODING {
		if strings.HasPrefix(m, prefix) {
			return enc
		}
	}
	return tiktoken.MODEL_CL100K_BASE
}

func tiktokenForModel(model string) (*tiktoken.Tiktoken, bool) {
	initTiktokenOfflineLoader()

	encName := encodingForModel(model)
	if v, ok := tiktokenByEncoding.Load(encName); ok {
		if tk, ok := v.(*tiktoken.Tiktoken); ok && tk != nil {
			return tk, true
		}
	}

	tk, err := tiktoken.GetEncoding(encName)
	if err != nil || tk == nil {
		if encName != tiktoken.MODEL_CL100K_BASE {
			if v, ok := tiktokenByEncoding.Load(tiktoken.MODEL_CL100K_BASE); ok {
				if tk, ok := v.(*tiktoken.Tiktoken); ok && tk != nil {
					return tk, true
				}
			}
			tk, err = tiktoken.GetEncoding(tiktoken.MODEL_CL100K_BASE)
			if err != nil || tk == nil {
				return nil, false
			}
		} else {
			return nil, false
		}
	}

	tiktokenByEncoding.Store(encName, tk)
	return tk, true
}

func fallbackTokenEstimate(text string) int {
	if text == "" {
		return 0
	}
	n := len([]byte(text))
	return (n + 3) / 4
}

func estimateTokens(text string, model string) (tokens int) {
	fallback := fallbackTokenEstimate(text)
	if text == "" {
		return 0
	}

	tokens = fallback
	defer func() {
		if recover() != nil {
			tokens = fallback
		}
	}()

	tk, ok := tiktokenForModel(model)
	if !ok {
		return fallback
	}
	return len(tk.Encode(text, nil, nil))
}

func estimateOpenAIRequestInputTokens(body []byte, model string) (tokens int) {
	tokens, _ = estimateOpenAIRequestInputTokensAndModel(body, model)
	return tokens
}

func estimateOpenAIRequestInputTokensAndModel(body []byte, model string) (tokens int, effectiveModel string) {
	defer func() {
		if recover() != nil {
			tokens = 0
			effectiveModel = ""
		}
	}()

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, ""
	}

	effectiveModel = strings.TrimSpace(model)
	if m, ok := payload["model"].(string); ok && strings.TrimSpace(m) != "" {
		effectiveModel = m
	}

	msgs, _ := payload["messages"].([]any)
	if len(msgs) == 0 {
		return 0, effectiveModel
	}

	total := 0
	for _, item := range msgs {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content := msg["content"]
		if content == nil {
			continue
		}
		switch v := content.(type) {
		case string:
			total += estimateTokens(v, effectiveModel)
		default:
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			total += estimateTokens(string(b), effectiveModel)
		}
	}
	return total, effectiveModel
}
