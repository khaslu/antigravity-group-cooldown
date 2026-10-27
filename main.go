package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/khaslu/antigravity-group-cooldown/pkg/cooldown"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

var (
	currentConfig atomic.Value
	store         *cooldown.Store
	resolver      *cooldown.AliasResolver
	initOnce      sync.Once
	roundRobinIdx uint64
)

type pluginConfig struct {
	Enabled         bool              `yaml:"enabled"`
	StateFile       string            `yaml:"state_file"`
	ProxyConfigFile string            `yaml:"proxy_config_file"` // Opcional: caminho para o config.yaml do CPA
	LogDecisions    bool              `yaml:"log_decisions"`
	AliasGroups     map[string]string `yaml:"alias_groups"`      // Opcional: overrides manuais específicos
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	Scheduler   bool `json:"scheduler"`
	UsagePlugin bool `json:"usage_plugin"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodSchedulerPick:
		return pickAuth(request)
	case pluginabi.MethodUsageHandle:
		return handleUsage(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}

	cfg := pluginConfig{
		Enabled:      true,
		LogDecisions: true,
		StateFile:    defaultStateFilePath(),
		AliasGroups:  make(map[string]string),
	}

	if len(req.ConfigYAML) > 0 {
		var decoded pluginConfig
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &decoded); errUnmarshal == nil {
			cfg.Enabled = decoded.Enabled
			if decoded.StateFile != "" {
				cfg.StateFile = decoded.StateFile
			}
			if decoded.ProxyConfigFile != "" {
				cfg.ProxyConfigFile = decoded.ProxyConfigFile
			}
			cfg.LogDecisions = decoded.LogDecisions
			if len(decoded.AliasGroups) > 0 {
				cfg.AliasGroups = decoded.AliasGroups
			}
		}
	}

	initOnce.Do(func() {
		store = cooldown.NewStore(cfg.StateFile)
		resolver = cooldown.NewAliasResolver(cfg.AliasGroups)
		discovered := resolver.DiscoverFromProxyConfig(cfg.ProxyConfigFile)
		if discovered > 0 {
			log.Printf("[antigravity-group-cooldown] Discovered %d model aliases from proxy config", discovered)
		}
	})

	currentConfig.Store(cfg)
	log.Printf("[antigravity-group-cooldown] Configured. Enabled=%v, StateFile=%s", cfg.Enabled, cfg.StateFile)
	return nil
}

func defaultStateFilePath() string {
	home, _ := os.UserHomeDir()
	if home != "" {
		return filepath.Join(home, ".cli-proxy-api", "antigravity-group-cooldown.json")
	}
	return "/tmp/antigravity-group-cooldown.json"
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Antigravity Group Cooldown",
			Version:          "0.1.0",
			Author:           "khaslu",
			GitHubRepository: "https://github.com/khaslu/antigravity-group-cooldown",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "log_decisions",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     true,
					Description: "Log cooldown detections and routing failover decisions to the proxy output.",
				},
				{
					Name:        "state_file",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "Path to JSON file where active group cooldowns are persisted (leave empty for default).",
				},
				{
					Name:        "proxy_config_file",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "Custom path to proxy config.yaml for automatic alias discovery (auto-detected if empty).",
				},
			},
		},
		Capabilities: registrationCapability{
			Scheduler:   true,
			UsagePlugin: true,
		},
	}
}

// pickAuth intercepta a seleção de credenciais quando o grupo pretendido do Antigravity está em cooldown.
func pickAuth(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	cfg := getLoadedConfig()
	if !cfg.Enabled || store == nil {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	now := time.Now()
	targetGroup := cooldown.GroupUnknown
	if resolver != nil {
		targetGroup = resolver.Resolve(req.Model)
	}

	// Se o modelo/alias não pertence a nenhum grupo conhecido do Antigravity, deixa o CPA tratar
	if targetGroup == cooldown.GroupUnknown {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	// Verificar se algum candidato é Antigravity e se o grupo dele está em cooldown
	var hasCoolingAntigravity bool
	var eligibleCandidates []pluginapi.SchedulerAuthCandidate

	for _, cand := range req.Candidates {
		isAntigravity := isAntigravityCandidate(cand)
		if isAntigravity {
			inCool, resetAt := store.IsGroupInCooldown(cand.ID, targetGroup, now)
			if inCool {
				hasCoolingAntigravity = true
				if cfg.LogDecisions {
					log.Printf("[antigravity-group-cooldown] Skipping Antigravity %s for model=%s (group=%s in cooldown until %s)",
						cand.ID, req.Model, targetGroup, resetAt.Format("15:04:05"))
				}
				continue // Candidato do Antigravity desqualificado para este grupo!
			}
		}
		eligibleCandidates = append(eligibleCandidates, cand)
	}

	// Se nenhum Antigravity foi descartado por cooldown de grupo, deixa o CPA tratar nativamente!
	if !hasCoolingAntigravity {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	// Se houve descarte de Antigravity mas existem outros candidatos elegíveis (ex: Claude ou Codex)
	if len(eligibleCandidates) > 0 {
		// Escolher o próximo candidato elegível via round-robin simples
		idx := atomic.AddUint64(&roundRobinIdx, 1) - 1
		selected := eligibleCandidates[idx%uint64(len(eligibleCandidates))]

		if cfg.LogDecisions {
			log.Printf("[antigravity-group-cooldown] Selected alternative candidate %s (provider=%s) for model=%s",
				selected.ID, selected.Provider, req.Model)
		}

		return okEnvelope(pluginapi.SchedulerPickResponse{
			AuthID:  selected.ID,
			Handled: true,
		})
	}

	// Se todos os candidatos foram desqualificados (ou seja, só havia Antigravity e ele está resfriado),
	// deixa o CPA tratar e emitir a resposta de cooldown/erro adequada
	return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
}

// handleUsage escuta os resultados de requests e detecta 429 para registrar cooldown por grupo.
func handleUsage(raw []byte) ([]byte, error) {
	var rec pluginapi.UsageRecord
	if errUnmarshal := json.Unmarshal(raw, &rec); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	cfg := getLoadedConfig()
	if !cfg.Enabled || store == nil {
		return okEnvelope(map[string]any{"ok": true})
	}

	// Auto-aprendizado: associar alias ao modelo upstream real se informado
	if rec.Alias != "" && rec.Model != "" && resolver != nil {
		resolver.Learn(rec.Alias, rec.Model)
	}

	// Apenas falhas do provedor Antigravity nos interessam
	isAntigravity := strings.EqualFold(rec.Provider, "antigravity") || strings.Contains(strings.ToLower(rec.AuthID), "antigravity")
	if !isAntigravity || !rec.Failed {
		return okEnvelope(map[string]any{"ok": true})
	}

	// Verificar se é erro de cota / 429
	model := rec.ResponseModel
	if model == "" {
		model = rec.Model
	}

	recorded, groupRec := store.RecordFailure(rec.AuthID, rec.AuthIndex, model, rec.Failure.StatusCode, rec.Failure.Body)
	if recorded && groupRec != nil {
		log.Printf("[antigravity-group-cooldown] Quota cooldown recorded: auth=%s group=%s model=%s reset_at=%s (duration=%s)",
			groupRec.AuthID, groupRec.Group, groupRec.Model, groupRec.ResetAt.Format("15:04:05"), groupRec.CooldownDur)
	}

	return okEnvelope(map[string]any{"ok": true})
}

func isAntigravityCandidate(cand pluginapi.SchedulerAuthCandidate) bool {
	if strings.EqualFold(cand.Provider, "antigravity") {
		return true
	}
	if strings.Contains(strings.ToLower(cand.ID), "antigravity") {
		return true
	}
	return false
}

func getLoadedConfig() pluginConfig {
	if v := currentConfig.Load(); v != nil {
		return v.(pluginConfig)
	}
	return pluginConfig{Enabled: true}
}

func okEnvelope(result any) ([]byte, error) {
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{
		OK:     true,
		Result: raw,
	})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{
		OK: false,
		Error: &envelopeError{
			Code:    code,
			Message: message,
		},
	})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, data []byte) {
	if response == nil {
		return
	}
	if len(data) == 0 {
		response.ptr = nil
		response.len = 0
		return
	}
	ptr := C.CBytes(data)
	response.ptr = ptr
	response.len = C.size_t(len(data))
}
