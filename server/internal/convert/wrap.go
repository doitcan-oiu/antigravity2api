package convert

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type InnerRequest struct {
	SystemInstruction any `json:"systemInstruction,omitempty"`
	Tools             any `json:"tools,omitempty"`
	ToolConfig        any `json:"toolConfig,omitempty"`
	GenerationConfig  any `json:"generationConfig,omitempty"`
	SafetySettings    any `json:"safetySettings,omitempty"`
	SessionID         any `json:"sessionId,omitempty"`
	Contents          any `json:"contents"`
}

type OuterRequest struct {
	Project            string `json:"project"`
	Request            any    `json:"request"`
	Model              string `json:"model"`
	UserAgent          string `json:"userAgent"`
	RequestType        string `json:"requestType"`
	EnabledCreditTypes any    `json:"enabledCreditTypes,omitempty"`
	RequestID          string `json:"requestId"`
}

func SafetyOff() []map[string]string {
	cats := []string{
		"HARM_CATEGORY_HARASSMENT",
		"HARM_CATEGORY_HATE_SPEECH",
		"HARM_CATEGORY_SEXUALLY_EXPLICIT",
		"HARM_CATEGORY_DANGEROUS_CONTENT",
	}
	out := make([]map[string]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, map[string]string{"category": c, "threshold": "OFF"})
	}
	return out
}

func Wrap(projectID, model, email, sessionID string, inner InnerRequest, image bool) OuterRequest {
	if inner.SafetySettings == nil {
		inner.SafetySettings = SafetyOff()
	}
	if sessionID != "" {
		inner.SessionID = sessionID
	}
	ua := "antigravity"
	reqType := "agent"
	if image {
		reqType = "image_gen"
	}
	if email != "" && !strings.HasSuffix(email, "@gmail.com") && !strings.HasSuffix(email, "@googlemail.com") {
		ua = "jetski"
	}
	out := OuterRequest{
		Project:     projectID,
		Request:     inner,
		Model:       model,
		UserAgent:   ua,
		RequestType: reqType,
		RequestID:   fmt.Sprintf("agent/%d/%s", time.Now().UnixMilli(), uuid.NewString()[:8]),
	}
	if !image {
		out.EnabledCreditTypes = []string{"GOOGLE_ONE_AI"}
	}
	return out
}

func SessionID(accountID, fallback string) string {
	if fallback != "" {
		return fallback
	}
	sum := sha1.Sum([]byte(accountID))
	return hex.EncodeToString(sum[:])[:16]
}

func AsString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}

func AsMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func AsSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func GetPath(v any, keys ...string) any {
	cur := v
	for _, k := range keys {
		m := AsMap(cur)
		if m == nil {
			return nil
		}
		cur = m[k]
	}
	return cur
}
