package cooldown

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAliasResolverDirectModel(t *testing.T) {
	r := NewAliasResolver(nil)

	if g := r.Resolve("gemini-4-argon-high"); g != GroupArgon {
		t.Errorf("expected GroupArgon, got %s", g)
	}
	if g := r.Resolve("gemini-3.8-flash-high"); g != GroupGemini {
		t.Errorf("expected GroupGemini, got %s", g)
	}
	if g := r.Resolve("claude-opus-5-5-high"); g != GroupClaudeGPT {
		t.Errorf("expected GroupClaudeGPT, got %s", g)
	}
}

func TestAliasResolverLearning(t *testing.T) {
	r := NewAliasResolver(nil)

	// Inicialmente um alias customizado aleatório não é conhecido
	alias := "meu-alias-customizado-2027"
	if g := r.Resolve(alias); g != GroupUnknown {
		t.Errorf("expected GroupUnknown before learning, got %s", g)
	}

	// O plugin aprende dinamicamente após ver uma requisição
	r.Learn(alias, "gemini-4-argon-high")

	if g := r.Resolve(alias); g != GroupArgon {
		t.Errorf("expected GroupArgon after learning, got %s", g)
	}

	// Outro alias aprendendo Gemini
	flashAlias := "codigo-rapido"
	r.Learn(flashAlias, "gemini-3.8-flash-high")
	if g := r.Resolve(flashAlias); g != GroupGemini {
		t.Errorf("expected GroupGemini, got %s", g)
	}
}

func TestAliasResolverDiscoverFromYAML(t *testing.T) {
	yamlContent := `
model-alias:
  antigravity:
    - alias: "alias-argon-dinamico"
      name: "gemini-4-argon-high"
    - alias: "alias-flash-dinamico"
      name: "gemini-3.8-flash-high"
`
	tmpDir, err := os.MkdirTemp("", "resolver-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewAliasResolver(nil)
	count := r.DiscoverFromProxyConfig(configPath)
	if count != 2 {
		t.Fatalf("expected 2 discovered aliases, got %d", count)
	}

	if g := r.Resolve("alias-argon-dinamico"); g != GroupArgon {
		t.Errorf("expected GroupArgon, got %s", g)
	}
	if g := r.Resolve("alias-flash-dinamico"); g != GroupGemini {
		t.Errorf("expected GroupGemini, got %s", g)
	}
}

func TestAliasResolverManualOverride(t *testing.T) {
	manual := map[string]string{
		"super-model": "argon",
	}
	r := NewAliasResolver(manual)

	if g := r.Resolve("super-model"); g != GroupArgon {
		t.Errorf("expected GroupArgon via manual override, got %s", g)
	}
}
