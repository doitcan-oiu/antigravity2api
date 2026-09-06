package convert

import "strings"

type ModelResolution struct {
	Model           string
	ThinkingBudget  int
	MaxOutputTokens int
	IncludeThinking bool
	Variant         bool
}

// ResolveModel resolves client aliases before account-specific availability is
// considered. Some legacy IDs are also tier aliases in the demo; WithModel
// bypasses this step once account selection has produced a physical model ID.
func ResolveModel(model string, budget *int, effort string) ModelResolution {
	key := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(model), "models/"))
	key = strings.TrimSuffix(key, "-online")
	if IsImageModel(key) {
		for _, s := range []string{"-4k", "-2k", "-1k", "-hd", "-standard", "-21x9", "-16x9", "-9x16", "-4x3", "-3x4", "-3x2", "-2x3", "-5x4", "-4x5", "-1x1"} {
			key = strings.ReplaceAll(key, s, "")
		}
		key = strings.TrimSuffix(key, "-preview")
	}
	tier := "high"
	if budget != nil {
		if *budget < 2000 {
			tier = "low"
		} else if *budget < 7000 {
			tier = "medium"
		}
	}
	switch strings.ToLower(effort) {
	case "low", "medium", "high":
		tier = strings.ToLower(effort)
	}
	mapped := MapModel(key)
	variant := false
	switch key {
	case "gemini-3-flash", "gemini-3.5-flash", "gemini-3.5-flash-high":
		variant = true
		switch tier {
		case "low":
			mapped = "gemini-3.5-flash-extra-low"
		case "medium":
			mapped = "gemini-3.5-flash-low"
		default:
			mapped = "gemini-3-flash-agent"
		}
	case "gemini-3.5-flash-medium":
		variant = true
		mapped = "gemini-3.5-flash-low"
	case "gemini-3.5-flash-low":
		variant = true
		mapped = "gemini-3.5-flash-extra-low"
	case "gemini-3.7-flash", "gemini-3.7-flash-tiered", "gemini-3.7-flash-high",
		"gemini-3.6-flash", "gemini-3.6-flash-tiered", "gemini-3.6-flash-high":
		variant = true
		mapped = "gemini-3.7-flash-" + tier
	case "gemini-3.7-flash-medium", "gemini-3.6-flash-medium":
		variant = true
		mapped = "gemini-3.7-flash-medium"
	case "gemini-3.7-flash-low", "gemini-3.6-flash-low":
		variant = true
		mapped = "gemini-3.7-flash-low"
	case "gemini-3.1-pro", "gemini-pro", "gemini-3.1-pro-high", "gemini-3-pro-high":
		variant = true
		if tier == "low" {
			mapped = "gemini-3.1-pro-low"
		} else {
			mapped = "gemini-pro-agent"
		}
	case "gemini-3.1-pro-low":
		variant = true
		mapped = key
	}
	return ModelResolution{Model: mapped, ThinkingBudget: DefaultThinkingBudget(mapped), MaxOutputTokens: MaxOutputTokens(mapped), IncludeThinking: IsThinkingModel(mapped), Variant: variant}
}

// Variant clients send budget magnitudes as tier hints. The demo replaces those
// hints with calibrated parameters after resolving the tier. Use the final
// account model here so deprecated-model forwarding cannot leave stale budgets.
func applyVariantGeneration(gen map[string]any, requested, final string) {
	if !ResolveModel(requested, nil, "").Variant {
		return
	}
	switch strings.ToLower(final) {
	case "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high",
		"gemini-3.6-flash-low", "gemini-3.6-flash-medium", "gemini-3.6-flash-high",
		"gemini-3.5-flash-extra-low", "gemini-3.5-flash-low", "gemini-3-flash-agent",
		"gemini-3.1-pro-low", "gemini-pro-agent":
	default:
		return
	}
	if !thinkingConfigEnabled(gen) {
		return // Preserve an explicit disabled/none request.
	}
	gen["thinkingConfig"] = map[string]any{"includeThoughts": true, "thinkingBudget": DefaultThinkingBudget(final)}
	gen["maxOutputTokens"] = MaxOutputTokens(final)
}

func requestModel(requested, final string, thinking *OpenAIThinking, effort string) string {
	if final != "" {
		return strings.TrimPrefix(strings.TrimSpace(final), "models/")
	}
	var budget *int
	if thinking != nil {
		budget = thinking.BudgetTokens
		if effort == "" && thinking.Effort != "" {
			effort = thinking.Effort
		}
	}
	return ResolveModel(requested, budget, effort).Model
}
