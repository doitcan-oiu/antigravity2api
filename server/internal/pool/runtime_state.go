package pool

import "time"

// AccountRuntime is a detached snapshot of an account's scheduler state.
type AccountRuntime struct {
	Active         int              `json:"active"`
	CooldownUntil  int64            `json:"cooldown_until"`
	ModelCooldowns map[string]int64 `json:"model_cooldowns,omitempty"`
}

// RuntimeStates reports current reservations and unexpired limits. Model limits
// remain separate from account-wide cooldowns so other models stay available.
func (p *Pool) RuntimeStates() map[string]AccountRuntime {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	states := make(map[string]AccountRuntime, len(p.reservations))
	for id, reservation := range p.reservations {
		states[id] = AccountRuntime{Active: reservation.active}
	}
	for key, limit := range p.limits {
		if !limit.until.After(now) {
			continue
		}
		state := states[key.accountID]
		if key.model == "" {
			state.CooldownUntil = limit.until.Unix()
		} else {
			if state.ModelCooldowns == nil {
				state.ModelCooldowns = make(map[string]int64)
			}
			state.ModelCooldowns[key.model] = limit.until.Unix()
		}
		states[key.accountID] = state
	}
	return states
}
