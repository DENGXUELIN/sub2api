//go:build unit

package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestKiroRemoteCompactionRequestOnlyUsesTheV2Signal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"tools":[{"type":"function","name":"exec"}],"input":[{"type":"message","role":"user","content":"hello"},{"type":"compaction_trigger"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("x-codex-beta-features", "responses_websockets_v2, remote_compaction_v2")

	require.True(t, isKiroRemoteCompactionV2Request(c, body))
	require.False(t, IsOpenAIRemoteCompactionV2Request(body, ""))
	require.False(t, IsOpenAIRemoteCompactionV2Request(body, "Remote_Compaction_V2"))
	require.False(t, IsOpenAIRemoteCompactionV2Request([]byte(`not-json`), "remote_compaction_v2"))
	svc := &GatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "test-jwt-secret"}}}
	prepared, err := svc.prepareKiroResponsesBody(body, true)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(prepared, `input.#(type=="compaction_trigger")`).Exists())
	require.Contains(t, string(prepared), kiroCompactionSummaryPrompt)
	require.False(t, gjson.GetBytes(prepared, "tools").Exists())

	regular := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	require.False(t, hasKiroCompactionPayload(regular))
	require.False(t, shouldPrepareKiroResponsesBody(regular, false))
	require.True(t, shouldPrepareKiroResponsesBody(regular, true))
	require.False(t, IsOpenAIRemoteCompactionV2Request(regular, "remote_compaction_v2"))
	untouched, err := svc.prepareKiroResponsesBody(regular, false)
	require.NoError(t, err)
	require.Equal(t, regular, untouched)

	unmarkedTrigger, err := svc.prepareKiroResponsesBody(body, false)
	require.NoError(t, err)
	require.Equal(t, body, unmarkedTrigger)
}

func TestKiroCompactionPayloadRoundTripAndOwnership(t *testing.T) {
	payload, err := encodeKiroCompactionSummary("keep this context", "test-jwt-secret")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(payload, kiroCompactionPayloadPrefix))

	decoded, owned, err := decodeKiroCompactionSummary(payload, "test-jwt-secret")
	require.NoError(t, err)
	require.True(t, owned)
	require.Equal(t, "keep this context", decoded)

	_, owned, err = decodeKiroCompactionSummary("native-provider-payload", "test-jwt-secret")
	require.NoError(t, err)
	require.False(t, owned)

	svc := &GatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "test-jwt-secret"}}}
	followUp := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"compaction","encrypted_content":"` + payload + `"},{"type":"message","role":"user","content":"continue"}]}`)
	require.True(t, hasKiroCompactionPayload(followUp))
	require.True(t, shouldPrepareKiroResponsesBody(followUp, false))
	prepared, err := svc.prepareKiroResponsesBody(followUp, false)
	require.NoError(t, err)
	require.Equal(t, "message", gjson.GetBytes(prepared, "input.0.type").String())
	require.Contains(t, gjson.GetBytes(prepared, "input.0.content.0.text").String(), "keep this context")
	require.Equal(t, "continue", gjson.GetBytes(prepared, "input.1.content").String())
}

func TestHandleKiroResponsesCompactStreamingResponseReturnsOneCompactionItem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAICompactClientStream(c)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"kiro-compact-request"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"gpt-5.6-sol","usage":{"input_tokens":12}}}`,
			"",
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"summary from Kiro"}}`,
			"",
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			"",
		}, "\n"))),
	}
	svc := &GatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "test-jwt-secret"}}}
	result, err := svc.handleKiroResponsesCompactStreamingResponse(resp, c, "gpt-5.6-sol", "claude-sonnet-4.5", time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)

	body := recorder.Body.String()
	require.Contains(t, body, "event: response.output_item.done")
	require.Contains(t, body, "event: response.completed")
	var completed gjson.Result
	forEachOpenAISSEDataPayload(body, func(data []byte) {
		if gjson.GetBytes(data, "type").String() == "response.completed" {
			require.Equal(t, int64(1), gjson.GetBytes(data, "response.output.#").Int())
			completed = gjson.GetBytes(data, "response.output.0")
		}
	})
	require.True(t, completed.Exists())
	require.Equal(t, "compaction", completed.Get("type").String())
	encrypted := completed.Get("encrypted_content").String()
	decoded, owned, err := decodeKiroCompactionSummary(encrypted, "test-jwt-secret")
	require.NoError(t, err)
	require.True(t, owned)
	require.Equal(t, "summary from Kiro", decoded)
}

func TestHandleKiroResponsesCompactExplicitEndpointReturnsJSONWithOneCompactionItem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"kiro-explicit-compact-request"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"gpt-5.6-sol","usage":{"input_tokens":12}}}`,
			"",
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"summary from explicit compact"}}`,
			"",
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			"",
		}, "\n"))),
	}
	svc := &GatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "test-jwt-secret"}}}
	result, err := svc.handleKiroResponsesCompactStreamingResponse(resp, c, "gpt-5.6-sol", "claude-sonnet-4.5", time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))

	body := gjson.Parse(recorder.Body.String())
	require.Equal(t, int64(1), body.Get("output.#").Int())
	require.Equal(t, "compaction", body.Get("output.0.type").String())
	decoded, owned, err := decodeKiroCompactionSummary(body.Get("output.0.encrypted_content").String(), "test-jwt-secret")
	require.NoError(t, err)
	require.True(t, owned)
	require.Equal(t, "summary from explicit compact", decoded)
}
