package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wo/antigravity2api/internal/convert"
)

// Only recover this explicit capability rejection, once on the same account.
// An unrelated 400 must retain its original status and parameters.
func isPenaltyRejection(status int, raw []byte) bool {
	if status != 400 {
		return false
	}
	message := strings.ToLower(clipErr(raw))
	return strings.Contains(message, "penalty is not enabled") || strings.Contains(message, "penalties are not enabled") || strings.Contains(message, "unsupported penalty") || strings.Contains(message, "penalty is not supported")
}

func stripPenaltyFields(payload any) bool {
	var configs []map[string]any
	switch value := payload.(type) {
	case convert.OuterRequest:
		return stripPenaltyFields(value.Request)
	case convert.InnerRequest:
		configs = append(configs, convert.AsMap(value.GenerationConfig))
	case map[string]any:
		if inner, ok := value["request"]; ok {
			return stripPenaltyFields(inner)
		}
		configs = append(configs, convert.AsMap(value["generationConfig"]), convert.AsMap(value["generation_config"]))
	}
	removed := false
	for _, config := range configs {
		for _, key := range []string{"presencePenalty", "frequencyPenalty", "presence_penalty", "frequency_penalty"} {
			if _, ok := config[key]; ok {
				delete(config, key)
				removed = true
			}
		}
	}
	return removed
}

func upstreamEndpoint(resp *http.Response) string {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return ""
	}
	// Never include userinfo, query parameters or authentication headers.
	return resp.Request.URL.Host + resp.Request.URL.EscapedPath()
}

func diagnosticErrorBody(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	const limit = 8 << 10
	truncated := len(raw) > 2*limit
	if truncated {
		raw = raw[:2*limit]
	}
	var formatted bytes.Buffer
	if json.Indent(&formatted, raw, "", "  ") == nil {
		raw = formatted.Bytes()
	}
	text := sanitizeError(string(raw))
	if len(text) > limit {
		text = strings.ToValidUTF8(text[:limit], "")
		truncated = true
	}
	if truncated {
		text += "\n[truncated]"
	}
	return text
}

func failureLog(message string, raw []byte, category, endpoint string, attempts int) string {
	summary := fmt.Sprintf("%s\ncategory=%s attempts=%d", sanitizeError(message), category, attempts)
	if endpoint != "" {
		summary += " endpoint=" + endpoint
	}
	if detail := diagnosticErrorBody(raw); detail != "" {
		summary += "\n" + detail
	}
	return summary
}
