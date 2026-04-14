# TOPS

TOPS (**Terminal Operations System**) is a terminal-native assistant for:

- understanding commands (`tops help`)
- generating commands from natural language (`tops gen`)
- answering local system questions with grounded evidence (`tops ask`)

It is designed to be explainable and safety-conscious, not a fully autonomous shell agent.

## What TOPS is (and is not)

TOPS is:

- CLI-first for core operations
- grounded in local evidence when needed
- explicit about assumptions/risks
- policy-controlled for local action execution

TOPS is not:

- a silent auto-execution bot
- a replacement for normal shell usage
- a finished “agent framework” with unrestricted tools

## Current status

This README reflects the current code in this repository.

- `tops` (no subcommand) launches a Bubble Tea manager TUI with `Config` and `Chats` tabs.
- `tops help`, `tops gen`, `tops ask`, and `tops setup` are available via Cobra CLI commands.
- Hosted providers are wired through `any-llm-go` (`openai`, `anthropic`, `gemini`).
- Local providers (`ollama`, `local`) use Ollama’s native Go API.
- Chat/session and workflow audits are persisted in SQLite.
- Test suite currently passes (`go test ./...`) and binary builds successfully.

## Feature overview

### 1) CLI modes

- `tops help "<command or snippet>"`
  - collects local docs (`shell help`, `--help`, `man`) and summarizes them
- `tops gen "<request>"`
  - turns natural language into command output with explanation and risk labels
- `tops ask "<question>"`
  - answers with local evidence, and can request/execute approved workflow steps

### 2) Manager TUI (`tops`)

Top tabs:

- `Config`: model/config/policy management
- `Chats`: embedded shell + TOPS conversation transcript

Config tab supports menu-driven edits plus slash commands.  
Chats tab supports session switching, transcript browsing, copy/export, and TOPS turns (`ask ...` / `gen ...`).

### 3) Workflow + approvals

For `ask`/`gen`, the model may return a workflow plan instead of immediate final JSON.

TOPS then:

- normalizes/validates plan steps
- resolves semantic function calls to allowlisted commands
- classifies risk (`read-only`, `safe-write`, `privileged`, `networked`, etc.)
- applies execution policy (`allow | request | disallow`)
- prompts for approval where required (`y/N`)
- runs one step at a time and audits results

## Safety model

- Command execution is restricted to an internal allowlist in `internal/tools`.
- Arguments are sanitized and time-bounded.
- Workflow policy is explicit:
  - `execution.permissions.read_only`
  - `execution.permissions.write`
- Default behavior today:
  - `read_only=allow`
  - `write=request`
- `execution.trace_mode` controls output verbosity (`release` or `debug`).

Note: `execution.enabled` is still accepted in config for backward compatibility but policy fields are the effective runtime control.

## Data and config files

Default paths:

- Config: `~/.tops/config.json`
- Model profiles: `~/.tops/models.json`
- Chat/workflow DB: `~/.tops/chats.db`

Overrides:

- `TOPS_CONFIG`
- `TOPS_MODEL_PROFILES`
- `TOPS_CHAT_DB`

## Quick start

## Requirements

- Go `1.25.5+`
- macOS or Linux recommended
- Optional: Ollama for local models

## Build

```bash
go build -o ./tops ./cmd/tops
```

Use `./tops` from the project directory. On macOS, `/usr/bin/tops` can exist and conflict with the command name.

## Setup

Interactive setup:

```bash
./tops setup
```

Manual setup:

```bash
./tops setup --manual \
  --provider ollama \
  --model qwen3.5:0.8b \
  --endpoint http://localhost:11434
```

## Example usage

```bash
./tops help "grep -r"
./tops gen "find .log files larger than 100MB"
./tops ask "What is my operating system?"
./tops
```

## Provider support

- `openai`: hosted via `any-llm-go`
- `anthropic`: hosted via `any-llm-go`
- `gemini`: hosted via `any-llm-go` (legacy endpoint path still exists)
- `ollama`: local via `github.com/ollama/ollama/api`
- `local`: alias for Ollama path

For local endpoints (`localhost`, `127.0.0.1`, `::1`), TOPS attempts on-demand `ollama serve` start if unavailable.

