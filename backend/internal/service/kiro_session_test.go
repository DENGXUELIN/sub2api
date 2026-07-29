//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildKiroPayloadForAccountUsesStableConversationIDs(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{ID: 40, Credentials: map[string]any{"profile_arn": "profile-a"}}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello","additional_kwargs":{"conversationId":"client-conv","continuationId":"client-cont"}}]}`)

	first, err := svc.buildKiroPayloadForAccount(context.Background(), account, nil, body, "claude-sonnet-4.5", "token", "claude-sonnet-4-5", nil)
	require.NoError(t, err)
	second, err := svc.buildKiroPayloadForAccount(context.Background(), account, nil, body, "claude-sonnet-4.5", "token", "claude-sonnet-4-5", nil)
	require.NoError(t, err)

	firstConversationID := gjson.GetBytes(first.Payload, "conversationState.conversationId").String()
	secondConversationID := gjson.GetBytes(second.Payload, "conversationState.conversationId").String()
	firstContinuationID := gjson.GetBytes(first.Payload, "conversationState.agentContinuationId").String()
	secondContinuationID := gjson.GetBytes(second.Payload, "conversationState.agentContinuationId").String()
	require.NotEmpty(t, firstConversationID)
	require.NotEmpty(t, secondConversationID)
	require.Equal(t, firstConversationID, secondConversationID)
	require.NotEqual(t, "client-conv", firstConversationID)
	require.NotEmpty(t, firstContinuationID)
	require.Equal(t, firstContinuationID, secondContinuationID)
	require.NotEqual(t, firstConversationID, firstContinuationID)
}

func TestBuildKiroPayloadForAccountCanDisableStableAgentContinuationID(t *testing.T) {
	t.Setenv("SUB2API_KIRO_AGENT_CONTINUATION_ID_MODE", "off")
	svc := &GatewayService{}
	account := &Account{ID: 40, Credentials: map[string]any{"profile_arn": "profile-a"}}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)

	result, err := svc.buildKiroPayloadForAccount(context.Background(), account, nil, body, "claude-sonnet-4.5", "token", "claude-sonnet-4-5", nil)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(result.Payload, "conversationState.agentContinuationId").Exists())
}

func TestBuildKiroPayloadForAccountReplaysFullMessagesIntoHistory(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{ID: 40, Credentials: map[string]any{"profile_arn": "profile-a"}}
	body := []byte(`{
		"model":"claude-sonnet-4-5",
		"system":"system prompt",
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"second"}
		]
	}`)

	result, err := svc.buildKiroPayloadForAccount(context.Background(), account, nil, body, "claude-sonnet-4.5", "token", "claude-sonnet-4-5", nil)
	require.NoError(t, err)

	history := gjson.GetBytes(result.Payload, "conversationState.history").Array()
	require.Len(t, history, 4)
	require.Contains(t, history[0].Get("userInputMessage.content").String(), "system prompt")
	require.Equal(t, "first", history[2].Get("userInputMessage.content").String())
	require.Equal(t, "answer", history[3].Get("assistantResponseMessage.content").String())
	require.Equal(t, "second", gjson.GetBytes(result.Payload, "conversationState.currentMessage.userInputMessage.content").String())
}

func TestBuildKiroPayloadForAccountFreezesSystemPromptBySessionAndPrefix(t *testing.T) {
	resetKiroSystemPromptCacheForTest()
	t.Cleanup(resetKiroSystemPromptCacheForTest)

	svc := &GatewayService{}
	account := &Account{ID: 40, Credentials: map[string]any{"profile_arn": "profile-a"}}
	parsed := &ParsedRequest{ExplicitSessionID: "session-main"}
	prefix := strings.Repeat("stable agent prompt ", 40)
	buildBody := func(system, user string) []byte {
		body, err := json.Marshal(map[string]any{
			"model":    "claude-sonnet-4-5",
			"system":   system,
			"messages": []map[string]any{{"role": "user", "content": user}},
		})
		require.NoError(t, err)
		return body
	}

	first, err := svc.buildKiroPayloadForAccount(context.Background(), account, parsed, buildBody(prefix+"first tail", "first"), "claude-sonnet-4.5", "token", "claude-sonnet-4-5", nil)
	require.NoError(t, err)
	second, err := svc.buildKiroPayloadForAccount(context.Background(), account, parsed, buildBody(prefix+"changed tail", "second"), "claude-sonnet-4.5", "token", "claude-sonnet-4-5", nil)
	require.NoError(t, err)

	firstSystem := gjson.GetBytes(first.Payload, "conversationState.history.0.userInputMessage.content").String()
	secondSystem := gjson.GetBytes(second.Payload, "conversationState.history.0.userInputMessage.content").String()
	require.Contains(t, firstSystem, "first tail")
	require.NotContains(t, secondSystem, "changed tail")
	require.Equal(t, firstSystem, secondSystem)
	require.Equal(t, "second", gjson.GetBytes(second.Payload, "conversationState.currentMessage.userInputMessage.content").String())
}

