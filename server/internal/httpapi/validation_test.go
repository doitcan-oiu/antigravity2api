package httpapi

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/wo/antigravity2api/internal/convert"
)

func validationInt(value int) *int           { return &value }
func validationFloat(value float64) *float64 { return &value }

func TestValidateOpenAIRejectsUnsupportedOrInvalidParameters(t *testing.T) {
	tests := []struct {
		name    string
		request convert.OpenAIRequest
		field   string
	}{
		{"multiple choices", convert.OpenAIRequest{N: validationInt(2)}, "n must be 1"},
		{"zero choices", convert.OpenAIRequest{N: validationInt(0)}, "n must be 1"},
		{"zero tokens", convert.OpenAIRequest{MaxTokens: validationInt(0)}, "max_tokens"},
		{"negative completion tokens", convert.OpenAIRequest{MaxCompletionTokens: validationInt(-1)}, "max_completion_tokens"},
		{"negative output tokens", convert.OpenAIRequest{MaxOutputTokens: validationInt(-2)}, "max_output_tokens"},
		{"invalid temperature", convert.OpenAIRequest{Temperature: validationFloat(2.1)}, "temperature"},
		{"infinite temperature", convert.OpenAIRequest{Temperature: validationFloat(math.Inf(1))}, "temperature"},
		{"nan top p", convert.OpenAIRequest{TopP: validationFloat(math.NaN())}, "top_p"},
		{"negative top p", convert.OpenAIRequest{TopP: validationFloat(-.1)}, "top_p"},
		{"invalid presence penalty", convert.OpenAIRequest{PresencePenalty: validationFloat(3)}, "presence_penalty"},
		{"invalid frequency penalty", convert.OpenAIRequest{FrequencyPenalty: validationFloat(-3)}, "frequency_penalty"},
		{"negative budget", convert.OpenAIRequest{Thinking: &convert.OpenAIThinking{BudgetTokens: validationInt(-2)}}, "budget_tokens"},
		{"bad reasoning budget", convert.OpenAIRequest{Reasoning: &convert.OpenAIThinking{BudgetTokens: validationInt(-3)}}, "budget_tokens"},
		{"numeric stop", convert.OpenAIRequest{Stop: 42}, "stop"},
		{"mixed stop array", convert.OpenAIRequest{Stop: []any{"END", 42}}, "stop"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateOpenAIRequest(test.request)
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("got %v; want field %s", err, test.field)
			}
		})
	}
}
func TestValidateOpenAIAcceptsSupportedOptionalParameters(t *testing.T) {
	req := convert.OpenAIRequest{N: validationInt(1), MaxTokens: validationInt(1), MaxOutputTokens: validationInt(65536), Temperature: validationFloat(2), TopP: validationFloat(0), PresencePenalty: validationFloat(-2), FrequencyPenalty: validationFloat(2), Thinking: &convert.OpenAIThinking{Type: "adaptive", BudgetTokens: validationInt(-1)}, Stop: []any{"END", ""}}
	if err := validateOpenAIRequest(req); err != nil {
		t.Fatal(err)
	}
	if err := validateOpenAIRequest(convert.OpenAIRequest{}); err != nil {
		t.Fatal(err)
	}
}
func TestValidateClaudeDoesNotRequireMaxTokensForCountTokens(t *testing.T) {
	if err := validateClaudeRequest(convert.ClaudeRequest{}); err != nil {
		t.Fatalf("optional max_tokens rejected: %v", err)
	}
	if err := validateClaudeRequest(convert.ClaudeRequest{Thinking: &convert.OpenAIThinking{BudgetTokens: validationInt(0)}, StopSequences: []string{"END"}}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []convert.ClaudeRequest{{MaxTokens: validationInt(0)}, {Temperature: validationFloat(1.1)}, {TopP: validationFloat(math.Inf(-1))}, {TopK: validationInt(0)}, {StopSequences: []any{false}}, {Thinking: &convert.OpenAIThinking{BudgetTokens: validationInt(-2)}}} {
		if err := validateClaudeRequest(req); err == nil {
			t.Fatalf("invalid request accepted %#v", req)
		}
	}
}
func TestValidationRejectsMixedBuiltinSearchButKeepsClientSearchFunction(t *testing.T) {
	function := map[string]any{"type": "function", "function": map[string]any{"name": "web_search", "parameters": map[string]any{"type": "object"}}}
	if err := validateOpenAIRequest(convert.OpenAIRequest{Tools: []any{function}}); err != nil {
		t.Fatalf("client function rejected: %v", err)
	}
	if err := validateOpenAIRequest(convert.OpenAIRequest{Tools: []any{function, map[string]any{"type": "web_search"}}}); err == nil {
		t.Fatal("mixed search accepted")
	}
	claudeTool := map[string]any{"name": "read_file", "input_schema": map[string]any{"type": "object"}}
	if err := validateClaudeRequest(convert.ClaudeRequest{Model: "gemini-3-flash-online", Tools: []any{claudeTool}}); err == nil {
		t.Fatal("online/function combination accepted")
	}
}
func TestValidateNativeGenerationAndTools(t *testing.T) {
	invalid := []string{
		`{"generationConfig":{"candidateCount":2}}`,
		`{"generationConfig":{"candidateCount":1.5}}`,
		`{"generationConfig":{"maxOutputTokens":0}}`,
		`{"generationConfig":{"topK":0}}`,
		`{"generationConfig":{"topP":1.1}}`,
		`{"generationConfig":{"temperature":"1"}}`,
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-2}}}`,
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":1.2}}}`,
		`{"generationConfig":{"thinkingConfig":{"includeThoughts":"true"}}}`,
		`{"generationConfig":{"stopSequences":["END",1]}}`,
		`{"generationConfig":[]}`,
		`{"tools":[{"functionDeclarations":[{"name":"read"}]},{"googleSearch":{}}]}`,
		`{"tools":[{"function_declarations":[{"name":"read"}]},{"google_search":{}}]}`,
	}
	for _, raw := range invalid {
		var req map[string]any
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatal(err)
		}
		if err := validateNativeRequest(req); err == nil {
			t.Fatalf("invalid native request accepted %s", raw)
		}
	}
	valid := []string{`{}`, `{"generationConfig":{"candidateCount":1,"temperature":2,"topP":0,"maxOutputTokens":1,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":false}}}`, `{"generation_config":{"candidate_count":1,"thinking_config":{"thinking_budget":0}}}`, `{"tools":[{"googleSearch":{}}]}`, `{"tools":[{"functionDeclarations":[{"name":"read"}]}]}`}
	for _, raw := range valid {
		var req map[string]any
		_ = json.Unmarshal([]byte(raw), &req)
		if err := validateNativeRequest(req); err != nil {
			t.Fatalf("valid native request rejected %s: %v", raw, err)
		}
	}
}
func TestValidateNativeRejectsNonFiniteAndHugeIntegerWithoutMutation(t *testing.T) {
	for _, value := range []any{math.NaN(), math.Inf(1), 1e100, json.Number("999999999999999999999999")} {
		req := map[string]any{"generationConfig": map[string]any{"maxOutputTokens": value}}
		if err := validateNativeRequest(req); err == nil {
			t.Fatalf("unsafe number accepted %v", value)
		}
	}
	req := map[string]any{"generationConfig": map[string]any{"maxOutputTokens": json.Number("2048"), "stopSequences": []any{"END"}}}
	before, _ := json.Marshal(req)
	if err := validateNativeRequest(req); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("validation mutated native input")
	}
}
