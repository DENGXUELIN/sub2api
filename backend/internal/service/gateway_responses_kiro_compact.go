package service

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	kiroCompactionPayloadPrefix = "sub2api-kiro-compaction-v1:"
	kiroCompactionSummaryPrompt = `Summarize the conversation so far for a successor assistant. Preserve the user's requests, decisions, constraints, important facts, code changes, errors, commands, file paths, and unfinished work. Keep the summary concise but complete enough to continue without the discarded assistant and tool history. Do not answer the user or add commentary; output only the summary.`
)

func isKiroRemoteCompactionV2Request(c *gin.Context, body []byte) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return IsOpenAIRemoteCompactionV2Request(body, c.GetHeader("x-codex-beta-features"))
}

func IsOpenAIRemoteCompactionV2Request(body []byte, betaFeatures string) bool {
	featureEnabled := false
	for _, feature := range strings.Split(betaFeatures, ",") {
		if strings.TrimSpace(feature) == "remote_compaction_v2" {
			featureEnabled = true
			break
		}
	}
	if !featureEnabled || !bytes.Contains(body, []byte("compaction_trigger")) {
		return false
	}
	return gjson.GetBytes(body, "stream").Bool() && HasCompactionTriggerInInput(body)
}

func hasKiroCompactionPayload(body []byte) bool {
	return bytes.Contains(body, []byte(kiroCompactionPayloadPrefix))
}

func shouldPrepareKiroResponsesBody(body []byte, compact bool) bool {
	return compact || hasKiroCompactionPayload(body)
}

func (s *GatewayService) prepareKiroResponsesBody(body []byte, compact bool) ([]byte, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Kiro Responses body: %w", err)
	}

	input, ok := payload["input"].([]any)
	if !ok {
		return body, nil
	}

	secret := ""
	if s != nil && s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	prepared := make([]any, 0, len(input)+1)
	changed := false
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			prepared = append(prepared, raw)
			continue
		}
		typ := strings.TrimSpace(stringValue(item["type"]))
		if typ == "compaction_trigger" {
			if compact {
				changed = true
				continue
			}
			prepared = append(prepared, raw)
			continue
		}
		if isKiroCompactionItemType(typ) {
			if encrypted := strings.TrimSpace(stringValue(item["encrypted_content"])); encrypted != "" {
				if summary, owned, err := decodeKiroCompactionSummary(encrypted, secret); err != nil {
					return nil, err
				} else if owned {
					changed = true
					prepared = append(prepared, kiroSummaryInputItem(summary))
					continue
				}
			}
		}
		prepared = append(prepared, raw)
	}
	if compact {
		changed = true
		prepared = append(prepared, map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": kiroCompactionSummaryPrompt,
			}},
		})
		delete(payload, "tools")
		delete(payload, "tool_choice")
		delete(payload, "parallel_tool_calls")
		delete(payload, "reasoning")
		delete(payload, "text")
		delete(payload, "include")
		delete(payload, "store")
		if _, exists := payload["max_output_tokens"]; !exists {
			payload["max_output_tokens"] = 8192
		}
	}
	if !changed {
		return body, nil
	}
	payload["input"] = prepared
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Kiro Responses body: %w", err)
	}
	return encoded, nil
}

func kiroSummaryInputItem(summary string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{map[string]any{
			"type": "input_text",
			"text": "<conversation_summary>\n" + summary + "\n</conversation_summary>",
		}},
	}
}

func isKiroCompactionItemType(value string) bool {
	switch strings.TrimSpace(value) {
	case "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

func kiroCompactionCipherKey(secret string) []byte {
	sum := sha256.Sum256([]byte("sub2api/kiro/remote-compaction/v1\x00" + secret))
	return sum[:]
}

func encodeKiroCompactionSummary(summary, secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("Kiro compaction requires a configured JWT secret")
	}
	block, err := aes.NewCipher(kiroCompactionCipherKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate Kiro compaction nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(summary), []byte(kiroCompactionPayloadPrefix))
	return kiroCompactionPayloadPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func decodeKiroCompactionSummary(value, secret string) (summary string, owned bool, err error) {
	if !strings.HasPrefix(value, kiroCompactionPayloadPrefix) {
		return "", false, nil
	}
	if strings.TrimSpace(secret) == "" {
		return "", true, fmt.Errorf("Kiro compaction payload cannot be decrypted without JWT secret")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, kiroCompactionPayloadPrefix))
	if err != nil {
		return "", true, fmt.Errorf("decode Kiro compaction payload: %w", err)
	}
	block, err := aes.NewCipher(kiroCompactionCipherKey(secret))
	if err != nil {
		return "", true, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", true, err
	}
	if len(raw) < gcm.NonceSize() {
		return "", true, fmt.Errorf("Kiro compaction payload is truncated")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(kiroCompactionPayloadPrefix))
	if err != nil {
		return "", true, fmt.Errorf("decrypt Kiro compaction payload: %w", err)
	}
	return string(plain), true, nil
}

func (s *GatewayService) handleKiroResponsesCompactStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	mappedModel string,
	startTime time.Time,
) (*ForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	var usage ClaudeUsage
	var summary strings.Builder
	var firstTokenMs *int

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		line := scanner.Text()
		_, ok := parseAnthropicSSEField(line, "event")
		if !ok {
			continue
		}
		if !scanner.Scan() {
			break
		}
		payload, ok := parseAnthropicSSEField(scanner.Text(), "data")
		if !ok {
			continue
		}
		var event apicompat.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		mergeKiroCreditsFromAnthropicPayload(&usage, payload)
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				mergeAnthropicUsage(&usage, event.Message.Usage)
			}
		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "text" && event.ContentBlock.Text != "" {
				if firstTokenMs == nil {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
				summary.WriteString(event.ContentBlock.Text)
			}
		case "content_block_delta":
			if event.Delta != nil && event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				if firstTokenMs == nil {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
				summary.WriteString(event.Delta.Text)
			}
		case "message_delta":
			if event.Usage != nil {
				mergeAnthropicUsage(&usage, *event.Usage)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Kiro compact stream: %w", err)
	}

	text := strings.TrimSpace(summary.String())
	if text == "" {
		return nil, fmt.Errorf("Kiro compact stream ended without a summary")
	}
	secret := ""
	if s != nil && s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	encrypted, err := encodeKiroCompactionSummary(text, secret)
	if err != nil {
		return nil, err
	}
	totalInput := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	response := map[string]any{
		"id":     "resp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"object": "response",
		"model":  originalModel,
		"status": "completed",
		"output": []any{map[string]any{
			"id":                "cmp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			"type":              "compaction",
			"status":            "completed",
			"encrypted_content": encrypted,
		}},
		"usage": map[string]any{
			"input_tokens":  totalInput,
			"output_tokens": usage.OutputTokens,
			"total_tokens":  totalInput + usage.OutputTokens,
		},
	}
	if usage.CacheReadInputTokens > 0 {
		response["usage"].(map[string]any)["input_tokens_details"] = map[string]any{"cached_tokens": usage.CacheReadInputTokens}
	}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode Kiro compact response: %w", err)
	}

	if !writeOpenAICompactSSEBridge(c, http.StatusOK, encodedResponse) {
		c.Data(http.StatusOK, "application/json; charset=utf-8", encodedResponse)
	}
	return &ForwardResult{
		RequestID:     requestID,
		Usage:         usage,
		Model:         originalModel,
		UpstreamModel: mappedModel,
		Stream:        true,
		Duration:      time.Since(startTime),
		FirstTokenMs:  firstTokenMs,
	}, nil
}
