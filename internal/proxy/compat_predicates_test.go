package proxy

import (
	"net/http"
	"testing"

	"gpt-load/internal/models"
)

func TestShouldApplyToolcallCompat(t *testing.T) {
	group := &models.Group{ChannelType: "openai", ToolcallCompat: true}
	if !shouldApplyToolcallCompat(group, http.MethodPost, "/v1/chat/completions") {
		t.Fatalf("expected shouldApplyToolcallCompat=true")
	}
	if shouldApplyToolcallCompat(group, http.MethodGet, "/v1/chat/completions") {
		t.Fatalf("expected false for non-POST method")
	}
	if shouldApplyToolcallCompat(group, http.MethodPost, "/v1/models") {
		t.Fatalf("expected false for non-target path")
	}
	if shouldApplyToolcallCompat(&models.Group{ChannelType: "gemini", ToolcallCompat: true}, http.MethodPost, "/v1/chat/completions") {
		t.Fatalf("expected false for non-openai channel")
	}
	if shouldApplyToolcallCompat(&models.Group{ChannelType: "openai", ToolcallCompat: false}, http.MethodPost, "/v1/chat/completions") {
		t.Fatalf("expected false when toolcall compat disabled")
	}
	if shouldApplyToolcallCompat(nil, http.MethodPost, "/v1/chat/completions") {
		t.Fatalf("expected false for nil group")
	}
}

func TestShouldApplyAnthropicCompat(t *testing.T) {
	group := &models.Group{ChannelType: "openai", AnthropicCompat: true}
	if !shouldApplyAnthropicCompat(group, http.MethodPost, "/v1/messages") {
		t.Fatalf("expected shouldApplyAnthropicCompat=true")
	}
	if shouldApplyAnthropicCompat(group, http.MethodGet, "/v1/messages") {
		t.Fatalf("expected false for non-POST method")
	}
	if shouldApplyAnthropicCompat(group, http.MethodPost, "/v1/chat/completions") {
		t.Fatalf("expected false for non-target path")
	}
	if shouldApplyAnthropicCompat(&models.Group{ChannelType: "gemini", AnthropicCompat: true}, http.MethodPost, "/v1/messages") {
		t.Fatalf("expected false for non-openai channel")
	}
	if shouldApplyAnthropicCompat(&models.Group{ChannelType: "openai", AnthropicCompat: false}, http.MethodPost, "/v1/messages") {
		t.Fatalf("expected false when anthropic compat disabled")
	}
	if shouldApplyAnthropicCompat(nil, http.MethodPost, "/v1/messages") {
		t.Fatalf("expected false for nil group")
	}
}
