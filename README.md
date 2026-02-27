# myringa

Built with [Claude Code](https://claude.ai/claude-code) and Sonnet 4.6 + Opus 4.6 (Anthropic).

A terminal dashboard and provisioning CLI for [Incus](https://linuxcontainers.org/incus/) containers.

- **TUI dashboard** — live table of containers and VMs: CPU (% of allocated capacity), memory, state
- **`launch`** — one command to create a fully configured dev container: user account, sudo, workspace mount, proxy, Docker, dev tools
- **`images build`** — build custom myringa images locally from upstream Alpine or Ubuntu

## Requirements

- Go 1.22+
- A running Incus daemon (`incus info` should work)

## Install

```sh
go build -o myringa .
```

Or install directly:

```sh
go install .
```

## TUI dashboard

```sh
myringa
```

Displays a live table of all Incus instances. Refreshes every 2 seconds.

### Main view

| Key | Action |
|-----|--------|
| `j` / `k` or arrow keys | Scroll down / up |
| `s` | Start (if stopped) or stop (if running) |
| `r` | Restart (running instances only) |
| `e` | Open a shell (`incus exec … -- sh`) |
| `d` or `Enter` | Open detail view (running instances only) |
| `S` | Create a snapshot (prompts for name) |
| `x` | Delete instance (confirms first) |
| `q` | Quit |
| `Ctrl+C` | Quit |

### Detail view

Shows instance status, CPU/memory/disk metrics, IP address, and a snapshot table.

| Key | Action |
|-----|--------|
| `j` / `k` or arrow keys | Scroll snapshot list |
| `c` | Create a snapshot (prompts for name) |
| `r` | Restore selected snapshot (confirms first) |
| `d` | Delete selected snapshot (confirms first) |
| `Esc` | Back to main view |

### Overlays

Confirm prompts (`y` / `n` or `Esc`) appear before destructive actions. Snapshot name prompts accept free text and submit on `Enter` or cancel on `Esc`.

CPU is shown as a percentage of the container's total allocated capacity (e.g. a container with `limits.cpu: 4` at full single-core load shows 25%).

## myringa launch

Creates a new Incus dev container with:

- A user account matching your host user (UID/GID, username)
- `/bin/zsh` as the default shell with `mise` activated
- Your current working directory (or `--workspace`) bind-mounted at `/workspace` (or `--mount-path`)
- UID/GID mapping so workspace files aren't owned by root inside the container
- Passwordless sudo (disable with `--no-sudo`)
- Optional Docker-in-Incus support
- Optional dev tools (oh-my-zsh, fzf, bat, zsh-autosuggestions, Docker packages)

```sh
myringa launch <name> [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--distro` | `alpine` | OS distro: `alpine` or `ubuntu` |
| `--dev-tools` | off | Dev image variant: oh-my-zsh, fzf, bat, Docker packages |
| `--docker` | off | Enable Docker (implies `--dev-tools`) |
| `--no-sudo` | off | Disable passwordless sudo |
| `--proxy` | — | HTTP proxy as `host:port` (sets `HTTP_PROXY` / `HTTPS_PROXY`) |
| `--workspace` | cwd | Host directory to bind-mount |
| `--mount-path` | `/workspace` | Container mount point |
| `--dry-run` | off | Show what would be done without making changes |

### Examples

```sh
# Minimal Alpine container
myringa launch mydev

# Ubuntu with Docker support
myringa launch mydev --distro ubuntu --docker

# Alpine dev container with a specific workspace
myringa launch mydev --dev-tools --workspace ~/projects/myapp

# Preview what would be created
myringa launch mydev --distro ubuntu --docker --dry-run
```

### Auto-build

If the required image doesn't exist locally, `myringa launch` will build it automatically before creating the container. This takes a few minutes on first run. Subsequent launches are fast.

You can also build images explicitly:

## myringa images build

Builds a myringa custom image and publishes it to the local Incus image store. If an image with the same alias already exists it is replaced.

```sh
myringa images build <distro> [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dev` | off | Build the `-dev` variant (includes oh-my-zsh, fzf, bat, Docker packages) |
| `--tag` | `latest` | Image tag |

### Examples

```sh
# Build the base Alpine image
myringa images build alpine

# Build the Alpine dev variant
myringa images build alpine --dev

# Build Ubuntu with a custom tag
myringa images build ubuntu --tag 2025-02
```

### What gets built

Base images (`alpine`, `ubuntu`):

- Base OS packages (curl, git, zsh, ca-certificates, cloud-init, etc.)
- [mise](https://mise.jdx.dev/) at `/usr/local/bin/mise` for runtime version management
- `/etc/skel` configured with `.zshrc` (mise activated)

Dev images (`alpine-dev`, `ubuntu-dev`) additionally include:

- [oh-my-zsh](https://ohmyz.sh/) + zsh-autosuggestions in `/etc/skel`
- fzf, bat
- Docker CE (service disabled by default; enabled at launch with `--docker`)

### Image aliases

| Distro | Variant | Alias |
|--------|---------|-------|
| Alpine | base | `myringa/alpine:latest` |
| Alpine | dev | `myringa/alpine-dev:latest` |
| Ubuntu | base | `myringa/ubuntu:latest` |
| Ubuntu | dev | `myringa/ubuntu-dev:latest` |

## Incus profiles

Two profiles are created automatically on first launch and shared across all myringa containers:

- **`myringa-base`** — CPU/memory limits, 20 GiB root disk
- **`myringa-docker`** — security nesting + AppArmor unconfined (required for Docker-in-Incus)

## Development

```sh
go test ./...
```

---
