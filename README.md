# ring (experimental)

Local development with [Incus](https://linuxcontainers.org/incus/)

Ring is a TUI and CLI wrapper around Incus to create secure local development environments with sensible defaults and a reasonably complete set of
tools. 

### Features
* Docker/Docker Compose *inside* an isolated Incus (formerly LXC) container
* Simplified, local development-focused API wrapper around Incus containers
* TUI for Incus container life cycle management
* Common dev tools included (mise, claude, fzf, bat, docker, nvim, etc)

Built with [Claude Code](https://claude.ai/claude-code) and Sonnet 4.6 + Opus 4.6 (Anthropic).

[![asciicast](https://asciinema.org/a/JHxpIO9BzBt7iZDS.svg)](https://asciinema.org/a/JHxpIO9BzBt7iZDS)

- **TUI dashboard** — live table of containers and VMs: CPU (% of allocated capacity), memory, state
- **`launch`** — one command to create a fully configured dev container: user account, passwordless sudo, workspace mount, oh-my-zsh, Docker, mise, claude
- **`images build`** — build custom ring images locally from upstream Alpine or Ubuntu

## Requirements

- Go 1.22+
- A running Incus daemon (`incus info` should work)

## Install

```sh
go build -o ring .
```

Or install directly:

```sh
go install .
```

## TUI dashboard

```sh
ring
```

Displays a live table of all Incus instances. Refreshes every 2 seconds.

### Main view

| Key | Action |
|-----|--------|
| `j` / `k` or arrow keys | Scroll down / up |
| `s` | Start (if stopped) or stop (if running) |
| `r` | Restart (running instances only) |
| `e` | Open a shell as your host user (`su - $USER` inside the container) |
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

## ring launch

Creates a new Incus dev container with:

- A user account matching your host user (UID/GID, username)
- `/bin/zsh` as the default shell with oh-my-zsh and `mise` activated
- Your current working directory (or `--workspace`) bind-mounted at `/workspace` (or `--mount-path`)
- UID/GID mapping so workspace files aren't owned by root inside the container
- Passwordless privilege escalation by default (`doas` on Alpine, `sudo` on Ubuntu)
- Docker-in-Incus support (always enabled via `ring-docker` profile)
- Dev tools pre-installed: fzf, bat, neovim, claude CLI

```sh
ring launch <name> [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--distro` | `alpine` | OS distro: `alpine` or `ubuntu` |
| `--enable-sudo` | on | Passwordless privilege escalation (`doas` on Alpine, `sudo` on Ubuntu) |
| `--proxy` | — | HTTP proxy as `host:port` (sets `HTTP_PROXY` / `HTTPS_PROXY`) |
| `--workspace` | cwd | Host directory to bind-mount |
| `--mount-path` | `/workspace` | Container mount point |
| `--dry-run` | off | Show what would be done without making changes |

### Examples

```sh
# Alpine container (default)
ring launch mydev

# Ubuntu container
ring launch mydev --distro ubuntu

# Mount a specific workspace directory
ring launch mydev --workspace ~/projects/myapp

# Preview what would be created
ring launch mydev --distro ubuntu --dry-run
```

### Auto-build

If the required image doesn't exist locally, `ring launch` will build it automatically before creating the container. This takes a few minutes on first run. Subsequent launches are fast.

You can also build images explicitly:

## ring images build

Builds a ring custom image and publishes it to the local Incus image store. If an image with the same alias already exists it is replaced.

```sh
ring images build <distro> [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--tag` | `latest` | Image tag |

### Examples

```sh
# Build the Alpine image
ring images build alpine

# Build Ubuntu with a custom tag
ring images build ubuntu --tag 2025-02
```

### What gets built

Every ring image includes:

- Base OS packages (curl, git, zsh, doas/sudo, ca-certificates, etc.)
- [mise](https://mise.jdx.dev/) at `/usr/local/bin/mise` for runtime version management
- [oh-my-zsh](https://ohmyz.sh/) + zsh-autosuggestions
- fzf, bat, neovim
- Docker CE (service disabled by default; enabled at launch via `ring-docker` profile)
- [claude](https://claude.ai/claude-code) CLI at `/usr/local/bin/claude`

### Image aliases

| Distro | Alias |
|--------|-------|
| Alpine | `ring/alpine:latest` |
| Ubuntu | `ring/ubuntu:latest` |

## Incus profiles

Two profiles are created automatically on first launch and applied to all ring containers:

- **`ring-base`** — CPU/memory limits, 20 GiB root disk
- **`ring-docker`** — security nesting + AppArmor unconfined (required for Docker-in-Incus; always applied)

## Development

```sh
go test ./...
```

---
