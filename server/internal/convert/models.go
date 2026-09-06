package convert

import (
	"sort"
	"strings"
)

type OfficialModel struct {
	ID          string
	DisplayName string
}

var modelMap = map[string]string{
	"gpt-4":                      "gemini-2.5-flash",
	"gpt-4-turbo":                "gemini-2.5-flash",
	"gpt-4o":                     "gemini-2.5-flash",
	"gpt-4o-mini":                "gemini-2.5-flash",
	"gpt-3.5-turbo":              "gemini-2.5-flash",
	"o1":                         "gemini-3.1-pro-preview",
	"o3":                         "gemini-3.1-pro-preview",
	"o4-mini":                    "gemini-3-flash",
	"claude-3-5-sonnet":          "claude-sonnet-4-6",
	"claude-3-5-sonnet-20241022": "claude-sonnet-4-6",
	"claude-3-7-sonnet":          "claude-sonnet-4-6",
	"claude-sonnet-4":            "claude-sonnet-4-6",
	"claude-sonnet-4-5":          "claude-sonnet-4-6",
	"claude-sonnet-4-5-thinking": "claude-sonnet-4-6-thinking",
	"claude-sonnet-4-6":          "claude-sonnet-4-6",
	"claude-sonnet-4-6-thinking": "claude-sonnet-4-6-thinking",
	"claude-opus-4":              "claude-opus-4-6-thinking",
	"claude-opus-4-5":            "claude-opus-4-6-thinking",
	"claude-opus-4-5-thinking":   "claude-opus-4-6-thinking",
	"claude-opus-4-6":            "claude-opus-4-6-thinking",
	"claude-opus-4-6-thinking":   "claude-opus-4-6-thinking",
	"claude-haiku-4":             "claude-sonnet-4-6",
	"gemini-2.5-flash-lite":      "gemini-2.5-flash",
	"gemini-2.5-flash":           "gemini-2.5-flash",
	"gemini-2.5-pro":             "gemini-2.5-pro",
	"gemini-3-flash":             "gemini-3-flash",
	"gemini-3.5-flash":           "gemini-3.5-flash",
	"gemini-3.7-flash":           "gemini-3.7-flash",
	"gemini-3-pro":               "gemini-3-pro-preview",
	"gemini-3-pro-preview":       "gemini-3-pro-preview",
	"gemini-3.1-pro":             "gemini-3.1-pro-preview",
	"gemini-3.1-pro-preview":     "gemini-3.1-pro-preview",
	"gemini-3-pro-image":         "gemini-3-pro-image",
	"gpt-4-turbo-preview":        "gemini-2.5-flash",
	"gpt-4-0125-preview":         "gemini-2.5-flash",
	"gpt-4-1106-preview":         "gemini-2.5-flash",
	"gpt-4-0613":                 "gemini-2.5-flash",
	"gpt-4o-2024-05-13":          "gemini-2.5-flash",
	"gpt-4o-2024-08-06":          "gemini-2.5-flash",
	"gpt-4o-mini-2024-07-18":     "gemini-2.5-flash",
	"claude-sonnet-4-5-20250929": "claude-sonnet-4-6-thinking",
	"claude-3-5-sonnet-20240620": "claude-sonnet-4-6",
	"claude-opus-4-5-20251101":   "claude-opus-4-6-thinking",
	"claude-opus-4-6-20260201":   "claude-opus-4-6-thinking",
	"claude-opus-4.6":            "claude-opus-4-6-thinking",
	"claude-opus-4.6-thinking":   "claude-opus-4-6-thinking",
	"claude-3-haiku-20240307":    "claude-sonnet-4-6",
	"claude-haiku-4-5-20251001":  "claude-sonnet-4-6",
	"gemini-3-flash-preview":     "gemini-3-flash",
	"gemini-3-pro-image-preview": "gemini-3-pro-image",
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

// RewriteToAvailable applies official deprecated-model forwarding, then rewrites
// the mapped ID to one the selected account actually received from fetchAvailableModels.
func RewriteToAvailable(mapped string, available []string, forwarding map[string]string) string {
	mapped = strings.TrimSpace(strings.TrimPrefix(mapped, "models/"))
	if mapped == "" {
		return mapped
	}
	if next := strings.TrimSpace(forwarding[mapped]); next != "" {
		mapped = next
	}
	if id := matchAvailable(mapped, available); id != "" {
		return id
	}
	return mapped
}

func Catalog() []map[string]any {
	return BuildCatalog(nil)
}

func BuildCatalog(official []OfficialModel) []map[string]any {
	type entry struct {
		id       string
		display  string
		official bool
	}
	seen := map[string]struct{}{}
	var list []entry
	add := func(id, display string, official bool) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		list = append(list, entry{id: id, display: strings.TrimSpace(display), official: official})
	}
	for _, m := range official {
		add(m.ID, m.DisplayName, true)
	}
	for alias, target := range modelMap {
		add(alias, "", false)
		add(target, "", false)
	}
	if len(official) == 0 {
		for _, id := range fallbackCatalogIDs {
			add(id, "", false)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		item := map[string]any{
			"id":       e.id,
			"object":   "model",
			"created":  1706745600,
			"owned_by": "antigravity",
			"official": e.official,
		}
		if e.display != "" {
			item["display_name"] = e.display
		}
		out = append(out, item)
	}
	return out
}

var fallbackCatalogIDs = []string{
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

func matchAvailable(mapped string, available []string) string {
	if mapped == "" || len(available) == 0 {
		return ""
	}
	index := map[string]string{}
	for _, name := range available {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		index[strings.ToLower(name)] = name
	}
	if id, ok := index[strings.ToLower(mapped)]; ok {
		return id
	}
	for _, cand := range familyCandidates(mapped) {
		if id, ok := index[cand]; ok {
			return id
		}
	}
	return ""
}

func familyCandidates(model string) []string {
	m := strings.ToLower(strings.TrimSpace(model))
	proImage := []string{"gemini-3-pro-image", "gemini-3.1-pro-image"}
	flashImage := []string{"gemini-3-flash-image", "gemini-3.1-flash-image"}
	if contains(proImage, m) {
		return uniquePrefixed(m, proImage)
	}
	if contains(flashImage, m) {
		return uniquePrefixed(m, flashImage)
	}
	proFamily := []string{
		"gemini-pro-agent",
		"gemini-3.1-pro-preview",
		"gemini-3-pro-preview",
		"gemini-3.1-pro-high",
		"gemini-3-pro-high",
		"gemini-3.1-pro-low",
		"gemini-3-pro-low",
		"gemini-3.1-pro",
		"gemini-3-pro",
	}
	if contains(proFamily, m) {
		return uniquePrefixed(m, proFamily)
	}
	return nil
}

func uniquePrefixed(first string, rest []string) []string {
	seen := map[string]struct{}{first: {}}
	out := []string{first}
	for _, v := range rest {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func IsThinkingModel(model string) bool {
	m := strings.ToLower(model)
	if strings.Contains(m, "claude") && (strings.Contains(m, "thinking") || strings.Contains(m, "sonnet-4-6")) {
		return true
	}
	if !strings.Contains(m, "gemini") || IsImageModel(m) || strings.Contains(m, "flash-lite") {
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
	switch m {
	case "gemini-3.7-flash-low", "gemini-3.6-flash-low", "gemini-3.5-flash-extra-low":
		return 1000
	case "gemini-3.7-flash-medium", "gemini-3.6-flash-medium", "gemini-3.5-flash-low":
		return 4000
	case "gemini-3.7-flash-high", "gemini-3.6-flash-high", "gemini-3-flash-agent":
		return 10000
	case "gemini-3.1-pro-low", "gemini-3-pro-low":
		return 1001
	case "gemini-pro-agent":
		return 10001
	case "claude-sonnet-4-6":
		return 1024
	}
	if strings.Contains(m, "flash-lite") {
		return 0
	}
	if strings.Contains(m, "claude-opus-4-6") {
		return 24576
	}
	if strings.Contains(m, "gemini-3.1-pro") || strings.Contains(m, "gemini-3-pro-high") {
		return 49152
	}
	if strings.Contains(m, "pro") || strings.Contains(m, "flash") || strings.Contains(m, "thinking") {
		return 32768
	}
	return 8192
}

func MaxOutputTokens(model string) int {
	m := strings.ToLower(model)
	if strings.Contains(m, "flash-lite") {
		return 16384
	}
	if strings.Contains(m, "opus-4-6") {
		return 57344
	}
	if m == "gemini-pro-agent" || m == "gemini-3.1-pro-low" {
		return 65535
	}
	if strings.Contains(m, "claude") {
		return 64000
	}
	return 65536
}
