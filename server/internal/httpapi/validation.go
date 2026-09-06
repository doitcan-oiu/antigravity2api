package httpapi

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/wo/antigravity2api/internal/convert"
)

func validateOpenAIRequest(req convert.OpenAIRequest) error {
	if req.N != nil && *req.N != 1 {
		return fmt.Errorf("n must be 1; multiple generated choices are not supported by this endpoint")
	}
	for _, field := range []struct {
		name  string
		value *int
	}{{"max_tokens", req.MaxTokens}, {"max_completion_tokens", req.MaxCompletionTokens}, {"max_output_tokens", req.MaxOutputTokens}, {"top_k", req.TopK}} {
		if field.value != nil && *field.value <= 0 {
			return fmt.Errorf("%s must be a positive integer", field.name)
		}
	}
	for _, field := range []struct {
		name     string
		value    *float64
		min, max float64
	}{{"temperature", req.Temperature, 0, 2}, {"top_p", req.TopP, 0, 1}, {"presence_penalty", req.PresencePenalty, -2, 2}, {"frequency_penalty", req.FrequencyPenalty, -2, 2}} {
		if field.value != nil {
			if err := validateFiniteRange(field.name, *field.value, field.min, field.max); err != nil {
				return err
			}
		}
	}
	for _, field := range []struct {
		name  string
		value *convert.OpenAIThinking
	}{{"thinking", req.Thinking}, {"reasoning", req.Reasoning}} {
		if err := validateThinkingBudget(field.name, field.value); err != nil {
			return err
		}
	}
	if err := validateStopValue("stop", req.Stop); err != nil {
		return err
	}
	return convert.ValidateToolCombination(req.Tools, req.Model)
}

func validateClaudeRequest(req convert.ClaudeRequest) error {
	if req.MaxTokens != nil && *req.MaxTokens <= 0 {
		return fmt.Errorf("max_tokens must be a positive integer")
	}
	if req.TopK != nil && *req.TopK <= 0 {
		return fmt.Errorf("top_k must be a positive integer")
	}
	for _, field := range []struct {
		name     string
		value    *float64
		min, max float64
	}{{"temperature", req.Temperature, 0, 1}, {"top_p", req.TopP, 0, 1}} {
		if field.value != nil {
			if err := validateFiniteRange(field.name, *field.value, field.min, field.max); err != nil {
				return err
			}
		}
	}
	if err := validateThinkingBudget("thinking", req.Thinking); err != nil {
		return err
	}
	if err := validateStopValue("stop_sequences", req.StopSequences); err != nil {
		return err
	}
	return convert.ValidateToolCombination(req.Tools, req.Model)
}

