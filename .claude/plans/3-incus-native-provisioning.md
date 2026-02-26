# Plan: Incus-Native Provisioning

> Composable Incus primitives and a thin Go launcher for dev container provisioning.

## Who This Is For

This plan builds dev container provisioning for the tool author — a personal dev workflow tool. The primary user is someone who wants to spin up a fully-configured Incus container, exec in, and start working. Agent sandbox provisioning (headless, fast-boot, API-first) is a future use case that will reuse the same `internal/provision/` package with different defaults (no dev tools, no shell candy, optimized for boot speed and disposability).

## Decisions Locked

- **Launcher**: Go package inside myringa (`internal/provision/`)
- **Instance types**: Containers only (dev containers)
- **Init strategy**: Cloud-init for both Alpine and Ubuntu (unified path)
- **UX**: Flags only, no interactive prompts (TUI integration comes later, reuses same package)
- **Image refs**: `alpine@latest`, `ubuntu@latest` style
- **Secrets**: Out of scope. Future external secrets hydration system.
- **Dev tools**: Baked into image variants, not installed at boot via cloud-init runcmd
- **Docker**: Baked into `-dev` image variants. `--docker` flag applies security profile + enables the service. No `curl | sh` at boot.
- **Sudo**: NOPASSWD by default, opt-out via `--no-sudo`
- **Input validation**: `LaunchOpts.Validate()` enforces safe patterns for Name, Username, Workspace, Proxy
- **Workspace/proxy config**: Instance-level devices and config, not dynamic profiles

---

## Architecture: Five Layers

```
┌─────────────────────────────────────────────────────┐
│  Layer 5: CLI subcommand (myringa launch)            │
├─────────────────────────────────────────────────────┤
│  Layer 4: Go launcher (internal/provision/)          │
│           Orchestrates layers 1-3                    │
├─────────────────────────────────────────────────────┤
│  Layer 3: Cloud-init template (per-instance)         │
│           User creation, shell config                │
├─────────────────────────────────────────────────────┤
│  Layer 2: Composable profiles (YAML, stacked)        │
│           base, docker — static config only          │
├─────────────────────────────────────────────────────┤
│  Layer 1: Custom base images (built once)            │
│           Packages, mise, Claude Code baked in       │
│           Dev-tools variant: + oh-my-zsh, fzf, bat   │
└─────────────────────────────────────────────────────┘
```

---

## Layer 1: Custom Base Images

**What gets baked in** (stable, changes monthly at most):
- OS packages (~40 APK or APT packages)
- cloud-init (Alpine needs this added explicitly)
- mise runtime manager
- Claude Code
- zsh as default shell skeleton in `/etc/skel`

**Dev-tools variant adds:**
- Oh My Zsh (with zsh-autosuggestions plugin)
- fzf, bat via mise
- Docker engine (packages only — daemon starts when `--docker` flag is used at launch)
- Pre-configured `.zshrc` skeleton in `/etc/skel` with theme, plugins, aliases

**Four image variants:**

| Alias | Base | Dev tools? |
|-------|------|:---:|
| Alias | Base | Dev tools? | Docker? |
|-------|------|:---:|:---:|
| `myringa/alpine:latest` | `images:alpine/3.21` | No | No |
| `myringa/alpine-dev:latest` | `images:alpine/3.21` | Yes | Yes (packages only) |
| `myringa/ubuntu:latest` | `images:ubuntu/24.04` | No | No |
| `myringa/ubuntu-dev:latest` | `images:ubuntu/24.04` | Yes | Yes (packages only) |

Rationale: Dev tools (oh-my-zsh, fzf, bat) and Docker are stable software that changes rarely. Curling install scripts at boot is fragile — GitHub goes down, URLs change, installs are slow. Baking them in means instant boot and no network dependency. Docker packages are pre-installed in `-dev` images but the daemon only starts when `--docker` is passed at launch (which applies the security profile and enables the service).

**Build strategy: `incus publish`**

Not distrobuilder. Rationale: zero new tooling, you already know the workflow, and for a personal/small-team tool rebuilt monthly, snapshot-and-publish is the right level of engineering.

**Build script**: `infra/images/build.sh`

```
Usage: ./build.sh <alpine|ubuntu> [--dev] [--tag v2]

1. incus launch from upstream image
2. incus exec — install packages from packages-{distro}.txt
3. incus exec — install mise, Claude Code, configure /etc/skel
4. If --dev:
   a. Install Oh My Zsh + zsh-autosuggestions into /etc/skel
   b. mise use --global fzf@latest bat@latest
   c. Configure /etc/skel/.zshrc with theme, plugins, aliases
   d. Install Docker engine packages (apt-get/apk, no curl|sh)
   e. Disable docker service by default (enabled at launch via --docker)
5. incus stop
6. incus publish --alias myringa/{distro}[-dev]:latest
7. incus delete builder instance
```

