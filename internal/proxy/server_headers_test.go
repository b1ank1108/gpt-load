package proxy

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

type noopTransformer struct{}

func (noopTransformer) HandleSuccess(_ *gin.Context, _ *http.Response, _ bool) error { return nil }
func (noopTransformer) HandleUpstreamError(_ *gin.Context, _ int, _ []byte) bool     { return false }

func TestApplyTransformerRequestHeaders_LeavesHeaderWhenNoTransformer(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept-Encoding", "gzip")

	applyTransformerRequestHeaders(req, nil)
	if got := req.Header.Get("Accept-Encoding"); got != "gzip" {
		t.Fatalf("expected Accept-Encoding unchanged, got %q", got)
	}
}

func TestApplyTransformerRequestHeaders_ForcesIdentityWhenTransformerActive(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept-Encoding", "gzip, br")

	applyTransformerRequestHeaders(req, noopTransformer{})
	if got := req.Header.Get("Accept-Encoding"); got != "identity" {
		t.Fatalf("expected Accept-Encoding=identity, got %q", got)
	}
}

func TestApplyTransformerRequestHeaders_NilRequestSafe(t *testing.T) {
	applyTransformerRequestHeaders(nil, noopTransformer{})
}
