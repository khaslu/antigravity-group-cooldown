package cooldown

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModelToGroup(t *testing.T) {
	tests := []struct {
		model    string
		expected QuotaGroup
	}{
		// Gemini / Flash
		{"gemini-3.8-flash-high", GroupGemini},
		{"gemini-3.5-flash-lite", GroupGemini},
		{"gemini-2.5-flash", GroupGemini},
		{"gemini-3-flash", GroupGemini},
		// Argon
		{"gemini-4-argon-high", GroupArgon},
		{"gemini-4-argon-low", GroupArgon},
		{"gemini-4-argon", GroupArgon},
		// Claude / GPT
		{"claude-opus-5-5-high", GroupClaudeGPT},
		{"claude-sonnet-5-5-high", GroupClaudeGPT},
		{"gpt-oss-120b-medium", GroupClaudeGPT},
		{"chat_20706", GroupClaudeGPT},
		// Desconhecido
		{"custom-unknown-model", GroupUnknown},
	}

	for _, tt := range tests {
		got := ModelToGroup(tt.model)
		if got != tt.expected {
			t.Errorf("ModelToGroup(%q) = %q, want %q", tt.model, got, tt.expected)
		}
	}
}

func TestParseResetDuration(t *testing.T) {
	tests := []struct {
		body     string
		expected time.Duration
	}{
		{
			body:     `{"error": {"code": 429, "message": "Individual quota reached. Resets in 2h14m47s"}}`,
			expected: 2*time.Hour + 14*time.Minute + 47*time.Second,
		},
		{
			body:     `RESOURCE_EXHAUSTED: resets in 45m10s`,
			expected: 45*time.Minute + 10*time.Second,
		},
		{
			body:     `Resets in 30s`,
			expected: 30 * time.Second,
		},
		{
			body:     `Quota exceeded. Resets in 18h45m44s`,
			expected: 18*time.Hour + 45*time.Minute + 44*time.Second,
		},
		{
			body:     `Generic 429 without duration`,
			expected: 0,
		},
	}

	for _, tt := range tests {
		got := ParseResetDuration(tt.body)
		if got != tt.expected {
			t.Errorf("ParseResetDuration(%q) = %v, want %v", tt.body, got, tt.expected)
		}
	}
}

func TestStoreCooldownAndPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cooldown-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	filePath := filepath.Join(tmpDir, "state.json")
	store := NewStore(filePath)

	authID := "antigravity-test@example.com.json"

	// 1. Registrar 429 em Claude
	body := `RESOURCE_EXHAUSTED: Resets in 1h`
	recorded, rec := store.RecordFailure(authID, "auth_123", "claude-opus-5-5-high", 429, body)
	if !recorded || rec == nil {
		t.Fatalf("expected recorded failure")
	}
	if rec.Group != GroupClaudeGPT {
		t.Errorf("expected GroupClaudeGPT, got %s", rec.Group)
	}

	// 2. Verificar que Claude está em cooldown, mas Gemini e Argon NÃO estão
	now := time.Now()
	inCool, resetAt := store.IsGroupInCooldown(authID, GroupClaudeGPT, now)
	if !inCool || resetAt.Before(now) {
		t.Errorf("expected Claude to be in cooldown")
	}

	inCoolGemini, _ := store.IsGroupInCooldown(authID, GroupGemini, now)
	if inCoolGemini {
		t.Errorf("Gemini should NOT be in cooldown")
	}

	inCoolArgon, _ := store.IsGroupInCooldown(authID, GroupArgon, now)
	if inCoolArgon {
		t.Errorf("Argon should NOT be in cooldown")
	}

	// 3. Testar recarga de persistência
	storeReloaded := NewStore(filePath)
	inCoolReloaded, _ := storeReloaded.IsGroupInCooldown(authID, GroupClaudeGPT, now)
	if !inCoolReloaded {
		t.Errorf("reloaded store should retain cooldown")
	}

	// 4. Testar limpeza
	storeReloaded.ClearCooldown(authID, GroupClaudeGPT)
	inCoolCleared, _ := storeReloaded.IsGroupInCooldown(authID, GroupClaudeGPT, now)
	if inCoolCleared {
		t.Errorf("cooldown should be cleared")
	}
}