~120 lines. The one imperative script that stays imperative — image building is inherently imperative.

**Package lists** as flat files for easy diffing:
- `infra/images/packages-alpine.txt`
- `infra/images/packages-ubuntu.txt`

---

## Layer 2: Composable Profiles

Profiles handle config and devices that compose via `--profile` stacking. Only **static, shared** config goes in profiles. Cloud-init does NOT go here (it doesn't merge across profiles — last one clobbers). Instance-specific config (workspace mounts, proxy) goes directly on the instance.

### `myringa-base` (always applied)

```yaml
config:
  limits.cpu: "4"
  limits.memory: 4GiB
description: Base myringa dev environment
devices:
  root:
    type: disk
    pool: default
    path: /
    size: 20GiB
```

### `myringa-docker` (opt-in)

```yaml
config:
  security.nesting: "true"
  security.syscalls.intercept.mknod: "true"
  security.syscalls.intercept.setxattr: "true"
  raw.lxc: lxc.apparmor.profile=unconfined
description: Docker-in-Incus support (requires AppArmor unconfined)
```

Note: `raw.lxc "lxc.apparmor.profile=unconfined"` is required — AppArmor blocks Docker-in-Incus on current kernels. This widens the container's security boundary: a container escape is more feasible with AppArmor unconfined. Acceptable for single-user dev containers on a local machine. Do not use this profile for multi-tenant or network-exposed containers.

### No dynamic profiles

Workspace mounts, proxy env vars, and idmap config are instance-specific by definition. They go directly on the instance as devices and config via the SDK. This avoids profile lifecycle management (creation, cleanup on instance delete) and keeps profiles for what they're good at: shared, reusable config.

### Profile storage

Static profiles live as YAML in `infra/profiles/` and are embedded into the Go binary via `//go:embed`. The launcher syncs them to the Incus daemon on first run or via `myringa profiles sync`.

---

## Layer 3: Cloud-init (Per-Instance)

Set as instance-level `cloud-init.user-data` at launch time. NOT in any profile (because cloud-init doesn't merge across profiles).

Cloud-init is now **lean** — user creation, shell dotfiles, and service enablement. Docker packages are baked into `-dev` images; cloud-init just enables the service and adds the user to the docker group.

Generated from a Go `text/template`:

```yaml
#cloud-config
users:
  - name: {{.Username}}
    uid: "{{.UID}}"
    groups: [sudo{{if .Docker}}, docker{{end}}]
    shell: /bin/zsh
    {{- if .Sudo}}
    sudo: ALL=(ALL) NOPASSWD:ALL
    {{- end}}
    no_create_home: false

write_files:
  - path: /home/{{.Username}}/.zprofile
    owner: "{{.UID}}:{{.GID}}"
    permissions: "0644"
    content: |
      [[ -d /workspace ]] && cd /workspace
  - path: /home/{{.Username}}/.zshrc
    owner: "{{.UID}}:{{.GID}}"
    permissions: "0644"
    content: |
      export PATH="$HOME/.local/bin:$PATH"
      eval "$(mise activate zsh)"
      # Trust /workspace for mise — assumes the mounted repo is trusted
      export MISE_TRUSTED_CONFIG_PATHS="/workspace"
      {{- if .DevTools}}
      [[ -f ~/.oh-my-zsh/oh-my-zsh.sh ]] && {
        export ZSH="$HOME/.oh-my-zsh"
        ZSH_THEME="dpoggi"
        plugins=(git zsh-autosuggestions)
        source $ZSH/oh-my-zsh.sh
        PROMPT="%{$fg[cyan]%}[incus]%{$reset_color%} ${PROMPT}"
      }
      alias f="fzf --preview 'bat {-1} --color=always'"
      {{- end}}

runcmd:
  {{- if .Docker}}
  - [systemctl, enable, --now, docker]
  - [usermod, -aG, docker, {{.Username}}]
  {{- end}}
  - [su, {{.Username}}, -lc, "cd /workspace && mise install 2>/dev/null || true"]
```

Docker is pre-installed in `-dev` images. The runcmd just enables the daemon and adds the user to the docker group — no network dependency, no `curl | sh`.

**Template lives at**: `infra/templates/cloud-init.yaml.tmpl` (embedded via `//go:embed`)

---

## Layer 4: Go Provision Package

### `internal/provision/`

Start with two files plus tests. Split only when a file grows past ~200 lines and has a clear seam.

```
internal/provision/
  provision.go       — LaunchOpts, Launch() orchestrator, profile sync,
                       workspace mount + idmap negotiation
  provision_test.go  — mock SDK tests for launch flow, profiles, idmap
  cloudinit.go       — template rendering
  cloudinit_test.go  — test output for all option combos
```

### LaunchOpts

```go
type LaunchOpts struct {
    Name        string   // required, must match [a-zA-Z0-9][a-zA-Z0-9-]*
    Distro      string   // "alpine" or "ubuntu" (default: "alpine")
    Docker      bool     // requires -dev image variant (has Docker pre-installed)
    DevTools    bool     // selects -dev image variant
    Sudo        bool     // NOPASSWD sudo (default: true, --no-sudo to disable)
    Proxy       string   // "host:port" or empty
    Workspace   string   // host path (default: cwd), must be absolute
    MountPath   string   // container path (default: /workspace)
    Username    string   // default: current user, must match POSIX [a-z_][a-z0-9_-]*
    UID         int      // default: os/user
    GID         int      // default: os/user
}

// Validate checks all fields for safe values. Called at the top of Launch().
// Rejects injection-prone inputs before they reach cloud-init templates or SDK calls.
func (o LaunchOpts) Validate() error {
    // Name: [a-zA-Z0-9][a-zA-Z0-9-]* (Incus naming rules)
    // Username: [a-z_][a-z0-9_-]* (POSIX)
    // Distro: must be "alpine" or "ubuntu"
    // Workspace: must be absolute path, must exist
    // MountPath: must be absolute path
    // Proxy: if set, must match host:port pattern
    // Docker: if true, requires DevTools (Docker is baked into -dev images)
}
```

### Launch() flow

```
func Launch(ctx context.Context, conn incus.InstanceServer, opts LaunchOpts) error

1. Resolve image alias:
   - DevTools=false → "myringa/{distro}:latest"
   - DevTools=true  → "myringa/{distro}-dev:latest"
2. Sync static profiles (myringa-base, myringa-docker) if missing
3. Build profile list:
   - Always: ["default", "myringa-base"]
   - If Docker: append "myringa-docker"
4. Render cloud-init from template
5. Create instance:
   api.InstancesPost{
     Name:   opts.Name,
     Source: api.InstanceSource{Type: "image", Alias: imageAlias},
     InstancePut: api.InstancePut{
       Profiles: profiles,
       Config: map[string]string{
         "cloud-init.user-data": renderedCloudInit,
       },
     },
   }
6. Add workspace device with idmap negotiation (instance-level):
   a. Try raw.idmap "both {UID} {GID}" + device add
   b. If fails: device add without idmap, warn user
7. If Proxy: set environment.HTTP_PROXY, environment.HTTPS_PROXY
   as instance-level config
8. Start instance
9. Wait for cloud-init to complete (see polling design below)
```

### Idmap negotiation

```go
func mountWorkspace(ctx context.Context, conn InstanceServer, name string, opts LaunchOpts) error {
    // Try raw.idmap first
    err := conn.UpdateInstance(name, api.InstancePut{
        Config: map[string]string{
            "raw.idmap": fmt.Sprintf("both %d %d", opts.UID, opts.GID),
        },
    }, "")

    device := map[string]string{
        "type":   "disk",
        "source": opts.Workspace,
        "path":   opts.MountPath,
    }

    // Try adding device
    // If start fails, remove idmap, retry without it, warn
}
```

~20 lines. Stays imperative because it's genuinely runtime-dependent.

### Cloud-init polling (concrete design)

```go
func waitForCloudInit(ctx context.Context, conn InstanceServer, name string, timeout time.Duration) error {
    deadline := time.After(timeout) // default: 5 minutes
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-deadline:
            return fmt.Errorf("cloud-init did not complete within %s", timeout)
        case <-ticker.C:
            // incus exec {name} -- cloud-init status --format json
            // Returns: {"status": "running|done|error", ...}
            result, err := execInInstance(conn, name,
                []string{"cloud-init", "status", "--format", "json"})
            if err != nil {
                continue // instance may not be ready yet
            }
            var status struct{ Status string `json:"status"` }
            json.Unmarshal(result, &status)
            switch status.Status {
            case "done":
                return nil
            case "error":
                // Fetch cloud-init logs for diagnostics
                logs, _ := execInInstance(conn, name,
                    []string{"cat", "/var/log/cloud-init-output.log"})
                return fmt.Errorf("cloud-init failed:\n%s", truncateLogs(logs, 20))
            }
            // "running" or parse error → keep polling
        }
    }
}
```

Key decisions:
- **5 minute timeout** — cloud-init with Docker install takes ~2 min, generous buffer
- **2 second poll interval** — not too aggressive, fast enough feedback
- **Error case**: fetch the last 20 lines of cloud-init logs so the user knows what failed
- **Partial failure**: cloud-init reports "error" even for partial failures; we surface the log and let the user decide

### Future: Two-phase provisioning

Current design blocks until cloud-init completes. The cloud-init template has a natural seam that supports a future `--no-wait` flag:

- **Phase 1 (fast, ~3-5s)**: `users:` + `write_files:` — user creation, shell config. Container is usable after this.
- **Phase 2 (slow, ~60-120s)**: `runcmd:` — Docker service enablement, `mise install`. Could run in background.

No code changes needed now — just preserve this template structure. Future work: add a `Wait bool` field to `LaunchOpts` (default true), return after container start when false, show provisioning progress in TUI.

---

## Layer 5: CLI Subcommand

### Extend main.go or add subcommands

The current `main.go` is a pure TUI launcher (18 lines). Two options:

Add a `launch` subcommand. If no subcommand, run TUI as before. One binary, shared `internal/` packages, simpler distribution.

```
myringa                                    # TUI dashboard (existing)
myringa launch mydev                       # create container
myringa launch mydev --ubuntu              # Ubuntu container
myringa launch mydev --docker --dev-tools  # with Docker + dev tools
myringa launch mydev --dry-run             # show what would happen
myringa images build alpine                # build custom image
myringa profiles sync                      # sync static profiles to Incus
```

### First-run UX note

The TUI (Plan 2) should include empty-state guidance when no instances exist: "No instances found. Press c to create one, or run: myringa launch <name>". The `myringa launch --help` output should serve as a complete getting-started guide with examples for common combos.

### Flag definitions

```
myringa launch [flags] <name>

  --distro string       OS distro: alpine, ubuntu (default "alpine")
  --docker              Enable Docker (implies --dev-tools, pre-installed in image)
  --dev-tools           Use dev image variant (oh-my-zsh, fzf, bat, Docker packages)
  --no-sudo             Disable passwordless sudo for the container user
  --proxy string        HTTP proxy host:port
  --workspace string    Host directory to mount (default: cwd)
  --mount-path string   Container mount point (default: /workspace)
  --dry-run             Show what would be done
```

---

## File Organization

```
/workspace/
  main.go                              # extend: subcommand routing
  internal/
    incus/
      client.go                        # extend: profile CRUD, richer CreateInstance
    provision/
      provision.go                     # LaunchOpts, Launch(), profiles, idmap, polling
      provision_test.go
      cloudinit.go                     # template rendering
      cloudinit_test.go
    ui/
      ...                              # existing, unchanged
  infra/
    profiles/
      myringa-base.yaml
      myringa-docker.yaml
    images/
      build.sh
      packages-alpine.txt
      packages-ubuntu.txt
    templates/
      cloud-init.yaml.tmpl
```

`infra/` contents are embedded into the binary via `//go:embed` in the provision package.

---

## Changes to Existing Code

### `internal/incus/client.go`

Extend the `Client` interface:

```go
// Add to interface:
CreateProfile(ctx context.Context, name string, config map[string]string, devices map[string]map[string]string) error
UpdateProfile(ctx context.Context, name string, config map[string]string) error
ProfileExists(ctx context.Context, name string) (bool, error)
CreateInstanceFull(ctx context.Context, req api.InstancesPost) error
UpdateInstanceConfig(ctx context.Context, name string, config map[string]string) error
AddDevice(ctx context.Context, instanceName, deviceName string, device map[string]string) error
ExecInstance(ctx context.Context, instanceName string, cmd []string) ([]byte, error)
StartInstance(ctx context.Context, name string) error  // already exists
```

### `main.go`

Add subcommand routing. If `os.Args[1]` is a known subcommand, dispatch to it. Otherwise, run TUI. Keep it simple — no cobra/urfave unless you want it.

---

## Migration Path (TDD — tests first, then implementation)

### Phase 1: Infrastructure (profiles + images)
1. Create `infra/` directory with profile YAMLs, package lists, build script
2. Build all four image variants (alpine, alpine-dev, ubuntu, ubuntu-dev) on host
3. Apply profiles manually, verify stacking works

### Phase 2: Cloud-init (`internal/provision/cloudinit.go`)
1. Write `cloudinit_test.go` first — test template rendering for:
   - Bare minimum (just username/UID/GID)
   - With Sudo=true (NOPASSWD line present)
   - With Sudo=false (no sudo line in output)
   - With Docker (user added to docker group, systemctl enable docker in runcmd, NO curl|sh)
   - With DevTools (dev image selected, zshrc has oh-my-zsh config)
   - With Docker + DevTools combined
   - Valid YAML output for all combos
2. Implement `cloudinit.go` to make tests pass

### Phase 3: Launcher (`internal/provision/provision.go`)
1. Write `provision_test.go` first — test:
   - **Validation**: Validate() rejects bad Name (special chars, empty), bad Username (non-POSIX), relative Workspace, malformed Proxy
   - **Validation**: Validate() rejects Docker=true without DevTools (Docker is baked into -dev images)
   - **Validation**: Validate() accepts good inputs
   - Image alias resolution (distro + devtools → correct alias)
   - Static profile sync (create if missing, update if changed)
   - Profile list assembly (base + optional docker)
   - Workspace device config with correct source/path
   - Idmap: raw.idmap success path
   - Idmap: raw.idmap failure → fallback to no mapping + prominent warning with permission implications
   - Proxy config set as instance-level config
   - Full Launch() with mock SDK (verify correct InstancesPost)
   - Dry-run mode returns plan without creating anything
2. Implement `provision.go` to make tests pass

### Phase 4: Client extensions (`internal/incus/client.go`)
1. Extend mock client in existing test helpers
2. Add profile CRUD, richer CreateInstance, ExecInstance methods
3. Tests alongside existing `model_test.go` patterns

### Phase 5: CLI subcommand
1. Write test for subcommand routing (no args → TUI, `launch` → provision)
2. Wire flags to `LaunchOpts` in `main.go`
3. End-to-end test: `myringa launch test-1 --ubuntu --docker --dev-tools`

### Phase 6: Integration test
1. One test that creates a real container against a live Incus daemon
2. Verifies: container exists, user created, cloud-init completed, workspace mounted
3. Tears down container after test
4. Gated behind `go test -tags=integration` so it doesn't run in CI without a daemon

### Phase 7: TUI integration
1. Add "create instance" flow to the TUI (new view mode)
2. Reuse `internal/provision/` package from the TUI
3. Form-style input for launch options, live output during provisioning

### Phase 8: Published images via OCI registry (future)

Pre-build and publish images to ghcr.io so users skip the local `build.sh` step entirely. Boot becomes near-instant — just cloud-init user creation + `mise install` from project config.

1. Extend `build.sh` with `--publish` flag that pushes to `ghcr.io/youruser/myringa/{distro}[-dev]:latest`
2. Update `Launch()` image resolution to check local Incus image store first, fall back to remote OCI pull + cache
3. CI workflow (GitHub Actions) to rebuild and publish images on a schedule (monthly) or on tag
4. `build.sh` becomes optional — only needed for custom/offline builds

If myringa gains users beyond the author, consider a simplestreams server for native `incus remote add myringa https://...` UX. Overkill until then.

### Phase 9: Update README
Document the new provisioning functionality:
- `myringa launch` usage and flags
- Image building workflow (`infra/images/build.sh`)
- Profile system (what ships, how to customize)
- Cloud-init overview (what it configures per-instance)
- Examples for common combos (bare Alpine, Ubuntu+Docker, dev-tools)

---

## What Stays Imperative and Why

| Component | Declarative? | Reason |
|-----------|:---:|--------|
| Package installation | Baked in image | Image build is imperative, consumption is declarative |
| Dev tools (omz, fzf, bat) | Baked in image | Image build is imperative, `--dev-tools` just picks the variant |
| Profile config | Yes | Static YAML, synced via SDK |
| Docker flags | Yes | Profile config keys |
| Docker packages | Baked in image | Pre-installed in `-dev` images, daemon enabled via cloud-init runcmd |
| User creation | Yes | Cloud-init |
| Shell setup | Yes | Cloud-init write_files |
| Workspace mount | **No** | Instance-level device, set via SDK at launch |
| Workspace idmap | **No** | Runtime detection — can't know which strategy works in advance |
| Proxy config | **No** | Instance-level config, host-specific values |
| Image building | **No** | `incus publish` is inherently imperative |
