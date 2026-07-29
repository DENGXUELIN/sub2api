package service

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	kiroSystemPromptCacheMaxEntries = 256
	kiroSystemPromptCacheMaxBytes   = 32 * 1024 * 1024
	kiroSystemPromptCacheMaxEntry   = 2 * 1024 * 1024
	kiroSystemPromptCacheTTL        = 12 * time.Hour
	kiroSystemPromptPrefixRunes     = 512
	kiroSystemPromptAck             = "I will follow these instructions."
)

type kiroSystemPromptCacheEntry struct {
	content  string
	lastUsed time.Time
}

type kiroSystemPromptFreezeResult struct {
	CacheKeyHash string
	IncomingHash string
	FrozenHash   string
	IncomingLen  int
	FrozenLen    int
	Hit          bool
	Reused       bool
}

var kiroSystemPromptCache = struct {
	sync.Mutex
	entries    map[string]kiroSystemPromptCacheEntry
	totalBytes int
}{entries: make(map[string]kiroSystemPromptCacheEntry)}

func freezeKiroSystemPromptHistory(conversationID string, sourceBody, payload []byte) ([]byte, kiroSystemPromptFreezeResult) {
	var result kiroSystemPromptFreezeResult
	if !kiroSystemPromptFreezeEnabled() || strings.TrimSpace(conversationID) == "" || len(payload) == 0 {
		return payload, result
	}

	// history[0] is only a system prompt when the translator inserted its
	// acknowledgement pair. Never freeze ordinary conversation history.
	ack := strings.TrimSpace(gjson.GetBytes(payload, "conversationState.history.1.assistantResponseMessage.content").String())
	if ack != kiroSystemPromptAck {
		return payload, result
	}
	incoming := gjson.GetBytes(payload, "conversationState.history.0.userInputMessage.content").String()
	if incoming == "" || len(incoming) > kiroSystemPromptCacheMaxEntry {
		return payload, result
	}

	variant := kiroSystemPromptVariant(sourceBody, payload)
	if variant == "" {
		return payload, result
	}
	key := kiroSystemPromptCacheKey(conversationID, variant)
	now := time.Now()
	result.CacheKeyHash = key[:16]
	result.IncomingHash = hashKiroLogString(incoming)
	result.IncomingLen = len(incoming)

	kiroSystemPromptCache.Lock()
	entry, found := kiroSystemPromptCache.entries[key]
	if found && now.Sub(entry.lastUsed) > kiroSystemPromptCacheTTL {
		delete(kiroSystemPromptCache.entries, key)
		kiroSystemPromptCache.totalBytes -= len(entry.content)
		found = false
	}
	if found {
		entry.lastUsed = now
		kiroSystemPromptCache.entries[key] = entry
	} else {
		evictKiroSystemPromptCacheLocked(now, len(incoming))
		kiroSystemPromptCache.entries[key] = kiroSystemPromptCacheEntry{content: incoming, lastUsed: now}
		kiroSystemPromptCache.totalBytes += len(incoming)
		entry = kiroSystemPromptCache.entries[key]
	}
	kiroSystemPromptCache.Unlock()

	result.Hit = found
	result.FrozenHash = hashKiroLogString(entry.content)
	result.FrozenLen = len(entry.content)
	if !found || incoming == entry.content {
		return payload, result
	}

	next, err := sjson.SetBytes(payload, "conversationState.history.0.userInputMessage.content", entry.content)
	if err != nil {
		return payload, result
	}
	result.Reused = true
	return next, result
}

func kiroSystemPromptFreezeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SUB2API_KIRO_SYSTEM_PROMPT_FREEZE_MODE"))) {
	case "off", "false", "0", "none":
		return false
	default:
		return true
	}
}

func kiroSystemPromptCacheKey(conversationID, prefix string) string {
	sum := sha256.Sum256([]byte(conversationID + "\x00" + prefix))
	return hex.EncodeToString(sum[:])
}

func kiroSystemPromptVariant(sourceBody, payload []byte) string {
	systemResult := gjson.GetBytes(sourceBody, "system")
	systemPrompt := extractTextFromSystemRaw([]byte(systemResult.Raw))
	systemPrompt = kiropkg.NormalizeBillingHeader(systemPrompt)
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = gjson.GetBytes(payload, "conversationState.history.0.userInputMessage.content").String()
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return ""
	}

	toolNames := make([]string, 0)
	for _, tool := range gjson.GetBytes(sourceBody, "tools").Array() {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("type").String())
		}
		if name != "" {
			toolNames = append(toolNames, name)
		}
	}
	sort.Strings(toolNames)
	return firstKiroRunes(systemPrompt, kiroSystemPromptPrefixRunes) + "\x00tools:" + strings.Join(toolNames, "\x00")
}

func firstKiroRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for index := range value {
		if count == limit {
			return value[:index]
		}
		count++
	}
	return value
}

func evictKiroSystemPromptCacheLocked(now time.Time, incomingBytes int) {
	for key, entry := range kiroSystemPromptCache.entries {
		if now.Sub(entry.lastUsed) > kiroSystemPromptCacheTTL {
			delete(kiroSystemPromptCache.entries, key)
			kiroSystemPromptCache.totalBytes -= len(entry.content)
		}
	}
	for len(kiroSystemPromptCache.entries) >= kiroSystemPromptCacheMaxEntries ||
		kiroSystemPromptCache.totalBytes+incomingBytes > kiroSystemPromptCacheMaxBytes {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range kiroSystemPromptCache.entries {
			if oldestKey == "" || entry.lastUsed.Before(oldest) {
				oldestKey = key
				oldest = entry.lastUsed
			}
		}
		if oldestKey == "" {
			break
		}
		entry := kiroSystemPromptCache.entries[oldestKey]
		delete(kiroSystemPromptCache.entries, oldestKey)
		kiroSystemPromptCache.totalBytes -= len(entry.content)
	}
}

func resetKiroSystemPromptCacheForTest() {
	kiroSystemPromptCache.Lock()
	defer kiroSystemPromptCache.Unlock()
	kiroSystemPromptCache.entries = make(map[string]kiroSystemPromptCacheEntry)
	kiroSystemPromptCache.totalBytes = 0
}
