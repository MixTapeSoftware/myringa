package images

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"math/rand"
	"strings"
)

//go:embed embed
var embedFS embed.FS

// BuildClient is the interface Build() needs from the Incus connection.
type BuildClient interface {
	// LaunchBuilder creates and starts a builder instance from a remote image.
	// server is the full URL (e.g. "https://images.linuxcontainers.org"),
	// protocol is "simplestreams", alias is the image alias (e.g. "alpine/3.21").
	LaunchBuilder(ctx context.Context, name, server, protocol, alias string) error
	ExecStream(ctx context.Context, name string, cmd []string, stdout, stderr io.Writer) error
	StopInstance(ctx context.Context, name string) error
	ImageAliasExists(ctx context.Context, alias string) (bool, error)
	DeleteImageAlias(ctx context.Context, alias string) error
	PublishInstance(ctx context.Context, name, alias string) error
	DeleteInstance(ctx context.Context, name string) error
}

// BuildOpts holds parameters for building a ring custom image.
type BuildOpts struct {
	Distro string // "alpine" or "ubuntu"
	Tag    string // image tag, default "latest"
}

// Validate checks opts and fills in defaults.
func (o *BuildOpts) Validate() error {
	if o.Distro != "alpine" && o.Distro != "ubuntu" {
		return fmt.Errorf("distro %q is not supported: must be alpine or ubuntu", o.Distro)
	}
	if o.Tag == "" {
		o.Tag = "latest"
	}
	return nil
}

// upstreamRemote holds the components needed to launch from a remote image.
type upstreamRemote struct {
	server   string
	protocol string
	alias    string
	label    string // human-readable, e.g. "images:alpine/3.21"
}

// upstream returns the remote image parameters for the given distro.
func upstream(distro string) upstreamRemote {
	const (
		server   = "https://images.linuxcontainers.org"
		protocol = "simplestreams"
	)
	switch distro {
	case "ubuntu":
		return upstreamRemote{server, protocol, "ubuntu/24.04", "images:ubuntu/24.04"}
	default:
		return upstreamRemote{server, protocol, "alpine/3.23", "images:alpine/3.23"}
	}
}

// UpstreamLabel returns the human-readable upstream image label (for display).
func UpstreamLabel(distro string) string {
	return upstream(distro).label
}

// TargetAlias returns the local image alias that Build() will publish.
func TargetAlias(distro, tag string) string {
	return fmt.Sprintf("ring/%s:%s", distro, tag)
}

// LoadPackages reads the embedded package list for the given distro.
// Returns only non-blank, non-comment lines.
func LoadPackages(distro string) ([]string, error) {
	path := fmt.Sprintf("embed/packages-%s.txt", distro)
	data, err := embedFS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no package list for distro %q", distro)
	}

	var pkgs []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pkgs = append(pkgs, line)
	}
	return pkgs, nil
}

