package convert

import "strings"

type MixRule struct {
	From    string
	To      string
	Percent int
	Enabled bool
}

func ApplyMix(requested string, rules []MixRule, roll int) string {
	mapped := MapModel(requested)
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		to := strings.TrimSpace(rule.To)
		if to == "" {
			continue
		}
		if !MatchMixSource(rule.From, requested, mapped) {
			continue
		}
		p := rule.Percent
		if p < 0 {
			p = 0
		}
		if p > 100 {
			p = 100
		}
		if p > 0 && roll < p {
			return to
		}
		return requested
	}
	return requested
}

func MatchMixSource(from, requested, mapped string) bool {
	from = canonMix(from)
	if from == "" {
		return false
	}
	if canonMix(requested) == from || canonMix(mapped) == from {
		return true
	}
	return canonMix(MapModel(from)) == canonMix(mapped)
}

func canonMix(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "models/")
	for _, suf := range []string{"-preview", "-thinking", "-high", "-low", "-agent"} {
		s = strings.TrimSuffix(s, suf)
	}
	return s
}
