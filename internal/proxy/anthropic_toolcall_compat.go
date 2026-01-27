package proxy

import (
	"fmt"
	"io"
	"net/http"

	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
)

type anthropicCompatWithToolcallCompatTransformer struct {
	base   *anthropicCompatTransformer
	idSeed string
}

func newAnthropicCompatWithToolcallCompatTransformer(base *anthropicCompatTransformer, idSeed string) *anthropicCompatWithToolcallCompatTransformer {
	return &anthropicCompatWithToolcallCompatTransformer{
		base:   base,
		idSeed: idSeed,
	}
}

func (t *anthropicCompatWithToolcallCompatTransformer) HandleUpstreamError(c *gin.Context, statusCode int, upstreamBody []byte) bool {
	return t.base.HandleUpstreamError(c, statusCode, upstreamBody)
}

func (t *anthropicCompatWithToolcallCompatTransformer) HandleSuccess(c *gin.Context, resp *http.Response, isStream bool) error {
	if isStream {
		pipeReader, pipeWriter := io.Pipe()
		errCh := make(chan error, 1)

		go func() {
			defer close(errCh)
			err := transformOpenAIStreamToolcallCompat(resp.Body, openAISSEEmitter{w: pipeWriter}, t.idSeed)
			if err != nil {
				_ = pipeWriter.CloseWithError(err)
				errCh <- err
				return
			}
			_ = pipeWriter.Close()
			errCh <- nil
		}()

		fakeResp := &http.Response{
			StatusCode: resp.StatusCode,
			Body:       pipeReader,
		}

		streamErr := t.base.HandleSuccess(c, fakeResp, true)
		_ = pipeReader.Close()
		transformErr := <-errCh

		if streamErr != nil {
			return streamErr
		}
		return transformErr
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read upstream body: %w", err)
	}

	decompressed, err := utils.DecompressResponse(resp.Header.Get("Content-Encoding"), bodyBytes)
	if err != nil {
		decompressed = bodyBytes
	}

	toConvert := decompressed
	if restored, ok := restoreToolCallsInChatCompletion(decompressed, t.idSeed); ok {
		toConvert = restored
	}

	converted, err := convertOpenAIChatCompletionToAnthropic(toConvert, t.base.requestedModel)
	if err != nil {
		return err
	}

	c.Header("Content-Type", "application/json")
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(converted)
	return nil
}