// Build builds a ring custom image and publishes it to the local Incus store.
// Progress is written to out. This is a blocking operation (~2-5 minutes).
func Build(ctx context.Context, c BuildClient, opts BuildOpts, out io.Writer) error {
	if err := opts.Validate(); err != nil {
		return err
	}

	src := upstream(opts.Distro)
	alias := TargetAlias(opts.Distro, opts.Tag)
	builder := builderName()

	fmt.Fprintf(out, "Building %s from %s\n", alias, src.label)

	// Always clean up the builder, even on failure.
	// Stop first — DeleteInstance fails on running instances.
	defer func() {
		fmt.Fprintf(out, "Cleaning up builder %s...\n", builder)
		_ = c.StopInstance(context.Background(), builder) // best-effort; may already be stopped
		if err := c.DeleteInstance(context.Background(), builder); err != nil {
			fmt.Fprintf(out, "WARNING: failed to delete builder %q: %v\n", builder, err)
		}
	}()

	// Step 1: Launch builder from remote upstream image.
	fmt.Fprintf(out, "Launching builder %s...\n", builder)
	if err := c.LaunchBuilder(ctx, builder, src.server, src.protocol, src.alias); err != nil {
		return fmt.Errorf("launching builder: %w", err)
	}

	// Step 2: Install base packages.
	pkgs, err := LoadPackages(opts.Distro)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Installing packages...\n")
	if err := installPackages(ctx, c, builder, opts.Distro, pkgs, out); err != nil {
		return fmt.Errorf("installing packages: %w", err)
	}

	// Step 3: Install gh CLI (Ubuntu only — Alpine gets it via packages-alpine.txt).
	fmt.Fprintf(out, "Installing gh CLI...\n")
	if err := installGHCLI(ctx, c, builder, opts.Distro, out); err != nil {
		return fmt.Errorf("installing gh CLI: %w", err)
	}

	// Step 4: Install mise.
	fmt.Fprintf(out, "Installing mise...\n")
	if err := c.ExecStream(ctx, builder, []string{"sh", "-c",
		"curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh",
	}, out, out); err != nil {
		return fmt.Errorf("installing mise: %w", err)
	}

	// Step 5: Configure /etc/skel.
	fmt.Fprintf(out, "Configuring /etc/skel...\n")
	for _, cmd := range [][]string{
		{"mkdir", "-p", "/etc/skel/.local/bin"},
		{"sh", "-c", `echo 'export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"' > /etc/skel/.zshrc`},
		{"sh", "-c", `echo 'eval "$(mise activate zsh)"' >> /etc/skel/.zshrc`},
	} {
		if err := c.ExecStream(ctx, builder, cmd, out, out); err != nil {
			return fmt.Errorf("configuring skel: %w", err)
		}
	}

	// Step 6: Install headless Chrome/Chromium for testing.
	// Alpine: already in package list. Ubuntu: Google Chrome apt repo.
	fmt.Fprintf(out, "Installing headless Chrome...\n")
	if err := installChrome(ctx, c, builder, opts.Distro, out); err != nil {
		return fmt.Errorf("installing chrome: %w", err)
	}

	// Step 7: Dev tools (oh-my-zsh, fzf, bat, neovim, docker).
	if err := installDevTools(ctx, c, builder, opts.Distro, out); err != nil {
		return fmt.Errorf("installing dev tools: %w", err)
	}

	// Step 8: Install Claude Code.
	// The official installer is a bash script; bash must be in the image.
	// When run as root, the binary lands in /root/.local/bin/claude.
	fmt.Fprintf(out, "Installing Claude Code...\n")
	if err := c.ExecStream(ctx, builder, []string{"sh", "-c",
		"curl -fsSL https://claude.ai/install.sh | bash",
	}, out, out); err != nil {
		return fmt.Errorf("installing claude: %w", err)
	}
	// Inspect what the installer created so we can diagnose wrapper vs binary.
	fmt.Fprintf(out, "Inspecting installed claude...\n")
	_ = c.ExecStream(ctx, builder, []string{"sh", "-c",
		`echo "=== ls -la ~/.local/bin/claude ===" && ls -la /root/.local/bin/claude && ` +
			`echo "=== file type ===" && file /root/.local/bin/claude 2>/dev/null || true && ` +
			`echo "=== head -1 ===" && head -1 /root/.local/bin/claude && ` +
			`echo "=== ls ~/.claude ===" && ls /root/.claude/ 2>/dev/null || true`,
	}, out, out)
	// Copy the actual ELF binary to /usr/local/bin. If ~/.local/bin/claude is a
	// wrapper script referencing ~/.claude/downloads/, find and copy the real binary.
	fmt.Fprintf(out, "Installing claude to /usr/local/bin...\n")
	if err := c.ExecStream(ctx, builder, []string{"sh", "-c",
		// Find the actual ELF binary: prefer the download (actual binary),
		// fall back to ~/.local/bin/claude if it's already the binary.
		`real=$(find /root/.claude/downloads -name 'claude-*' -type f -perm /111 2>/dev/null | sort -V | tail -1) && ` +
			`[ -z "$real" ] && real=/root/.local/bin/claude && ` +
			`[ -f "$real" ] || { echo "ERROR: claude binary not found at $real" >&2; exit 1; } && ` +
			`cp "$real" /usr/local/bin/claude && chmod 0755 /usr/local/bin/claude && ` +
			`echo "Installed from: $real"`,
	}, out, out); err != nil {
		return fmt.Errorf("installing claude to /usr/local/bin: %w", err)
	}

	// Step 9: Stop builder.
	fmt.Fprintf(out, "Stopping builder...\n")
	if err := c.StopInstance(ctx, builder); err != nil {
		return fmt.Errorf("stopping builder: %w", err)
	}

	// Step 10: Publish (replacing any existing image with the same alias).
	fmt.Fprintf(out, "Publishing locally as %s...\n", alias)
	if exists, err := c.ImageAliasExists(ctx, alias); err != nil {
		return fmt.Errorf("checking existing image: %w", err)
	} else if exists {
		fmt.Fprintf(out, "Replacing existing %s...\n", alias)
		if err := c.DeleteImageAlias(ctx, alias); err != nil {
			return fmt.Errorf("removing old image alias: %w", err)
		}
	}
	if err := c.PublishInstance(ctx, builder, alias); err != nil {
		return fmt.Errorf("publishing image: %w", err)
	}

	fmt.Fprintf(out, "Done: %s\n", alias)
	return nil
}

