package convert

import "strings"

var modelMap = map[string]string{
	"gpt-4": "gemini-2.5-flash",
	"gpt-4-turbo": "gemini-2.5-flash",
	"gpt-4o": "gemini-2.5-flash",
	"gpt-4o-mini": "gemini-2.5-flash",
	"gpt-3.5-turbo": "gemini-2.5-flash",
	"o1": "gemini-3.1-pro-preview",
	"o3": "gemini-3.1-pro-preview",
	"o4-mini": "gemini-3-flash",
	"claude-3-5-sonnet": "claude-sonnet-4-6",
	"claude-3-5-sonnet-20241022": "claude-sonnet-4-6",
	"claude-3-7-sonnet": "claude-sonnet-4-6",
	"claude-sonnet-4": "claude-sonnet-4-6",
	"claude-sonnet-4-5": "claude-sonnet-4-6",
	"claude-sonnet-4-5-thinking": "claude-sonnet-4-6-thinking",
	"claude-sonnet-4-6": "claude-sonnet-4-6",
	"claude-sonnet-4-6-thinking": "claude-sonnet-4-6-thinking",
	"claude-opus-4": "claude-opus-4-6-thinking",
	"claude-opus-4-5": "claude-opus-4-6-thinking",
	"claude-opus-4-5-thinking": "claude-opus-4-6-thinking",
	"claude-opus-4-6": "claude-opus-4-6-thinking",
	"claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
	"claude-haiku-4": "claude-sonnet-4-6",
	"gemini-2.5-flash-lite": "gemini-2.5-flash",
	"gemini-2.5-flash": "gemini-2.5-flash",
	"gemini-2.5-pro": "gemini-2.5-pro",
	"gemini-3-flash": "gemini-3-flash",
	"gemini-3.5-flash": "gemini-3.5-flash",
	"gemini-3.7-flash": "gemini-3.7-flash",
	"gemini-3-pro": "gemini-3-pro-preview",
	"gemini-3-pro-preview": "gemini-3-pro-preview",
	"gemini-3.1-pro": "gemini-3.1-pro-preview",
	"gemini-3.1-pro-preview": "gemini-3.1-pro-preview",
	"gemini-3-pro-image": "gemini-3-pro-image",
}

func MapModel(input string) string {
	input = strings.TrimSpace(input)
	input = strings.TrimPrefix(input, "models/")
	if input == "" {
		return "gemini-2.5-flash"
	}
	if mapped, ok := modelMap[input]; ok {
		return mapped
	}
	lower := strings.ToLower(input)
	if mapped, ok := modelMap[lower]; ok {
		return mapped
	}
	return input
}

func Catalog() []map[string]any {
	ids := []string{
		"gemini-2.5-flash",
		"gemini-2.5-pro",
		"gemini-3-flash",
		"gemini-3.5-flash",
		"gemini-3.7-flash",
		"gemini-3-pro-preview",
		"gemini-3.1-pro-preview",
		"gemini-3-pro-image",
		"claude-sonnet-4-6",
		"claude-sonnet-4-6-thinking",
		"claude-opus-4-6-thinking",
		"gpt-4o",
		"gpt-4o-mini",
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{
			"id":       id,
			"object":   "model",
			"created":  1700000000,
			"owned_by": "antigravity",
		})
	}
	return out
}

func IsThinkingModel(model string) bool {
	m := strings.ToLower(model)
	if strings.Contains(m, "claude") && strings.Contains(m, "thinking") {
		return true
	}
	if !strings.Contains(m, "gemini") {
		return false
	}
	return strings.Contains(m, "thinking") ||
		strings.Contains(m, "pro") ||
		strings.Contains(m, "flash")
}

func IsImageModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "image")
}

func DefaultThinkingBudget(model string) int {
	m := strings.ToLower(model)
	if strings.Contains(m, "pro") {
		return 32768
	}
	return 8192
}