func validateNativeRequest(inner map[string]any) error {
	if inner == nil {
		return fmt.Errorf("request must be an object")
	}
	generation := nativeField(inner, "generationConfig", "generation_config")
	if generation != nil {
		gc := convert.AsMap(generation)
		if gc == nil {
			return fmt.Errorf("generationConfig must be an object")
		}
		for _, field := range []struct {
			name, alias string
			min, max    float64
		}{{"temperature", "temperature", 0, 2}, {"topP", "top_p", 0, 1}, {"presencePenalty", "presence_penalty", -2, 2}, {"frequencyPenalty", "frequency_penalty", -2, 2}} {
			if value := nativeField(gc, field.name, field.alias); value != nil {
				number, err := requestNumber(field.name, value)
				if err != nil {
					return err
				}
				if err := validateFiniteRange(field.name, number, field.min, field.max); err != nil {
					return err
				}
			}
		}
		for _, field := range []struct {
			name, alias string
			min         float64
		}{{"maxOutputTokens", "max_output_tokens", 1}, {"topK", "top_k", 1}, {"thinkingBudget", "thinking_budget", -1}} {
			if value := nativeField(gc, field.name, field.alias); value != nil {
				if _, err := validateIntegerValue(field.name, value, field.min); err != nil {
					return err
				}
			}
		}
		if value := nativeField(gc, "candidateCount", "candidate_count"); value != nil {
			number, err := validateIntegerValue("candidateCount", value, 1)
			if err != nil {
				return err
			}
			if number != 1 {
				return fmt.Errorf("candidateCount must be 1; multiple generated choices are not supported by this endpoint")
			}
		}
		if value := nativeField(gc, "seed", "seed"); value != nil {
			if _, err := validateIntegerValue("seed", value, -(1<<53 - 1)); err != nil {
				return err
			}
		}
		if err := validateStopValue("stopSequences", nativeField(gc, "stopSequences", "stop_sequences")); err != nil {
			return err
		}
		if value := nativeField(gc, "thinkingConfig", "thinking_config"); value != nil {
			tc := convert.AsMap(value)
			if tc == nil {
				return fmt.Errorf("thinkingConfig must be an object")
			}
			if value := nativeField(tc, "thinkingBudget", "thinking_budget"); value != nil {
				if _, err := validateIntegerValue("thinkingBudget", value, -1); err != nil {
					return err
				}
			}
			if value := nativeField(tc, "includeThoughts", "include_thoughts"); value != nil {
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("includeThoughts must be a boolean")
				}
			}
			if value := nativeField(tc, "thinkingLevel", "thinking_level"); value != nil {
				if _, ok := value.(string); !ok {
					return fmt.Errorf("thinkingLevel must be a string")
				}
			}
		}
	}
	toolsValue := inner["tools"]
	if toolsValue == nil {
		return nil
	}
	tools := convert.AsSlice(toolsValue)
	if tools == nil {
		return fmt.Errorf("tools must be an array")
	}
	functions, search := false, false
	for _, value := range tools {
		tool := convert.AsMap(value)
		if tool == nil {
			return fmt.Errorf("each tool must be an object")
		}
		if declarations := nativeField(tool, "functionDeclarations", "function_declarations"); declarations != nil {
			list := convert.AsSlice(declarations)
			if list == nil {
				return fmt.Errorf("functionDeclarations must be an array")
			}
			if len(list) > 0 {
				functions = true
			}
		}
		if nativeField(tool, "googleSearch", "google_search") != nil || nativeField(tool, "googleSearchRetrieval", "google_search_retrieval") != nil {
			search = true
		}
	}
	if functions && search {
		return fmt.Errorf("this upstream cannot combine built-in web search with function tools; send them in separate requests")
	}
	return convert.ValidateToolCombination(tools, "")
}

func validateThinkingBudget(name string, thinking *convert.OpenAIThinking) error {
	if thinking != nil && thinking.BudgetTokens != nil && *thinking.BudgetTokens < -1 {
		return fmt.Errorf("%s.budget_tokens must be -1 or a nonnegative integer", name)
	}
	return nil
}
func validateFiniteRange(name string, value, min, max float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < min || value > max {
		return fmt.Errorf("%s must be finite and between %g and %g", name, min, max)
	}
	return nil
}
func validateStopValue(name string, value any) error {
	if value == nil {
		return nil
	}
	switch stops := value.(type) {
	case string, []string:
		return nil
	case []any:
		for _, stop := range stops {
			if _, ok := stop.(string); !ok {
				return fmt.Errorf("%s must be a string or an array of strings", name)
			}
		}
		return nil
	default:
		return fmt.Errorf("%s must be a string or an array of strings", name)
	}
}
func nativeField(object map[string]any, canonical, alias string) any {
	if value, exists := object[canonical]; exists {
		return value
	}
	return object[alias]
}
func requestNumber(name string, value any) (float64, error) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case int:
		number = float64(value)
	case int64:
		number = float64(value)
	case int32:
		number = float64(value)
	case uint:
		number = float64(value)
	case uint64:
		number = float64(value)
	case json.Number:
		var err error
		number, err = value.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s must be a number", name)
		}
	default:
		return 0, fmt.Errorf("%s must be a number", name)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("%s must be finite", name)
	}
	return number, nil
}
func validateIntegerValue(name string, value any, min float64) (float64, error) {
	number, err := requestNumber(name, value)
	if err != nil {
		return 0, err
	}
	if math.Trunc(number) != number || number < min || number > 1<<53-1 {
		return 0, fmt.Errorf("%s must be an integer between %g and %d", name, min, int64(1<<53-1))
	}
	return number, nil
}