func installPackages(ctx context.Context, c BuildClient, builder, distro string, pkgs []string, out io.Writer) error {
	switch distro {
	case "alpine":
		// The linuxcontainers.org Alpine image may not have /etc/apk/repositories
		// populated. Write it explicitly from the container's own alpine-release version.
		setupRepos := `ver=$(cut -d. -f1,2 /etc/alpine-release) && ` +
			`printf 'https://dl-cdn.alpinelinux.org/alpine/v%s/main\nhttps://dl-cdn.alpinelinux.org/alpine/v%s/community\n' "$ver" "$ver" > /etc/apk/repositories`
		if err := c.ExecStream(ctx, builder, []string{"sh", "-c", setupRepos}, out, out); err != nil {
			return fmt.Errorf("configuring alpine repositories: %w", err)
		}
		// apk update may exit non-zero if a mirror is temporarily unavailable (exit 2).
		// Treat it as a warning — apk add will fail explicitly if packages are missing.
		if err := c.ExecStream(ctx, builder, []string{"apk", "update"}, out, out); err != nil {
			fmt.Fprintf(out, "WARNING: apk update returned an error (continuing): %v\n", err)
		}
		return c.ExecStream(ctx, builder, append([]string{"apk", "add"}, pkgs...), out, out)
	default: // ubuntu
		if err := c.ExecStream(ctx, builder, []string{"apt-get", "update", "-q"}, out, out); err != nil {
			return err
		}
		return c.ExecStream(ctx, builder, append([]string{"apt-get", "install", "-y", "-q"}, pkgs...), out, out)
	}
}

func installGHCLI(ctx context.Context, c BuildClient, builder, distro string, out io.Writer) error {
	if distro != "ubuntu" {
		return nil // Alpine: github-cli installed via packages-alpine.txt
	}
	for _, cmd := range [][]string{
		{"sh", "-c", "install -m 0755 -d /etc/apt/keyrings"},
		{"sh", "-c", "curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/etc/apt/keyrings/githubcli-archive-keyring.gpg"},
		{"sh", "-c", "chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg"},
		{"sh", "-c", `echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list`},
		{"apt-get", "update", "-q"},
		{"apt-get", "install", "-y", "-q", "gh"},
	} {
		if err := c.ExecStream(ctx, builder, cmd, out, out); err != nil {
			return fmt.Errorf("installing gh (ubuntu): %w", err)
		}
	}
	return nil
}

func installChrome(ctx context.Context, c BuildClient, builder, distro string, out io.Writer) error {
	if distro != "ubuntu" {
		return nil // Alpine: chromium installed via packages-alpine.txt
	}
	for _, cmd := range [][]string{
		{"sh", "-c", "install -m 0755 -d /etc/apt/keyrings"},
		{"sh", "-c", "curl -fsSL https://dl.google.com/linux/linux_signing_key.pub | gpg --dearmor -o /etc/apt/keyrings/google-chrome.gpg"},
		{"sh", "-c", "chmod go+r /etc/apt/keyrings/google-chrome.gpg"},
		{"sh", "-c", `echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/google-chrome.gpg] https://dl.google.com/linux/chrome/deb/ stable main" > /etc/apt/sources.list.d/google-chrome.list`},
		{"apt-get", "update", "-q"},
		{"apt-get", "install", "-y", "-q", "google-chrome-stable"},
	} {
		if err := c.ExecStream(ctx, builder, cmd, out, out); err != nil {
			return fmt.Errorf("installing google-chrome (ubuntu): %w", err)
		}
	}
	return nil
}

func installDevTools(ctx context.Context, c BuildClient, builder, distro string, out io.Writer) error {
	fmt.Fprintf(out, "Installing dev tools (oh-my-zsh, fzf, bat, nvim, docker)...\n")

	// fzf, bat, neovim
	if err := installDevPackages(ctx, c, builder, distro, out); err != nil {
		return err
	}

	// Oh My Zsh into /etc/skel (no curl|sh for runtime containers — build-time only)
	if err := c.ExecStream(ctx, builder, []string{"sh", "-c",
		`RUNZSH=no CHSH=no ZSH=/etc/skel/.oh-my-zsh sh -c "$(curl -fsSL https://raw.githubusercontent.com/ohmyzsh/ohmyzsh/master/tools/install.sh)"`,
	}, out, out); err != nil {
		return fmt.Errorf("installing oh-my-zsh: %w", err)
	}

	// zsh-autosuggestions plugin
	if err := c.ExecStream(ctx, builder, []string{"git", "clone",
		"https://github.com/zsh-users/zsh-autosuggestions",
		"/etc/skel/.oh-my-zsh/custom/plugins/zsh-autosuggestions",
	}, out, out); err != nil {
		return fmt.Errorf("installing zsh-autosuggestions: %w", err)
	}

	// Docker packages via package manager only (no curl|sh)
	if err := installDockerPackages(ctx, c, builder, distro, out); err != nil {
		return err
	}

	// Disable docker service by default (enabled at launch via --docker)
	_ = c.ExecStream(ctx, builder, disableDockerCmd(distro), out, out) // best-effort
	return nil
}

