package cooldown

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// QuotaGroup representa um grupo isolado de cota dentro da credencial Antigravity.
type QuotaGroup string

const (
	GroupGemini    QuotaGroup = "gemini"     // gemini-3.8-flash, gemini-3.5-lite, gemini-2.5-*, etc.
	GroupArgon     QuotaGroup = "argon"      // gemini-4-argon-*
	GroupClaudeGPT QuotaGroup = "claude-gpt" // claude-opus-*, claude-sonnet-*, gpt-oss-*
	GroupUnknown   QuotaGroup = "unknown"
)

// ModelToGroup mapeia o modelo para seu respectivo grupo de cota do Antigravity.
func ModelToGroup(model string) QuotaGroup {
	m := strings.ToLower(strings.TrimSpace(model))
	// Argon possui cota dedicada
	if strings.Contains(m, "argon") {
		return GroupArgon
	}
	// Modelos de parceiros externos hospedados no Antigravity (Claude e GPT)
	if strings.Contains(m, "claude") || strings.Contains(m, "gpt") || strings.HasPrefix(m, "chat_") {
		return GroupClaudeGPT
	}
	// Modelos Gemini nativos (Flash, Pro, Lite, etc.)
	if strings.Contains(m, "gemini") || strings.Contains(m, "flash") {
		return GroupGemini
	}
	return GroupUnknown
}

// ResetParseRegexes captura expressões de reset do Google/Antigravity
// Exemplo nos logs: "Resets in 2h14m47s", "reset in 54m", "reset in 30s", "18h45m44s"
var (
	resetInRegex = regexp.MustCompile(`(?i)resets?\s+in\s+(([0-9]+h)?([0-9]+m)?([0-9]+s)?)`)
	durationRe   = regexp.MustCompile(`(?i)^(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)?$`)
)

// ParseResetDuration tenta extrair a duração até o reset do corpo da mensagem de erro 429.
func ParseResetDuration(body string) time.Duration {
	if body == "" {
		return 0
	}
	match := resetInRegex.FindStringSubmatch(body)
	if len(match) > 1 && match[1] != "" {
		if d, err := time.ParseDuration(strings.ToLower(match[1])); err == nil && d > 0 {
			return d
		}
	}

	// Tentar parser manual se formato variar
	dur := parseFlexibleDuration(body)
	if dur > 0 {
		return dur
	}

	return 0
}

func parseFlexibleDuration(s string) time.Duration {
	// Procurar padrões como "XhYmZs", "Xh", "Ym"
	re := regexp.MustCompile(`(?i)([0-9]+)\s*h\s*([0-9]+)\s*m\s*([0-9]+)\s*s|([0-9]+)\s*h\s*([0-9]+)\s*m|([0-9]+)\s*h|([0-9]+)\s*m`)
	loc := re.FindString(s)
	if loc == "" {
		return 0
	}
	loc = strings.ReplaceAll(strings.ToLower(loc), " ", "")
	d, err := time.ParseDuration(loc)
	if err == nil {
		return d
	}
	return 0
}

// GroupCooldownRecord guarda o status de cooldown de um grupo específico de uma credencial.
type GroupCooldownRecord struct {
	AuthID      string     `json:"auth_id"`
	AuthIndex   string     `json:"auth_index,omitempty"`
	Group       QuotaGroup `json:"group"`
	Model       string     `json:"model"`
	CooldownAt  time.Time  `json:"cooldown_at"`
	ResetAt     time.Time  `json:"reset_at"`
	Reason      string     `json:"reason"`
	CooldownDur string     `json:"duration"`
}

// Store gerencia os cooldowns por grupo de forma thread-safe com persistência em disco.
type Store struct {
	mu       sync.RWMutex
	records  map[string]*GroupCooldownRecord // chave: "auth_id:group"
	filePath string
}

func NewStore(persistPath string) *Store {
	s := &Store{
		records:  make(map[string]*GroupCooldownRecord),
		filePath: persistPath,
	}
	s.load()
	return s
}

func makeKey(authID string, group QuotaGroup) string {
	return fmt.Sprintf("%s:%s", authID, group)
}

// RecordFailure registra o cooldown de um grupo de cota se a resposta indicar esgotamento (429 / RESOURCE_EXHAUSTED).
func (s *Store) RecordFailure(authID, authIndex, model string, statusCode int, errorBody string) (bool, *GroupCooldownRecord) {
	if statusCode != 429 && !strings.Contains(errorBody, "RESOURCE_EXHAUSTED") && !strings.Contains(errorBody, "quota") {
		return false, nil
	}

	group := ModelToGroup(model)
	if group == GroupUnknown {
		return false, nil
	}

	now := time.Now()
	dur := ParseResetDuration(errorBody)
	if dur <= 0 {
		// Duração padrão se o upstream não informar: 15 minutos
		dur = 15 * time.Minute
	}
	// Teto de segurança de 24h
	if dur > 24*time.Hour {
		dur = 24 * time.Hour
	}

	record := &GroupCooldownRecord{
		AuthID:      authID,
		AuthIndex:   authIndex,
		Group:       group,
		Model:       model,
		CooldownAt:  now,
		ResetAt:     now.Add(dur),
		Reason:      fmt.Sprintf("HTTP %d: %s", statusCode, truncateString(errorBody, 150)),
		CooldownDur: dur.String(),
	}

	s.mu.Lock()
	key := makeKey(authID, group)
	s.records[key] = record
	s.mu.Unlock()

	s.save()
	return true, record
}

// IsGroupInCooldown verifica se o grupo de uma credencial está em cooldown ativo.
func (s *Store) IsGroupInCooldown(authID string, group QuotaGroup, now time.Time) (bool, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := makeKey(authID, group)
	rec, ok := s.records[key]
	if !ok || rec == nil {
		return false, time.Time{}
	}

	if now.Before(rec.ResetAt) {
		return true, rec.ResetAt
	}

	return false, time.Time{}
}

// ClearCooldown remove o cooldown de um grupo ou de todos os grupos de uma credencial.
func (s *Store) ClearCooldown(authID string, group QuotaGroup) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if group != "" {
		delete(s.records, makeKey(authID, group))
	} else {
		for k, rec := range s.records {
			if rec.AuthID == authID {
				delete(s.records, k)
			}
		}
	}
	s.saveLocked()
}

// ActiveRecords retorna todos os cooldowns ativos no momento.
func (s *Store) ActiveRecords(now time.Time) []*GroupCooldownRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var active []*GroupCooldownRecord
	for _, rec := range s.records {
		if now.Before(rec.ResetAt) {
			active = append(active, rec)
		}
	}
	return active
}

func (s *Store) save() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.saveLocked()
}

func (s *Store) saveLocked() {
	if s.filePath == "" {
		return
	}
	dir := filepath.Dir(s.filePath)
	_ = os.MkdirAll(dir, 0755)

	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.filePath+".tmp", data, 0644)
	_ = os.Rename(s.filePath+".tmp", s.filePath)
}

func (s *Store) load() {
	if s.filePath == "" {
		return
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}
	var loaded map[string]*GroupCooldownRecord
	if err := json.Unmarshal(data, &loaded); err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range loaded {
		if now.Before(v.ResetAt) {
			s.records[k] = v
		}
	}
}

func truncateString(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
