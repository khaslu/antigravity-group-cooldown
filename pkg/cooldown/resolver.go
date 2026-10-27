package cooldown

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// AliasResolver resolve um alias ou nome de modelo para seu QuotaGroup do Antigravity.
// Ele suporta:
// 1. Auto-descoberta lendo o config.yaml do proxy (seção model-alias.antigravity)
// 2. Auto-aprendizado em runtime a partir de UsageRecord (rec.Alias -> rec.Model)
// 3. Overrides manuais opcionais configurados no YAML do plugin
// 4. Classificação direta por nome de modelo via ModelToGroup
type AliasResolver struct {
	mu           sync.RWMutex
	learned      map[string]string // alias -> upstream model (ex: "auto-high" -> "gemini-4-argon-high")
	manualGroups map[string]string // alias -> group name (ex: "meu-alias" -> "argon")
}

func NewAliasResolver(manualGroups map[string]string) *AliasResolver {
	r := &AliasResolver{
		learned:      make(map[string]string),
		manualGroups: make(map[string]string),
	}
	if manualGroups != nil {
		for k, v := range manualGroups {
			r.manualGroups[strings.ToLower(strings.TrimSpace(k))] = strings.ToLower(strings.TrimSpace(v))
		}
	}
	return r
}

// Learn associa um alias a um modelo upstream real (capturado via UsageRecord).
func (r *AliasResolver) Learn(alias, upstreamModel string) {
	alias = strings.ToLower(strings.TrimSpace(alias))
	upstreamModel = strings.TrimSpace(upstreamModel)
	if alias == "" || upstreamModel == "" || strings.EqualFold(alias, upstreamModel) {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.learned[alias] = upstreamModel
}

// DiscoverFromProxyConfig tenta ler a seção model-alias.antigravity do config.yaml do CPA.
func (r *AliasResolver) DiscoverFromProxyConfig(customPath string) int {
	home, _ := os.UserHomeDir()
	candidates := []string{
		customPath,
		"config.yaml",
		"cpa-core/config.yaml",
		"../config.yaml",
		"../cpa-core/config.yaml",
		filepath.Join(home, ".cli-proxy-api", "config.yaml"),
		filepath.Join(home, ".config", "cli-proxy-api", "config.yaml"),
	}

	for _, path := range candidates {
		if path == "" {
			continue
		}
		if data, err := os.ReadFile(path); err == nil {
			discovered := r.parseAliasesFromYAML(data)
			if discovered > 0 {
				return discovered
			}
		}
	}
	return 0
}

func (r *AliasResolver) parseAliasesFromYAML(data []byte) int {
	var root struct {
		ModelAlias struct {
			Antigravity []struct {
				Alias string `yaml:"alias"`
				Name  string `yaml:"name"`
			} `yaml:"antigravity"`
		} `yaml:"model-alias"`
		OAuth struct {
			ModelAlias struct {
				Antigravity []struct {
					Alias string `yaml:"alias"`
					Name  string `yaml:"name"`
				} `yaml:"antigravity"`
			} `yaml:"model-alias"`
		} `yaml:"oauth"`
	}

	if err := yaml.Unmarshal(data, &root); err != nil {
		return 0
	}

	count := 0
	r.mu.Lock()
	defer r.mu.Unlock()

	// Checar model-alias no root
	for _, entry := range root.ModelAlias.Antigravity {
		if entry.Alias != "" && entry.Name != "" {
			r.learned[strings.ToLower(strings.TrimSpace(entry.Alias))] = entry.Name
			count++
		}
	}
	// Checar oauth.model-alias
	for _, entry := range root.OAuth.ModelAlias.Antigravity {
		if entry.Alias != "" && entry.Name != "" {
			r.learned[strings.ToLower(strings.TrimSpace(entry.Alias))] = entry.Name
			count++
		}
	}

	return count
}

// Resolve identifica o QuotaGroup para uma dada string de modelo ou alias.
func (r *AliasResolver) Resolve(modelOrAlias string) QuotaGroup {
	m := strings.ToLower(strings.TrimSpace(modelOrAlias))
	if m == "" {
		return GroupUnknown
	}

	// 1. Verificar se existe override manual configurado
	r.mu.RLock()
	if manual, ok := r.manualGroups[m]; ok {
		r.mu.RUnlock()
		return parseGroupName(manual)
	}

	// 2. Verificar se conhecemos o modelo upstream mapeado para esse alias
	if upstream, ok := r.learned[m]; ok {
		r.mu.RUnlock()
		return ModelToGroup(upstream)
	}
	r.mu.RUnlock()

	// 3. Fallback: classificar diretamente pelo nome do modelo (ex: gemini-3.8-flash, gemini-4-argon, claude-opus)
	return ModelToGroup(m)
}

func parseGroupName(name string) QuotaGroup {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "argon":
		return GroupArgon
	case "gemini", "flash", "lite":
		return GroupGemini
	case "claude", "claude-gpt", "gpt":
		return GroupClaudeGPT
	default:
		return GroupUnknown
	}
}
