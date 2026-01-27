package proxy

import (
	"net/http"

	"gpt-load/internal/models"
)

func shouldApplyAnthropicCompat(group *models.Group, method string, path string) bool {
	if group == nil {
		return false
	}
	return group.ChannelType == "openai" &&
		group.AnthropicCompat &&
		method == http.MethodPost &&
		path == "/v1/messages"
}

func shouldApplyToolcallCompat(group *models.Group, method string, effectivePath string) bool {
	if group == nil {
		return false
	}
	return group.ChannelType == "openai" &&
		group.ToolcallCompat &&
		method == http.MethodPost &&
		effectivePath == "/v1/chat/completions"
}
