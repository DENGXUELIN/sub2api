package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Kiro currently accepts a smaller effective prompt than newer Claude Code
// clients may advertise for the selected model. The client therefore may not
// compact before Kiro rejects the request. Keep enough headroom for Kiro's own
// system/tool expansion and output budget. This guard is protocol-scoped to the
// Kiro /v1/messages path, so it covers every model used through Claude Code.
const (
	kiroContextGuardTriggerTokens = 176_000
	kiroContextGuardTailTokens    = 120_000
	kiroContextGuardSummaryBytes  = 16 * 1024
)

type kiroContextGuardResult struct {
	Applied        bool
	TokensBefore   int
	TokensAfter    int
	MessagesBefore int
	MessagesAfter  int
	MessagesFolded int
}

// applyKiroContextGuard reduces an oversized stateless /v1/messages replay
// before it reaches Kiro. The retained boundary is expanded so a tool_result is
// never separated from its corresponding tool_use. A bounded continuation note
// keeps the beginning and end of the discarded section available to the model.
func applyKiroContextGuard(ctx context.Context, body []byte) ([]byte, kiroContextGuardResult, error) {
	result := kiroContextGuardResult{TokensBefore: estimateKiroInputTokens(ctx, body)}
	if result.TokensBefore < kiroContextGuardTriggerTokens {
		return body, result, nil
	}

	messagesResult := gjson.GetBytes(body, "messages")
	if !messagesResult.IsArray() {
		return body, result, nil
	}
	rawMessages := messagesResult.Array()
	result.MessagesBefore = len(rawMessages)
	if len(rawMessages) < 4 {
		return body, result, nil
	}

	messages := make([]apicompat.AnthropicMessage, 0, len(rawMessages))
	messageTokens := make([]int, 0, len(rawMessages))
	for _, raw := range rawMessages {
		var message apicompat.AnthropicMessage
		if err := json.Unmarshal([]byte(raw.Raw), &message); err != nil {
			return body, result, fmt.Errorf("decode Kiro context message: %w", err)
		}
		messages = append(messages, message)
		oneMessageBody, err := sjson.SetRawBytes([]byte(`{"messages":[]}`), "messages", []byte("["+raw.Raw+"]"))
		if err != nil {
			return body, result, fmt.Errorf("estimate Kiro context message: %w", err)
		}
		messageTokens = append(messageTokens, max(estimateKiroInputTokens(ctx, oneMessageBody), 1))
	}

	emptyMessagesBody, err := sjson.SetRawBytes(body, "messages", []byte(`[]`))
	if err != nil {
		return body, result, fmt.Errorf("prepare Kiro context envelope: %w", err)
	}
	budget := kiroContextGuardTailTokens - estimateKiroInputTokens(ctx, emptyMessagesBody)
	if budget < 1 {
		// The envelope already consumes the tail budget. Keep the newest
		// message as the minimum viable continuation and avoid retaining
		// additional history that cannot fit beside the envelope.
		budget = 1
	}

	start := len(messages) - 1
	used := 0
	for start >= 0 {
		next := used + messageTokens[start]
		if next > budget && start < len(messages)-1 {
			break
		}
		used = next
		start--
	}
	start++
	start, toolPrelude := resolveKiroContextGuardBoundary(messages, messageTokens, start, used, budget)
	if start <= 0 {
		return body, result, nil
	}

	summary := buildKiroContextContinuation(messages[:start], kiroContextGuardSummaryBytes)
	retained := make([]apicompat.AnthropicMessage, 0, len(messages)-start+len(toolPrelude)+1)
	if summary != "" {
		content, marshalErr := json.Marshal([]apicompat.AnthropicContentBlock{{Type: "text", Text: summary}})
		if marshalErr != nil {
			return body, result, fmt.Errorf("encode Kiro context continuation: %w", marshalErr)
		}
		retained = append(retained, apicompat.AnthropicMessage{Role: "user", Content: content})
	}
	retained = append(retained, toolPrelude...)
	retained = append(retained, messages[start:]...)
	encodedMessages, err := json.Marshal(retained)
	if err != nil {
		return body, result, fmt.Errorf("encode guarded Kiro messages: %w", err)
	}
	guarded, err := sjson.SetRawBytes(body, "messages", encodedMessages)
	if err != nil {
		return body, result, fmt.Errorf("replace guarded Kiro messages: %w", err)
	}

	result.Applied = true
	result.MessagesFolded = start
	result.MessagesAfter = len(retained)
	result.TokensAfter = estimateKiroInputTokens(ctx, guarded)
	return guarded, result, nil
}