func installDevPackages(ctx context.Context, c BuildClient, builder, distro string, out io.Writer) error {
	switch distro {
	case "alpine":
		return c.ExecStream(ctx, builder,
			[]string{"apk", "add", "fzf", "bat", "neovim"}, out, out)
	default: // ubuntu
		if err := c.ExecStream(ctx, builder,
			[]string{"apt-get", "install", "-y", "-q", "fzf", "bat", "neovim"}, out, out); err != nil {
			return err
		}
		// Ubuntu installs bat as batcat; symlink so 'bat' works everywhere.
		return c.ExecStream(ctx, builder,
			[]string{"ln", "-sf", "/usr/bin/batcat", "/usr/local/bin/bat"}, out, out)
	}
}

// appArmorMaskScript returns a shell script that installs a boot-time service
// to mask /sys/module/apparmor/parameters/enabled inside the container.
// Docker reads that file; if it sees "Y" it tries to load the docker-default
// AppArmor profile via securityfs, which isn't accessible inside Incus containers.
// Setting it to "N" makes Docker skip AppArmor entirely.
// (The container's actual security boundary is the Incus/LXC AppArmor profile on the host.)
func appArmorMaskScript(distro string) string {
	if distro == "alpine" {
		return `cat > /etc/init.d/mask-apparmor <<'EOF'
#!/sbin/openrc-run
description="Mask AppArmor enabled flag for Docker-in-Incus"
depend() { before docker containerd; keyword -stop; }
start() {
    echo N > /run/apparmor_disabled
    mount --bind /run/apparmor_disabled /sys/module/apparmor/parameters/enabled
}
EOF
chmod +x /etc/init.d/mask-apparmor
rc-update add mask-apparmor boot`
	}
	// ubuntu — systemd
	return `cat > /etc/systemd/system/mask-apparmor.service <<'EOF'
[Unit]
Description=Mask AppArmor enabled flag for Docker-in-Incus
DefaultDependencies=no
Before=docker.service containerd.service docker.socket

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c "echo N > /run/apparmor_disabled && mount --bind /run/apparmor_disabled /sys/module/apparmor/parameters/enabled"

[Install]
WantedBy=multi-user.target
EOF
systemctl enable mask-apparmor.service`
}

func installDockerPackages(ctx context.Context, c BuildClient, builder, distro string, out io.Writer) error {
	switch distro {
	case "alpine":
		if err := c.ExecStream(ctx, builder,
			[]string{"apk", "add", "docker", "docker-compose"}, out, out); err != nil {
			return err
		}
	default: // ubuntu — add Docker apt repo then install
		for _, cmd := range [][]string{
			{"sh", "-c", "install -m 0755 -d /etc/apt/keyrings"},
			{"sh", "-c", "curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg"},
			{"sh", "-c", "chmod a+r /etc/apt/keyrings/docker.gpg"},
			{"sh", "-c",
				`echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] ` +
					`https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" ` +
					`> /etc/apt/sources.list.d/docker.list`},
			{"apt-get", "update", "-q"},
			{"apt-get", "install", "-y", "-q",
				"docker-ce", "docker-ce-cli", "containerd.io",
				"docker-buildx-plugin", "docker-compose-plugin"},
		} {
			if err := c.ExecStream(ctx, builder, cmd, out, out); err != nil {
				return fmt.Errorf("installing docker packages: %w", err)
			}
		}
	}
	// Install the AppArmor mask service so Docker doesn't try to load profiles
	// via securityfs (which isn't accessible inside Incus containers).
	if err := c.ExecStream(ctx, builder, []string{"sh", "-c", appArmorMaskScript(distro)}, out, out); err != nil {
		return fmt.Errorf("installing AppArmor mask service: %w", err)
	}
	return nil
}

func disableDockerCmd(distro string) []string {
	if distro == "alpine" {
		return []string{"rc-update", "del", "docker", "default"}
	}
	return []string{"systemctl", "disable", "docker"}
}

func builderName() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return "ring-builder-" + string(b)
}
