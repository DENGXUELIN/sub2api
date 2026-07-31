package service

import (
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAICompactRequestKey = "openai_compact_request"

// MarkOpenAICompactRequest preserves the client's compact intent after the
// handler normalizes /responses/compact into a regular Responses body.
func MarkOpenAICompactRequest(c *gin.Context) {
	if c != nil {
		c.Set(openAICompactRequestKey, true)
	}
}

func IsOpenAICompactRequest(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, exists := c.Get(openAICompactRequestKey)
	marked, _ := value.(bool)
	return exists && marked
}

func OpenAICompactRequestKeyForTest() string {
	return openAICompactRequestKey
}

// HasCompactionTriggerInInput detects an input item with
// type="compaction_trigger". The handler combines this body signal with the
// request path, stream flag, and Codex beta feature header to distinguish the
// native remote compaction v2 wire from the legacy /responses/compact bridge.
func HasCompactionTriggerInInput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "compaction_trigger" {
			found = true
			return false
		}
		return true
	})
	return found
}
