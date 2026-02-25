# Myringa: Agent Sandbox Manager via Incus TUI

## Context

Myringa is currently a read-only Incus monitoring TUI. The goal is to evolve it into an **agent sandbox manager** — the primary interface for creating, managing, and monitoring isolated Incus environments that coding agents work inside. The TUI remains the human-facing control plane; agent-specific features (MCP server, git worktrees) come later.

### Why Incus for Agent Sandboxes?

The current landscape splits into cloud-hosted sandboxes (E2B, Daytona, Modal) and local container isolation (Docker Sandboxes, Dagger Container Use, Claude Code bubblewrap). Incus fills a gap none of these cover:

- **System containers** — full Linux OS with systemd, not just process namespaces. Agents get a "real machine."
- **VMs too** — same API manages both containers and VMs, so you can offer stronger isolation when needed
- **Persistent state** — unlike Docker's immutable model, Incus containers retain state across restarts
- **Native snapshots** — snapshot before an agent run, restore if it breaks things. No other tool does this as cleanly.
- **Profiles & images** — built-in templating for reproducible environments
- **Self-hosted, zero cost** — no metering, no API keys, no cloud dependency
- **Resource limits & network policies** — Incus supports CPU/memory/disk limits and network firewalling natively

---

## Implementation Plan — Phase 1: Environment Lifecycle TUI

Transform myringa from a read-only dashboard into a create/manage interface for agent environments.

### 1. Instance Actions (start/stop/restart/delete)

Add keybindings in the table view to control instances:
- `s` — start/stop toggle (based on current status)
- `r` — restart
- `d` — delete (with confirmation prompt)
- `x` — exec shell (launches `incus exec <name> -- bash` in a subprocess)

**Files to modify:**
- `internal/ui/model.go` — add keybindings, confirmation state, action commands
- `internal/incus/client.go` — add `StartInstance`, `StopInstance`, `RestartInstance`, `DeleteInstance`, `ExecShell` wrappers around the Incus client API

**Incus API methods available** (from `github.com/lxc/incus/v6/client`):
- `UpdateInstanceState()` — start/stop/restart/freeze
- `DeleteInstance()` — remove instance
- For exec: shell out to `incus exec` CLI (the Go API for exec is complex with websockets)

### 2. Create Instance from Profile

Add a creation flow triggered by `c` key:
- Prompt for: name, image (list available), profile (list available)
- Create instance via API, auto-start it
- Refresh table to show new instance

**Files to modify:**
- `internal/ui/model.go` — add create flow state machine (name input → image select → profile select → confirm)
- `internal/ui/create.go` (new) — creation form view/logic
- `internal/incus/client.go` — add `ListImages`, `ListProfiles`, `CreateInstance` wrappers

**Incus API methods:**
- `GetImageAliases()` — list available images
- `GetProfiles()` — list available profiles
- `CreateInstance()` — create from image + profile

### 3. Snapshot Management

Add snapshot capabilities for pre/post agent work:
- `S` (shift-s) — create snapshot of selected instance (prompt for name, default to timestamp)
- Enter on instance → detail view showing snapshots with restore/delete options

**Files to modify:**
- `internal/ui/model.go` — snapshot keybinding, detail view state
- `internal/ui/detail.go` (new) — instance detail view with snapshot list
- `internal/incus/client.go` — add `CreateSnapshot`, `RestoreSnapshot`, `DeleteSnapshot`, `ListSnapshots` wrappers

**Incus API methods:**
- `CreateInstanceSnapshot()` — take snapshot
- `GetInstanceSnapshots()` — list snapshots
- `UpdateInstanceState()` with restore action — restore to snapshot
- `DeleteInstanceSnapshot()` — remove snapshot

### 4. Instance Filtering & Search

Add filtering to the table view:
- `/` — enter search mode, filter by name
- `f` — cycle filter: all → running → stopped → containers → VMs

**Files to modify:**
- `internal/ui/model.go` — filter state, search input handling, filtered row rendering

### 5. Resource Limit Display

Show configured limits in the table or detail view:
- CPU limit, memory limit from instance config
- Useful for seeing which agent environments are constrained

**Files to modify:**
- `internal/incus/client.go` — extract `limits.cpu`, `limits.memory` from instance config
- `internal/ui/model.go` — display in detail view

---

## Phase 2 (Future — Not This Plan)

- **MCP server mode** — expose environment management as MCP tools for agents
- **Git worktree mounting** — auto-create and mount worktree branches into containers
- **Environment templates** — higher-level abstractions over Incus profiles (e.g., "node20-agent", "python312-agent")
- **Parallel agent orchestration** — manage multiple agent environments, track which agent is in which container

---

## Key Files

| File | Role |
|------|------|
| `internal/incus/client.go` | Incus API wrapper — needs action methods added |
| `internal/ui/model.go` | TUI model — needs keybindings, state machines, new views |
| `internal/ui/styles.go` | Styles — may need new styles for confirmation prompts, forms |
| `internal/ui/create.go` (new) | Instance creation form |
| `internal/ui/detail.go` (new) | Instance detail/snapshot view |
| `main.go` | Entry point — no changes expected |

## Verification

- Run `go build ./...` after each feature to ensure compilation
- Test each feature against a running Incus daemon:
  - Create an instance from a profile, verify it appears in the table
  - Start/stop/restart, verify status updates
  - Take a snapshot, restore it, verify instance state
  - Delete an instance, verify it disappears
  - Exec shell, verify terminal handoff and return to TUI
  - Search/filter, verify table updates correctly

## Sources

- [Docker Sandboxes — Agent Safety](https://www.docker.com/blog/docker-sandboxes-a-new-approach-for-coding-agent-safety/)
- [Docker Sandbox vs Native vs DevContainers](https://shanedeconinck.be/posts/docker-sandbox-coding-agents/)
- [Container Use: Parallel Coding Agents (InfoQ)](https://www.infoq.com/news/2025/08/container-use/)
- [Dagger — Containing Agent Chaos](https://dagger.io/blog/agent-container-use)
- [Claude Code Sandboxing (Anthropic)](https://www.anthropic.com/engineering/claude-code-sandboxing)
- [Best Code Execution Sandbox 2026 (Northflank)](https://northflank.com/blog/best-code-execution-sandbox-for-ai-agents)
- [Daytona vs E2B 2026 (Northflank)](https://northflank.com/blog/daytona-vs-e2b-ai-code-execution-sandboxes)
- [AI Code Sandbox Benchmark 2026 (Superagent)](https://www.superagent.sh/blog/ai-code-sandbox-benchmark-2026)
- [Top AI Sandbox Products 2025 (Modal)](https://modal.com/blog/top-code-agent-sandbox-products)
- [Incus — Profiles](https://linuxcontainers.org/incus/docs/main/profiles/)
- [Incus — Instance Creation](https://linuxcontainers.org/incus/docs/main/howto/instances_create/)
- [Incus Go Client Library](https://pkg.go.dev/github.com/lxc/incus/client)
- [GitHub — dagger/container-use](https://github.com/dagger/container-use)
