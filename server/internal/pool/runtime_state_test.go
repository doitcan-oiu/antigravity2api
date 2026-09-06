package pool

import (
	"testing"
	"time"
)

func TestRuntimeStatesFiltersExpiredAndSeparatesModelLimits(t *testing.T) {
	now := time.Now()
	accountUntil := now.Add(time.Minute)
	modelUntil := now.Add(2 * time.Minute)
	p := &Pool{
		reservations: map[string]*reservation{"active": {active: 2}, "idle": {}},
		limits: map[limitKey]limitRecord{
			{accountID: "active"}:                       {until: accountUntil},
			{accountID: "active", model: "model-a"}:     {until: modelUntil},
			{accountID: "active", model: "expired"}:     {until: now.Add(-time.Second)},
			{accountID: "model-only", model: "model-b"}: {until: modelUntil},
			{accountID: "expired"}:                      {until: now.Add(-time.Second)},
			{accountID: "model-only"}:                   {until: now.Add(-time.Second)},
		},
	}
	states := p.RuntimeStates()
	active := states["active"]
	if active.Active != 2 || active.CooldownUntil != accountUntil.Unix() || len(active.ModelCooldowns) != 1 || active.ModelCooldowns["model-a"] != modelUntil.Unix() {
		t.Fatalf("incorrect active account snapshot: %+v", active)
	}
	modelOnly := states["model-only"]
	if modelOnly.Active != 0 || modelOnly.CooldownUntil != 0 || len(modelOnly.ModelCooldowns) != 1 || modelOnly.ModelCooldowns["model-b"] != modelUntil.Unix() {
		t.Fatalf("model limit became an account-wide limit: %+v", modelOnly)
	}
	if _, ok := states["expired"]; ok {
		t.Fatal("expired-only account remains in runtime snapshot")
	}
	if idle, ok := states["idle"]; !ok || idle.Active != 0 || idle.CooldownUntil != 0 || len(idle.ModelCooldowns) != 0 {
		t.Fatalf("idle reservation snapshot=%+v, present=%t", idle, ok)
	}
}

func TestRuntimeStatesReturnsDetachedMaps(t *testing.T) {
	until := time.Now().Add(time.Minute)
	p := &Pool{
		reservations: map[string]*reservation{"account": {active: 1}},
		limits: map[limitKey]limitRecord{
			{accountID: "account", model: "model-a"}: {until: until},
		},
	}
	first := p.RuntimeStates()
	first["account"].ModelCooldowns["model-a"] = 0
	first["account"].ModelCooldowns["injected"] = 1
	first["injected"] = AccountRuntime{Active: 99}
	delete(first, "account")
	second := p.RuntimeStates()
	if len(second) != 1 || second["account"].Active != 1 || len(second["account"].ModelCooldowns) != 1 || second["account"].ModelCooldowns["model-a"] != until.Unix() {
		t.Fatalf("caller mutation leaked into a later snapshot: %+v", second)
	}
	p.mu.Lock()
	p.reservations["account"].active = 2
	delete(p.limits, limitKey{accountID: "account", model: "model-a"})
	p.mu.Unlock()
	if second["account"].Active != 1 || second["account"].ModelCooldowns["model-a"] != until.Unix() {
		t.Fatal("scheduler updates mutated an existing snapshot")
	}
}
