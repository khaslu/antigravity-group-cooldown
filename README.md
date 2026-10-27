# Antigravity Group Cooldown Plugin for CLIProxyAPI

[![CI](https://github.com/khaslu/antigravity-group-cooldown/actions/workflows/ci.yml/badge.svg)](https://github.com/khaslu/antigravity-group-cooldown/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/khaslu/antigravity-group-cooldown)](https://github.com/khaslu/antigravity-group-cooldown/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![Go Report Card](https://goreportcard.com/badge/github.com/khaslu/antigravity-group-cooldown)](https://goreportcard.com/report/github.com/khaslu/antigravity-group-cooldown)

A native C-ABI plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) and **EasyCLIProxyAPI** that isolates quota cooldowns for Google Antigravity credentials by **model quota group** rather than putting the entire credential into a global multi-hour lockout.

---

## 💡 The Problem

Google Antigravity accounts have three independent quota groups:
1. **`gemini`**: Gemini Flash, Flash-Lite, Pro (`gemini-3.8-flash`, `gemini-3.5-flash-lite`, `gemini-2.5-pro`, etc.)
2. **`argon`**: Dedicated Gemini Argon (`gemini-4-argon-high`)
3. **`claude-gpt`**: Hosted partner models (`claude-opus-5-5-high`, `claude-sonnet-5-5-high`, `gpt-oss-120b-medium`)

In the stock CLIProxyAPI core (v8.0.x), whenever **any** of these groups runs out of quota (`HTTP 429 RESOURCE_EXHAUSTED` / `Individual quota reached`), the proxy engine places the **entire credential** into cooldown for hours (often 18h+).

As a result:
- If your weekly Claude quota runs out in Antigravity, your **Flash and Argon models are blocked too**, even with 100% quota remaining.
- If Argon quota is exhausted, your Flash models get locked out.

---

## ⚡ The Solution

This plugin integrates directly into CLIProxyAPI's native C-ABI lifecycle hooks (`sdk/pluginabi` and `sdk/pluginapi`):

```text
Downstream Request (e.g. model: "auto-high" -> Argon)
       │
       ▼
┌────────────────────────────────────────────────────────┐
│  Scheduler Hook (scheduler.pick)                       │
│  - Checks target group cooldown status                 │
│  - If Argon is healthy: delegates to native CPA (RR)   │
│  - If Argon is cooling: skips Antigravity and picks    │
│    an alternative healthy candidate (e.g., Claude)     │
└────────────────────────────────────────────────────────┘
       │
       ▼
┌────────────────────────────────────────────────────────┐
│  Usage Hook (usage.handle)                             │
│  - Detects upstream 429 quota exhaustion errors        │
│  - Parses exact reset duration (e.g., "Resets in 2h")  │
│  - Records cooldown strictly for the affected group    │
│    with automatic disk persistence across restarts     │
└────────────────────────────────────────────────────────┘
```

### Key Highlights
- **Zero Hardcoded Model Names:** Dynamically discovers all aliases from your proxy's `config.yaml` (`model-alias.antigravity`) on startup, and automatically learns new aliases at runtime from incoming requests.
- **Preserves Native Performance:** When quotas are healthy, the plugin returns `Handled: false`, allowing CLIProxyAPI's native session affinity, prompt caching, and round-robin load balancer to operate without overhead.
- **State Persistence:** Active group cooldowns are persisted in a JSON file so that proxy restarts or reloads do not lose the remaining cooldown schedule.

---

## 📦 Installation

### Option 1: Download Pre-built Binary (Recommended)

1. Download the latest `.zip` for your OS and architecture from [Releases](https://github.com/khaslu/antigravity-group-cooldown/releases).
2. Extract `antigravity-group-cooldown.so` (Linux) or `.dylib` (macOS) into your proxy's `plugins/` directory:
   ```bash
   mkdir -p cpa-core/plugins/
   cp antigravity-group-cooldown.so cpa-core/plugins/
   ```

### Option 2: Build from Source

Requirements: Go 1.24+ and a C compiler (`gcc`).

```bash
git clone https://github.com/khaslu/antigravity-group-cooldown.git
cd antigravity-group-cooldown
make build
# Binary is produced in bin/antigravity-group-cooldown.so
```

---

## ⚙️ Configuration

In your `config.yaml` (typically `cpa-core/config.yaml`):

### 1. Enable the plugin

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    antigravity-group-cooldown:
      enabled: true
      state_file: "~/.cli-proxy-api/antigravity-group-cooldown.json"
      log_decisions: true
```

### 2. Configure request-scoped fallback in `oauth`

To prevent the core engine from triggering an immediate blind global cooldown on 429 before the plugin handles it, add this rule under `oauth:` in `config.yaml`:

```yaml
oauth:
  request-scoped-errors:
    antigravity:
      - status: 429
        match:
          - "Individual quota reached"
          - "QUOTA_EXHAUSTED"
          - "RESOURCE_EXHAUSTED"
        action: "continue"
```

### 3. Restart CLIProxyAPI

Restart your proxy process or click **Restart** in the EasyCLIProxyAPI Desktop Console.

---

## 🔍 Verification & Logs

Check your proxy logs (`core-start-output.log`):

```text
[antigravity-group-cooldown] Discovered 7 model aliases from proxy config
[antigravity-group-cooldown] Configured. Enabled=true, StateFile=/home/user/.cli-proxy-api/antigravity-group-cooldown.json
...
[antigravity-group-cooldown] Quota cooldown recorded: auth=antigravity-... group=argon model=gemini-4-argon-high reset_at=13:46:11 (duration=2h0m0s)
[antigravity-group-cooldown] Skipping Antigravity ... for model=auto-high (group=argon in cooldown until 13:46:11)
[antigravity-group-cooldown] Selected alternative candidate ... (provider=claude) for model=auto-high
```

Requests targeting healthy groups (like `gemini-3.8-flash`) will continue using Antigravity simultaneously without disruption!

---

## 🧪 Testing

Run the automated test suite:

```bash
make test
```

---

## 📄 License

[MIT License](LICENSE) © 2026 khaslu
