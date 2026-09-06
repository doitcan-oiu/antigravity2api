package convert

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// These expectations mirror demo/common/variant_mapping.rs and the parameters
// applied by its OpenAI/Claude handlers, rather than only testing alias lookup.
func TestVariantHintsBecomeCalibratedOutboundParameters(t *testing.T) {
	cases := []struct {
		model  string
		hint   int
		final  string
		budget int
		max    int
	}{
		{"gemini-3.6-flash-medium", 1000, "gemini-3.7-flash-medium", 4000, 65536},
		{"gemini-3.6-flash-high", 1000, "gemini-3.7-flash-low", 1000, 65536},
		{"gemini-3.7-flash-high", 4000, "gemini-3.7-flash-medium", 4000, 65536},
		{"gemini-3.7-flash-high", 12000, "gemini-3.7-flash-high", 10000, 65536},
		{"gemini-3.7-flash-medium", 512, "gemini-3.7-flash-medium", 4000, 65536},
		{"gemini-3.5-flash-medium", 1000, "gemini-3.5-flash-low", 4000, 65536},
		{"gemini-3.5-flash-low", 12000, "gemini-3.5-flash-extra-low", 1000, 65536},
		{"gemini-3.1-pro-low", 512, "gemini-3.1-pro-low", 1001, 65535},
		{"gemini-3.1-pro-high", 4000, "gemini-pro-agent", 10001, 65535},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.model, tc.hint), func(t *testing.T) {
			body := fmt.Sprintf(`{"model":%q,"thinking":{"type":"enabled","budget_tokens":%d},"max_tokens":2048,"messages":[{"role":"user","content":"ok"}]}`, tc.model, tc.hint)
			openai, mapped, _ := OpenAIToGemini(decodeOpenAI(t, body), "p", "a@gmail.com", "a")
			claude, claudeMapped, _ := ClaudeToGemini(decodeClaude(t, body), "p", "a@gmail.com", "a")
			if mapped != tc.final || claudeMapped != tc.final {
				t.Fatalf("models OpenAI=%s Claude=%s want=%s", mapped, claudeMapped, tc.final)
			}
			for name, outer := range map[string]OuterRequest{"openai": openai, "claude": claude} {
				gen := innerOf(t, outer).GenerationConfig
				if intVal(GetPath(gen, "thinkingConfig", "thinkingBudget")) != tc.budget || intVal(GetPath(gen, "maxOutputTokens")) != tc.max {
					t.Fatalf("%s generation=%#v", name, gen)
				}
			}
		})
	}
}

func TestVariantCalibrationUsesFinalAccountModelWithoutResolvingAgain(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3.6-flash-medium","thinking":{"budget_tokens":512},"messages":[{"role":"user","content":"ok"}]}`)
	for final, budget := range map[string]int{"gemini-3.6-flash-medium": 4000, "gemini-3.7-flash-high": 10000, "gemini-3.5-flash-low": 4000} {
		outer, mapped, _ := OpenAIToGeminiWithModel(req, "p", "e", "a", final)
		if mapped != final || intVal(GetPath(innerOf(t, outer).GenerationConfig, "thinkingConfig", "thinkingBudget")) != budget {
			t.Fatalf("final %s changed: %#v", final, outer)
		}
	}
}

func TestZeroPenaltiesAreOmittedButNonzeroPenaltiesArePreserved(t *testing.T) {
	for _, value := range []float64{0, 0.5, -0.5} {
		req := decodeOpenAI(t, fmt.Sprintf(`{"model":"gemini-3.7-flash-high","presence_penalty":%v,"frequency_penalty":%v,"messages":[{"role":"user","content":"ok"}]}`, value, value))
		outer, _, _ := OpenAIToGemini(req, "p", "e", "a")
		gen := AsMap(innerOf(t, outer).GenerationConfig)
		for _, field := range []string{"presencePenalty", "frequencyPenalty"} {
			if value == 0 && gen[field] != nil || value != 0 && gen[field] != value {
				t.Fatalf("%s=%#v for value %v", field, gen[field], value)
			}
		}
	}
	body := map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": "ok"}}}}, "generationConfig": map[string]any{"presencePenalty": json.Number("0.0"), "frequency_penalty": float64(0)}}
	before, _ := json.Marshal(body)
	outer, _, _ := NativeGeminiToInternal(body, "gemini-3.7-flash-high", "p", "e", "a")
	gen := AsMap(innerOf(t, outer).GenerationConfig)
	if gen["presencePenalty"] != nil || gen["frequency_penalty"] != nil {
		t.Fatalf("native neutral penalty leaked: %#v", gen)
	}
	after, _ := json.Marshal(body)
	if string(before) != string(after) {
		t.Fatal("native caller body mutated")
	}
}

func TestClaudeSDKIdentityNormalizesOnlyTheStandaloneSystemBlock(t *testing.T) {
	billing := "x-anthropic-billing-header: cc_entrypoint=sdk-cli;"
	mention := "Compatibility note: " + claudeAgentSDKIdentity
	system := []any{map[string]any{"type": "text", "text": billing}, map[string]any{"type": "text", "text": claudeAgentSDKIdentity, "cache_control": map[string]any{"type": "ephemeral"}}, map[string]any{"type": "text", "text": mention}}
	req := ClaudeRequest{Model: "gemini-3.7-flash-high", System: system, Messages: []any{map[string]any{"role": "user", "content": claudeAgentSDKIdentity}}}
	before, _ := json.Marshal(req)
	outer, _, _ := ClaudeToGemini(req, "p", "e", "a")
	inner := innerOf(t, outer)
	parts := AsSlice(GetPath(inner.SystemInstruction, "parts"))
	var texts []string
	for _, part := range parts {
		texts = append(texts, AsString(GetPath(part, "text")))
	}
	if !reflect.DeepEqual(texts, []string{billing, claudeCodeCLIIdentity, mention}) {
		t.Fatalf("system blocks=%#v", texts)
	}
	if got := AsString(allRequestParts(inner)[0]["text"]); got != claudeAgentSDKIdentity {
		t.Fatalf("user text changed to %s", got)
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) || strings.Contains(fmt.Sprint(inner.SystemInstruction), "cache_control") {
		t.Fatal("system normalization mutated input or leaked cache_control")
	}
	if got := AsString(GetPath(claudeSystemParts(claudeAgentSDKIdentity)[0], "text")); got != claudeCodeCLIIdentity {
		t.Fatal("standalone string identity was not normalized")
	}
}
