package cloudcode

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/wo/antigravity2api/internal/models"
)

func parseQuotaSummary(data []byte) []models.QuotaGroup {
	var raw any
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	root, _ := raw.(map[string]any)
	if root == nil {
		return nil
	}
	items := jsonSlice(root, "groups", "quotaGroups", "quota_groups")
	if len(items) == 0 {
		if nested, ok := root["response"].(map[string]any); ok {
			items = jsonSlice(nested, "groups", "quotaGroups", "quota_groups")
		}
	}
	if len(items) == 0 {
		return nil
	}
	out := make([]models.QuotaGroup, 0, len(items))
	for _, item := range items {
		g, ok := item.(map[string]any)
		if !ok {
			continue
		}
		group := models.QuotaGroup{
			DisplayName: jsonString(jsonLookup(g, "displayName", "display_name")),
			Description: jsonString(jsonLookup(g, "description")),
		}
		for _, bucket := range jsonSlice(g, "buckets") {
			bm, ok := bucket.(map[string]any)
			if !ok {
				continue
			}
			group.Buckets = append(group.Buckets, models.QuotaBucket{
				BucketID:          jsonString(jsonLookup(bm, "bucketId", "bucket_id", "id")),
				Window:            jsonString(jsonLookup(bm, "window")),
				RemainingFraction: jsonFloat(jsonLookup(bm, "remainingFraction", "remaining_fraction")),
				ResetTime:         jsonString(jsonLookup(bm, "resetTime", "reset_time")),
				DisplayName:       jsonString(jsonLookup(bm, "displayName", "display_name")),
				Description:       jsonString(jsonLookup(bm, "description")),
			})
		}
		if len(group.Buckets) > 0 {
			out = append(out, group)
		}
	}
	return out
}

func jsonLookup(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := m[key]; ok && v != nil {
			return v
		}
	}
	return nil
}

func jsonSlice(m map[string]any, keys ...string) []any {
	v := jsonLookup(m, keys...)
	s, _ := v.([]any)
	return s
}

func jsonString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	default:
		return ""
	}
}

func jsonFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0
		}
		return f
	case map[string]any:
		return jsonFloat(jsonLookup(t, "value"))
	default:
		return 0
	}
}
