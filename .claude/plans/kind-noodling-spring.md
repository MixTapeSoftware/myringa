# Plan: Instance Lifecycle Management (Create, Enter, Delete) + Description Column

## Context

myringa is currently a read-only Incus dashboard. The goal is to make it a full dev environment manager — create containers/VMs with configurable tooling, shell into them, and tear them down. The user has a battle-tested ~400-line shell script that handles creation with edge cases (AppArmor masking for Docker-in-Incus, idmap fallback chain, subuid/subgid). Rather than rewrite that in Go, the TUI will collect inputs and shell out to the script.

---

## 1. Add Description Column (OS info)

**Files:** `internal/incus/client.go`, `internal/ui/model.go`

- Add `Description` field to `InstanceRow` struct
- Extract from `inst.ExpandedConfig["image.description"]` in `FetchInstances` (falls back to `inst.Config["image.os"]`, then `"—"`)
- Add `DESC` column to `tableColumns()` between NAME and CPU (width ~24)
- Include in `toTableRows()`

---

## 2. Add `huh` Dependency for Forms

**Why:** The create form needs text input, single-select, and multi-select. `huh` (Charm's form library) provides all of these with keyboard navigation, validation, and theming — integrates as a BubbleTea model. Building this from scratch with raw `bubbles/textinput` would be significant custom work for a worse result.

```
go get github.com/charmbracelet/huh
```

---

## 3. Create Flow

**New files:** `internal/ui/createview.go`, `internal/incus/create.go`, `scripts/create.sh` (embedded)

### 3a. Create Form View (`createview.go`)

New view state `viewCreate`. Activated by `c` keybinding from the table view.

**Form fields (using `huh`):**
- **Name** — text input, smart default derived from selected image (e.g., `alpine-dev-01`, auto-incrementing suffix based on existing instances)
- **Type** — select: Container / VM
- **Image** — select with predefined "latest" entries + "Browse remote..." option:
  - `images:alpine/edge` → "Alpine (rolling)"
  - `images:ubuntu/noble` → "Ubuntu 24.04 LTS"
  - `images:debian/trixie` → "Debian 13"
  - `images:fedora/41` → "Fedora 41"
  - `images:rocky/9` → "Rocky Linux 9"
  - Browse remote... (fetches from image server, shown in a filterable list)
- **Options** — multi-select checklist:
  - Oh My Zsh
  - fzf + bat
  - Docker
  - 1Password CLI
  - HTTP proxy (selecting this reveals host:port input)

**Always installed (not in checklist):** mise

### 3b. Creation Engine (`internal/incus/create.go`)

- Bundle the create script via `//go:embed scripts/create.sh`
- On form submit:
  1. Write embedded script to temp file (or use override if exists)
  2. Build CLI flags from form answers
  3. Execute script, parse stdout for phase transitions
  4. Show step-by-step progress with spinner in a `viewCreating` state
- Script override: if `~/.config/myringa/create.sh` exists, use it instead of the embedded script (same flag interface)
- Post-provision hook: if `~/.config/myringa/init.sh` exists, push it into the container and execute after the main script completes

### 3c. Progress View

New view state `viewCreating`. Shows:
```
  Creating alpine-dev-01...

  ✓ Container launched
  ✓ Packages installed
  ● Setting up Docker...        ← spinner on current step
    Configuring user
    Mounting workspace
```

Parse script `[+]` log lines to detect phase transitions. Map log prefixes to step names.

---

## 4. Enter/Shell Flow

**Files:** `internal/ui/model.go`

- Keybinding: `e` on selected row (only for Running instances)
- Use `tea.ExecProcess` to suspend the TUI and run:
  ```
  incus exec <name> -- su - <host_user>
  ```
  where `<host_user>` is the current `os/user.Current().Username`
- TUI resumes automatically when the shell exits
- Show hint in footer: `e shell`

---

## 5. Delete Flow

**New file:** `internal/ui/deleteview.go`

- Keybinding: `d` on selected row
- New view state `viewDelete`
- Shows confirmation prompt:
  ```
  Delete instance "alpine-dev-01"?

  Type the instance name to confirm: [          ]

  This will permanently destroy the container and all its data.
  esc cancel
  ```
- Uses `bubbles/textinput` for the confirmation field
- On match: calls `incus delete --force <name>` via the Incus SDK (`c.DeleteInstance(name)`)
- Returns to table view with a refresh

---

## 6. Script Bundling & Override

**New directory:** `scripts/`

- `scripts/create.sh` — the user's script (cleaned up, oh-my-zsh moved from always-on to flag-controlled)
- Embedded via `//go:embed` in `internal/incus/create.go`
- Override chain:
  1. `~/.config/myringa/create.sh` → full replacement (same flag interface)
  2. `~/.config/myringa/init.sh` → post-provision hook (pushed into container, executed after main script)

---

## 7. Keybinding Summary

| Key     | Context        | Action                        |
|---------|----------------|-------------------------------|
| `c`     | table view     | Open create form              |
| `e`     | table view     | Shell into selected instance  |
| `d`     | table view     | Delete selected instance      |
| `enter` | table view     | Vuln detail (existing)        |
| `esc`   | any sub-view   | Back to table                 |
| `r`     | table view     | Reload trivy results          |

---

## File Change Summary

| File | Change |
|------|--------|
| `internal/incus/client.go` | Add `Description` to `InstanceRow`, extract from config |
| `internal/incus/create.go` | **New** — script embedding, execution, override logic |
| `internal/ui/model.go` | Add view states, keybindings for c/e/d, description column |
| `internal/ui/createview.go` | **New** — huh form for create flow |
| `internal/ui/deleteview.go` | **New** — type-to-confirm delete view |
| `internal/ui/progressview.go` | **New** — step-by-step creation progress |
| `internal/ui/styles.go` | Add styles for progress steps (success, pending) |
| `scripts/create.sh` | **New** — bundled creation script |
| `go.mod` | Add `github.com/charmbracelet/huh` |

---

## Verification

1. `go build ./...` — compiles cleanly
2. Run the TUI, verify DESC column shows OS for existing instances
3. Press `c`, fill form, create a container — verify script runs with correct flags and progress shows
4. Press `e` on a running instance — verify shell opens and TUI resumes on exit
5. Press `d` on an instance — verify type-to-confirm works and instance is deleted
6. Test override: place a script at `~/.config/myringa/create.sh`, verify it's used instead of embedded
7. Test init hook: place `~/.config/myringa/init.sh`, verify it runs post-creation
