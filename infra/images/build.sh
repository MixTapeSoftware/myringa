#!/usr/bin/env bash
# build.sh — Build and publish myringa custom Incus images.
#
# Usage: ./build.sh <alpine|ubuntu> [--dev] [--tag <tag>]
#
# Examples:
#   ./build.sh alpine                 # build myringa/alpine:latest
#   ./build.sh ubuntu --dev           # build myringa/ubuntu-dev:latest
#   ./build.sh alpine --dev --tag v2  # build myringa/alpine-dev:v2

set -euo pipefail

# ── Args ──────────────────────────────────────────────────────────────────────

DISTRO=""
DEV=false
TAG="latest"

while [[ $# -gt 0 ]]; do
  case "$1" in
    alpine|ubuntu) DISTRO="$1" ;;
    --dev)         DEV=true ;;
    --tag)         shift; TAG="$1" ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
  shift
done

if [[ -z "$DISTRO" ]]; then
  echo "Usage: $0 <alpine|ubuntu> [--dev] [--tag <tag>]" >&2
  exit 1
fi

# ── Config ────────────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILDER_NAME="myringa-builder-$$"

if [[ "$DEV" == "true" ]]; then
  ALIAS="myringa/${DISTRO}-dev:${TAG}"
  VARIANT="${DISTRO}-dev"
else
  ALIAS="myringa/${DISTRO}:${TAG}"
  VARIANT="${DISTRO}"
fi

case "$DISTRO" in
  alpine) UPSTREAM="images:alpine/3.21" ;;
  ubuntu) UPSTREAM="images:ubuntu/24.04" ;;
esac

echo "Building: ${ALIAS} from ${UPSTREAM}"

# ── Cleanup on exit ───────────────────────────────────────────────────────────

cleanup() {
  echo "Cleaning up builder instance..."
  incus delete "${BUILDER_NAME}" --force 2>/dev/null || true
}
trap cleanup EXIT

# ── Step 1: Launch builder ────────────────────────────────────────────────────

echo "Launching builder: ${BUILDER_NAME}"
incus launch "${UPSTREAM}" "${BUILDER_NAME}"
sleep 5  # wait for init

# ── Step 2: Install base packages ────────────────────────────────────────────

PACKAGES_FILE="${SCRIPT_DIR}/packages-${DISTRO}.txt"
PACKAGES=$(grep -v '^#' "${PACKAGES_FILE}" | grep -v '^$' | tr '\n' ' ')

echo "Installing packages..."
case "$DISTRO" in
  alpine)
    incus exec "${BUILDER_NAME}" -- apk update
    incus exec "${BUILDER_NAME}" -- apk add --no-cache $PACKAGES
    ;;
  ubuntu)
    incus exec "${BUILDER_NAME}" -- apt-get update -q
    incus exec "${BUILDER_NAME}" -- apt-get install -y -q $PACKAGES
    ;;
esac

# ── Step 3: Install mise ──────────────────────────────────────────────────────

echo "Installing mise..."
incus exec "${BUILDER_NAME}" -- bash -c \
  'curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh'

# ── Step 4: Install Claude Code ───────────────────────────────────────────────

echo "Installing Claude Code..."
incus exec "${BUILDER_NAME}" -- bash -c \
  'npm install -g @anthropic-ai/claude-code 2>/dev/null || true'

# ── Step 5: Configure /etc/skel ──────────────────────────────────────────────

echo "Configuring /etc/skel..."
incus exec "${BUILDER_NAME}" -- bash -c 'mkdir -p /etc/skel/.local/bin'
incus exec "${BUILDER_NAME}" -- bash -c 'echo "export PATH=\"\$HOME/.local/bin:\$PATH\"" > /etc/skel/.zshrc'

# ── Step 6 (dev only): Dev tools ─────────────────────────────────────────────

if [[ "$DEV" == "true" ]]; then
  echo "Installing dev tools (oh-my-zsh, fzf, bat, docker packages)..."

  # Oh My Zsh into /etc/skel
  incus exec "${BUILDER_NAME}" -- bash -c \
    'RUNZSH=no CHSH=no ZSH=/etc/skel/.oh-my-zsh sh -c "$(curl -fsSL https://raw.githubusercontent.com/ohmyzsh/ohmyzsh/master/tools/install.sh)"'

  # zsh-autosuggestions plugin
  incus exec "${BUILDER_NAME}" -- bash -c \
    'git clone https://github.com/zsh-users/zsh-autosuggestions /etc/skel/.oh-my-zsh/custom/plugins/zsh-autosuggestions'

  # fzf + bat via mise (global, into /usr/local)
  incus exec "${BUILDER_NAME}" -- bash -c \
    'MISE_DATA_DIR=/usr/local/share/mise /usr/local/bin/mise use --global fzf@latest bat@latest'

  # Docker engine packages (no curl|sh)
  case "$DISTRO" in
    alpine)
      incus exec "${BUILDER_NAME}" -- apk add --no-cache docker docker-compose
      incus exec "${BUILDER_NAME}" -- rc-update del docker default 2>/dev/null || true
      ;;
    ubuntu)
      incus exec "${BUILDER_NAME}" -- bash -c \
        'install -m 0755 -d /etc/apt/keyrings && \
         curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg && \
         chmod a+r /etc/apt/keyrings/docker.gpg && \
         echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
           https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" \
           > /etc/apt/sources.list.d/docker.list && \
         apt-get update -q && \
         apt-get install -y -q docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin && \
         systemctl disable docker 2>/dev/null || true'
      ;;
  esac
fi

# ── Step 7: Stop and publish ──────────────────────────────────────────────────

echo "Stopping builder..."
incus stop "${BUILDER_NAME}"

echo "Publishing image as: ${ALIAS}"
incus publish "${BUILDER_NAME}" --alias "${ALIAS}"

echo "Done: ${ALIAS}"
