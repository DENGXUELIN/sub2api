package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestApplyKiroContextGuardLeavesNormalRequestUntouched(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)

	guarded, result, err := applyKiroContextGuard(context.Background(), body)

	require.NoError(t, err)
	require.False(t, result.Applied)
	require.Equal(t, body, guarded)
}

func TestApplyKiroContextGuardCompactsOversizedReplay(t *testing.T) {
	t.Parallel()
	body := buildOversizedKiroMessagesBody(t, false)

	guarded, result, err := applyKiroContextGuard(context.Background(), body)

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.GreaterOrEqual(t, result.TokensBefore, kiroContextGuardTriggerTokens)
	require.Less(t, result.TokensAfter, kiroContextGuardTriggerTokens)
	require.Less(t, result.MessagesAfter, result.MessagesBefore)
	require.Greater(t, result.MessagesFolded, 0)
	require.Contains(t, gjson.GetBytes(guarded, "messages.0.content.0.text").String(), "<conversation_summary>")
	lastMessagePath := fmt.Sprintf("messages.%d.content", result.MessagesAfter-1)
	require.Equal(t, "latest request must survive", gjson.GetBytes(guarded, lastMessagePath).String())
	require.Equal(t, "claude-opus-5", gjson.GetBytes(guarded, "model").String())
}

func TestApplyKiroContextGuardCoversClaudeCodeModelFamilies(t *testing.T) {
	t.Parallel()
	body := buildOversizedKiroMessagesBody(t, false)

	for _, model := range []string{
		"claude-opus-5",
		"claude-sonnet-5",
		"claude-haiku-4-5-20251001",
		"claude-code-future-model",
	} {
		model := model
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			modelBody, err := sjson.SetBytes(body, "model", model)
			require.NoError(t, err)

			guarded, result, err := applyKiroContextGuard(context.Background(), modelBody)

			require.NoError(t, err)
			require.True(t, result.Applied)
			require.Less(t, result.TokensAfter, kiroContextGuardTriggerTokens)
			require.Equal(t, model, gjson.GetBytes(guarded, "model").String())
		})
	}
}

func TestApplyKiroContextGuardAccountsForLargeSystemEnvelope(t *testing.T) {
	t.Parallel()
	body := buildOversizedKiroMessagesBody(t, false)
	body, err := sjson.SetBytes(body, "system", strings.Repeat("system context ", 60_000))
	require.NoError(t, err)

	guarded, result, err := applyKiroContextGuard(context.Background(), body)

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Less(t, result.TokensAfter, result.TokensBefore)
	require.Less(t, result.MessagesAfter, result.MessagesBefore)
	require.Equal(t, "latest request must survive", gjson.GetBytes(guarded, fmt.Sprintf("messages.%d.content", result.MessagesAfter-1)).String())
}

func TestApplyKiroContextGuardKeepsCrossBoundaryToolPair(t *testing.T) {
	t.Parallel()
	body := buildOversizedKiroMessagesBody(t, true)

	guarded, result, err := applyKiroContextGuard(context.Background(), body)

	require.NoError(t, err)
	require.True(t, result.Applied)
	messages := gjson.GetBytes(guarded, "messages").Array()
	var toolUseSeen, toolResultSeen bool
	for _, message := range messages {
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "tool_use":
				toolUseSeen = toolUseSeen || block.Get("id").String() == "toolu_boundary"
			case "tool_result":
				toolResultSeen = toolResultSeen || block.Get("tool_use_id").String() == "toolu_boundary"
			}
		}
	}
	require.Equal(t, toolUseSeen, toolResultSeen, "tool pair must never be split at the retained boundary")
	require.True(t, toolUseSeen, "cross-boundary tool_use must be restored as a compact prelude")
	require.True(t, toolResultSeen, "retained tool_result must survive compaction")
}

func TestCompactKiroContinuationTextKeepsValidUTF8(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("\u524d\u6bb5\u4fe1\u606f", 100) + strings.Repeat("tail", 100)
	compact := compactKiroContinuationText(text, 127)
	require.True(t, json.Valid([]byte(fmt.Sprintf("%q", compact))))
	require.LessOrEqual(t, len(compact), 127)
}

func buildOversizedKiroMessagesBody(t *testing.T, includeBoundaryToolPair bool) []byte {
	t.Helper()
	messages := make([]apicompat.AnthropicMessage, 0, 143)
	for i := 0; i < 140; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		content, err := json.Marshal(strings.Repeat(fmt.Sprintf("message-%03d payload ", i), 420))
		require.NoError(t, err)
		messages = append(messages, apicompat.AnthropicMessage{Role: role, Content: content})
	}
	if includeBoundaryToolPair {
		messages[25] = apicompat.AnthropicMessage{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"toolu_boundary","name":"Read","input":{"file_path":"main.go"}}]`)}
		messages[100] = apicompat.AnthropicMessage{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_boundary","content":"ok"}]`)}
	}
	latest, err := json.Marshal("latest request must survive")
	require.NoError(t, err)
	messages = append(messages, apicompat.AnthropicMessage{Role: "user", Content: latest})
	payload := map[string]any{
		"model":      "claude-opus-5",
		"stream":     true,
		"max_tokens": 8192,
		"system":     strings.Repeat("stable system prompt ", 200),
		"messages":   messages,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	return body
}