## TUI key controls (current)

Global:

- `Shift+Tab`: switch `Config` / `Chats`
- `Ctrl+C`: quit

Config tab:

- `Up/Down`: move menu selection
- `Space`: toggle/cycle selected item
- `Enter`: apply selected item
- `/`: enter slash-command input mode
- `Esc`: leave command mode / cancel edit

Chats tab:

- `Tab`: toggle input focus (`Shell` / `TOPS`)
- `Ctrl+O`: open/close chat session overlay
- `Ctrl+K`: open copy-items overlay
- `Ctrl+E`: export current transcript to temp file
- `Enter`: submit active input
- `Up/Down`, `PgUp/PgDn`, `Home/End`: transcript scrolling

TOPS messages in Chats must start with:

- `ask ...` or
- `gen ...`

## Screenshots

### Config tab

![TOPS Config](./tops-config.png)

### Shell focus in Chats

![TOPS Shell](./tops-shell.png)

### TOPS assistant flow in Chats

![TOPS Assistant](./tops-assistant.png)

## Copy/export in chats

From the chat copy overlay (`Ctrl+K`), you can copy:

- TOPS query text
- TOPS full stream for a turn (thinking/stream/answer/events)
- shell commands
- shell outputs

Clipboard backends:

- macOS: `pbcopy`
- Linux: `wl-copy`, `xclip`, or `xsel`

If clipboard is unavailable, export via `Ctrl+E` and copy from the exported file.

## Configuration examples

### `~/.tops/config.json`

```json
{
  "provider": {
    "type": "ollama",
    "model": "qwen3.5:0.8b",
    "endpoint": "http://localhost:11434"
  },
  "shell": "zsh",
  "output": {
    "format": "text"
  },
  "inspection": {
    "timeout_seconds": 10
  },
  "execution": {
    "enabled": true,
    "permissions": {
      "read_only": "allow",
      "write": "request"
    },
    "trace_mode": "release"
  },
  "debug": {
    "enabled": false
  }
}
```

### `~/.tops/models.json`

```json
{
  "version": 1,
  "entries": {
    "ollama:qwen3.5:0.8b": {
      "provider": "ollama",
      "model": "qwen3.5:0.8b",
      "context": 8192,
      "max_length": 512,
      "think": "off",
      "system_prompt": "Prefer concise, grounded answers.",
      "ask_response": {
        "observations": true,
        "inferences": true,
        "uncertainties": true,
        "assumptions": false,
        "notes": false
      }
    }
  }
}
```

## Development

Run tests:

```bash
go test ./...
```

Build:

```bash
go build -o ./tops ./cmd/tops
```

Release pipeline:

- GoReleaser config: `.goreleaser.yaml`
- GitHub Actions workflow: `.github/workflows/release.yaml`

## Project structure

- `cmd/tops`: CLI entrypoint
- `internal/cli`: Cobra commands
- `internal/app`: runtime wiring and mode execution
- `internal/core`: request normalization + mode dispatch
- `internal/help`, `internal/gen`, `internal/ask`: mode engines
- `internal/llm`: provider adapters
- `internal/workflow`: planner/executor/approval/audit context
- `internal/workflow/functions`: semantic function registry
- `internal/tui`: manager + chats Bubble Tea UI
- `internal/chatstore`: SQLite persistence
- `internal/modelprofile`: per-model profile storage
- `internal/prompt`, `internal/parser`, `internal/render`: prompt/parse/render pipeline
- `internal/policy`, `internal/tools`: risk classification + tool execution

## Known limitations

- The project is still evolving rapidly; UX and command surface can change.
- `help/gen/ask` are CLI commands; manager tab command input is for management flows.
- Tool execution is intentionally constrained to an allowlist and does not run arbitrary scripts.
- Mouse wheel behavior in terminal TUIs always depends on terminal emulator mouse-capture rules.
- No `LICENSE` file is currently present in the repository.

## Why this project exists

TOPS aims to make terminal assistance safer and more trustworthy by default:

- grounded evidence over confident guessing
- explicit approvals over silent automation
- consistent structured output over prompt noise
- manager UX for model/session control alongside scriptable CLI usage