func resolveKiroContextGuardBoundary(messages []apicompat.AnthropicMessage, messageTokens []int, start, retainedTokens, budget int) (int, []apicompat.AnthropicMessage) {
	if start <= 0 || start >= len(messages) {
		return start, nil
	}
	expanded := expandAnthropicCompatTrimBoundary(messages, start)
	if expanded == start {
		return start, nil
	}
	expandedTokens := retainedTokens
	for i := expanded; i < start && i < len(messageTokens); i++ {
		expandedTokens += messageTokens[i]
	}
	if expandedTokens <= budget {
		return expanded, nil
	}

	// A very old tool_use paired with a retained tool_result can pull tens of
	// thousands of unrelated history back into the request. Preserve only the
	// matching tool_use blocks as a compact assistant prelude.
	return start, buildKiroCrossBoundaryToolPrelude(messages, start)
}

func buildKiroCrossBoundaryToolPrelude(messages []apicompat.AnthropicMessage, start int) []apicompat.AnthropicMessage {
	if start <= 0 || start >= len(messages) {
		return nil
	}
	retainedResultIDs := make(map[string]struct{})
	for _, message := range messages[start:] {
		_, results := anthropicCompatMessageToolIDs(message)
		for _, id := range results {
			retainedResultIDs[id] = struct{}{}
		}
	}
	if len(retainedResultIDs) == 0 {
		return nil
	}

	blocks := make([]json.RawMessage, 0, len(retainedResultIDs))
	seen := make(map[string]struct{})
	for _, message := range messages[:start] {
		var content []json.RawMessage
		if err := json.Unmarshal(message.Content, &content); err != nil {
			continue
		}
		for _, block := range content {
			parsed := gjson.ParseBytes(block)
			if parsed.Get("type").String() != "tool_use" {
				continue
			}
			id := parsed.Get("id").String()
			if _, wanted := retainedResultIDs[id]; !wanted {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			blocks = append(blocks, append(json.RawMessage(nil), block...))
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	content, err := json.Marshal(blocks)
	if err != nil {
		return nil
	}
	return []apicompat.AnthropicMessage{{Role: "assistant", Content: content}}
}

func buildKiroContextContinuation(messages []apicompat.AnthropicMessage, maxBytes int) string {
	if len(messages) == 0 || maxBytes <= 0 {
		return ""
	}
	var transcript strings.Builder
	for _, message := range messages {
		text := extractKiroContinuationContent(message.Content)
		if text == "" {
			continue
		}
		_, _ = fmt.Fprintf(&transcript, "%s: %s\n", strings.ToUpper(strings.TrimSpace(message.Role)), text)
	}
	text := strings.TrimSpace(transcript.String())
	if text == "" {
		return ""
	}
	prefix := "<conversation_summary>\nEarlier conversation was compacted by the gateway because the Kiro context limit was reached. Preserve these requests, decisions, paths, errors, and unfinished work when continuing:\n"
	suffix := "\n</conversation_summary>"
	available := maxBytes - len(prefix) - len(suffix)
	if available < 256 {
		return ""
	}
	text = compactKiroContinuationText(text, available)
	return prefix + text + suffix
}

func extractKiroContinuationContent(raw json.RawMessage) string {
	value := gjson.ParseBytes(raw)
	if value.Type == gjson.String {
		return strings.Join(strings.Fields(value.String()), " ")
	}
	if !value.IsArray() {
		return ""
	}
	parts := make([]string, 0, len(value.Array()))
	for _, block := range value.Array() {
		switch block.Get("type").String() {
		case "text":
			parts = append(parts, block.Get("text").String())
		case "thinking":
			// Thinking is intentionally omitted; it is large and not required to
			// continue the user's work.
		case "tool_use":
			parts = append(parts, fmt.Sprintf("[tool_use %s %s]", block.Get("name").String(), block.Get("input").Raw))
		case "tool_result":
			content := block.Get("content")
			if content.Type == gjson.String {
				parts = append(parts, fmt.Sprintf("[tool_result %s] %s", block.Get("tool_use_id").String(), content.String()))
			} else {
				parts = append(parts, fmt.Sprintf("[tool_result %s] %s", block.Get("tool_use_id").String(), content.Raw))
			}
		}
	}
	return strings.Join(strings.Fields(strings.Join(parts, " \n")), " ")
}

func compactKiroContinuationText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	marker := "\n...[middle of compacted history omitted]...\n"
	headLimit := (limit - len(marker)) / 2
	tailLimit := limit - len(marker) - headLimit
	head := validUTF8Prefix(text, headLimit)
	tail := validUTF8Suffix(text, tailLimit)
	return strings.TrimSpace(head) + marker + strings.TrimSpace(tail)
}

func validUTF8Prefix(value string, limit int) string {
	if limit >= len(value) {
		return value
	}
	if limit <= 0 {
		return ""
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

func validUTF8Suffix(value string, limit int) string {
	if limit >= len(value) {
		return value
	}
	if limit <= 0 {
		return ""
	}
	start := len(value) - limit
	for start < len(value) && !utf8.ValidString(value[start:]) {
		start++
	}
	return value[start:]
}