func TestBuildKiroPayloadForAccountIsolatesAuxiliaryPromptFromMainPrompt(t *testing.T) {
	resetKiroSystemPromptCacheForTest()
	t.Cleanup(resetKiroSystemPromptCacheForTest)

	svc := &GatewayService{}
	account := &Account{ID: 40, Credentials: map[string]any{"profile_arn": "profile-a"}}
	parsed := &ParsedRequest{ExplicitSessionID: "session-shared-with-title"}
	prefix := strings.Repeat("stable main prompt ", 40)
	build := func(system string) *kiroPayloadForTest {
		body, err := json.Marshal(map[string]any{
			"model":    "claude-sonnet-4-5",
			"system":   system,
			"messages": []map[string]any{{"role": "user", "content": "hello"}},
		})
		require.NoError(t, err)
		result, err := svc.buildKiroPayloadForAccount(context.Background(), account, parsed, body, "claude-sonnet-4.5", "token", "claude-sonnet-4-5", nil)
		require.NoError(t, err)
		return &kiroPayloadForTest{payload: result.Payload}
	}

	mainFirst := build(prefix + "first tail").systemPrompt(t)
	auxiliary := build("Generate a concise title for this conversation.").systemPrompt(t)
	mainSecond := build(prefix + "changed tail").systemPrompt(t)

	require.NotEqual(t, mainFirst, auxiliary)
	require.Equal(t, mainFirst, mainSecond)
	require.Contains(t, auxiliary, "Generate a concise title")
}

func TestBuildKiroPayloadForAccountCanDisableSystemPromptFreeze(t *testing.T) {
	t.Setenv("SUB2API_KIRO_SYSTEM_PROMPT_FREEZE_MODE", "off")
	resetKiroSystemPromptCacheForTest()
	t.Cleanup(resetKiroSystemPromptCacheForTest)

	svc := &GatewayService{}
	account := &Account{ID: 40, Credentials: map[string]any{"profile_arn": "profile-a"}}
	parsed := &ParsedRequest{ExplicitSessionID: "session-no-freeze"}
	prefix := strings.Repeat("stable prompt ", 50)
	build := func(system string) []byte {
		body, err := json.Marshal(map[string]any{
			"model":    "claude-sonnet-4-5",
			"system":   system,
			"messages": []map[string]any{{"role": "user", "content": "hello"}},
		})
		require.NoError(t, err)
		result, err := svc.buildKiroPayloadForAccount(context.Background(), account, parsed, body, "claude-sonnet-4.5", "token", "claude-sonnet-4-5", nil)
		require.NoError(t, err)
		return result.Payload
	}

	first := gjson.GetBytes(build(prefix+"first tail"), "conversationState.history.0.userInputMessage.content").String()
	second := gjson.GetBytes(build(prefix+"changed tail"), "conversationState.history.0.userInputMessage.content").String()
	require.NotEqual(t, first, second)
	require.Contains(t, second, "changed tail")
}

func TestKiroSystemPromptVariantNormalizesBillingHashAndToolOrder(t *testing.T) {
	first := []byte(`{
		"system":"x-anthropic-billing-header: cc_version=2; cch=first;",
		"tools":[{"name":"Write"},{"name":"Read"}]
	}`)
	second := []byte(`{
		"system":"x-anthropic-billing-header: cc_version=2; cch=second;",
		"tools":[{"name":"Read"},{"name":"Write"}]
	}`)

	require.Equal(t, kiroSystemPromptVariant(first, nil), kiroSystemPromptVariant(second, nil))
}

type kiroPayloadForTest struct {
	payload []byte
}

func (p *kiroPayloadForTest) systemPrompt(t *testing.T) string {
	t.Helper()
	value := gjson.GetBytes(p.payload, "conversationState.history.0.userInputMessage.content").String()
	require.NotEmpty(t, value)
	return value
}
