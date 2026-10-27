package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/khaslu/antigravity-group-cooldown/pkg/cooldown"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func setupTestStore(t *testing.T) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "plugin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll(tmpDir)
	})

	statePath := filepath.Join(tmpDir, "state.json")
	store = cooldown.NewStore(statePath)
	resolver = cooldown.NewAliasResolver(nil) // Sem nenhum alias manual hardcoded
	cfg := pluginConfig{
		Enabled:      true,
		StateFile:    statePath,
		LogDecisions: false,
	}
	currentConfig.Store(cfg)
}

func TestRegistration(t *testing.T) {
	respBytes, err := handleMethod(pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var env envelope
	if err := json.Unmarshal(respBytes, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("expected OK: true, got false")
	}

	var reg registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatalf("failed to unmarshal registration: %v", err)
	}

	if !reg.Capabilities.Scheduler {
		t.Errorf("expected Scheduler capability to be true")
	}
	if !reg.Capabilities.UsagePlugin {
		t.Errorf("expected UsagePlugin capability to be true")
	}
	if reg.Metadata.Name != "Antigravity Group Cooldown" {
		t.Errorf("unexpected plugin name: %s", reg.Metadata.Name)
	}
	if len(reg.Metadata.ConfigFields) != 3 {
		t.Errorf("expected 3 ConfigFields, got %d", len(reg.Metadata.ConfigFields))
	}
}

func TestHandleUsageAndSchedulerPick(t *testing.T) {
	setupTestStore(t)

	antigravityID := "antigravity-user@example.com.json"
	claudeID := "claude-user@example.com.json"
	codexID := "codex-user@example.com.json"

	candidates := []pluginapi.SchedulerAuthCandidate{
		{ID: antigravityID, Provider: "antigravity", Priority: 0},
		{ID: claudeID, Provider: "claude", Priority: 0},
		{ID: codexID, Provider: "codex", Priority: 0},
	}

	// 1. Antes de qualquer erro: para o modelo "auto-high" (Argon), o plugin NÃO deve intervir (Handled: false)
	pickReq := pluginapi.SchedulerPickRequest{
		Model:      "auto-high",
		Candidates: candidates,
	}
	pickBytes, _ := json.Marshal(pickReq)
	resp, err := handleMethod(pluginabi.MethodSchedulerPick, pickBytes)
	if err != nil {
		t.Fatalf("pick failed: %v", err)
	}

	var env envelope
	_ = json.Unmarshal(resp, &env)
	var pickResp pluginapi.SchedulerPickResponse
	_ = json.Unmarshal(env.Result, &pickResp)

	if pickResp.Handled {
		t.Errorf("expected Handled: false when no cooldown is active, got true")
	}

	// 2. Simular falha 429 no Argon com alias "meu-alias-argon"
	// O plugin aprende dinamicamente a ligação alias -> modelo real a partir do UsageRecord
	usageRec := pluginapi.UsageRecord{
		Provider:      "antigravity",
		AuthID:        antigravityID,
		Alias:         "meu-alias-argon",
		Model:         "gemini-4-argon-high",
		ResponseModel: "gemini-4-argon-high",
		Failed:        true,
		Failure: pluginapi.UsageFailure{
			StatusCode: 429,
			Body:       `{"error": {"code": 429, "message": "Individual quota reached. Resets in 2h00m00s"}}`,
		},
	}
	usageBytes, _ := json.Marshal(usageRec)
	_, err = handleMethod(pluginabi.MethodUsageHandle, usageBytes)
	if err != nil {
		t.Fatalf("usage.handle failed: %v", err)
	}

	// 3. Verificar que o grupo Argon entrou em cooldown
	now := time.Now()
	inCoolArgon, _ := store.IsGroupInCooldown(antigravityID, cooldown.GroupArgon, now)
	if !inCoolArgon {
		t.Fatalf("expected Argon to be in cooldown")
	}

	// 4. Testar Scheduler para o alias recém-aprendido "meu-alias-argon":
	// O Antigravity DEVE ser ignorado e uma alternativa (Claude ou Codex) deve ser selecionada com Handled: true!
	pickReqArgon := pluginapi.SchedulerPickRequest{
		Model:      "meu-alias-argon",
		Candidates: candidates,
	}
	pickBytesArgon, _ := json.Marshal(pickReqArgon)
	resp, err = handleMethod(pluginabi.MethodSchedulerPick, pickBytesArgon)
	if err != nil {
		t.Fatalf("pick failed: %v", err)
	}
	_ = json.Unmarshal(resp, &env)
	_ = json.Unmarshal(env.Result, &pickResp)

	if !pickResp.Handled {
		t.Fatalf("expected Handled: true when Antigravity Argon is cooling down")
	}
	if pickResp.AuthID == antigravityID {
		t.Errorf("selected cooling Antigravity ID, expected alternative candidate")
	}
	if pickResp.AuthID != claudeID && pickResp.AuthID != codexID {
		t.Errorf("unexpected selected AuthID: %s", pickResp.AuthID)
	}

	// 5. Testar chamada com o nome direto do modelo Gemini Flash:
	// O grupo Gemini NÃO está em cooldown!
	// Portanto, o plugin NÃO deve intervir (Handled: false)!
	pickReqFlash := pluginapi.SchedulerPickRequest{
		Model:      "gemini-3.8-flash-high",
		Candidates: candidates,
	}
	pickBytesFlash, _ := json.Marshal(pickReqFlash)
	resp, err = handleMethod(pluginabi.MethodSchedulerPick, pickBytesFlash)
	if err != nil {
		t.Fatalf("pick failed: %v", err)
	}
	_ = json.Unmarshal(resp, &env)
	_ = json.Unmarshal(env.Result, &pickResp)

	if pickResp.Handled {
		t.Errorf("expected Handled: false for auto-medium because Gemini group is NOT in cooldown!")
	}
}
