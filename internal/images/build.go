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
	Distro   string // "alpine" or "ubuntu"
	DevTools bool   // build the -dev variant
	Tag      string // image tag, default "latest"
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
		return upstreamRemote{server, protocol, "alpine/3.21", "images:alpine/3.21"}
	}
}

// UpstreamLabel returns the human-readable upstream image label (for display).
func UpstreamLabel(distro string) string {
	return upstream(distro).label
}

// TargetAlias returns the local image alias that Build() will publish.
func TargetAlias(distro string, devTools bool, tag string) string {
	if devTools {
		return fmt.Sprintf("ring/%s-dev:%s", distro, tag)
	}
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
	alias := TargetAlias(opts.Distro, opts.DevTools, opts.Tag)
	builder := builderName()

	fmt.Fprintf(out, "Building %s from %s\n", alias, src.label)

	// Always clean up the builder, even on failure.
	defer func() {
		fmt.Fprintf(out, "Cleaning up builder %s...\n", builder)
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

	// Step 3: Enable cloud-init services (Alpine only — Ubuntu enables them via systemd on install).
	if opts.Distro == "alpine" {
		fmt.Fprintf(out, "Enabling cloud-init services...\n")
		// cloud-init-local must be in sysinit; the rest run in default.
		cloudInitSvcs := []struct{ svc, runlevel string }{
			{"cloud-init-local", "sysinit"},
			{"cloud-init", "default"},
			{"cloud-config", "default"},
			{"cloud-final", "default"},
		}
		for _, s := range cloudInitSvcs {
			if err := c.ExecStream(ctx, builder, []string{"rc-update", "add", s.svc, s.runlevel}, out, out); err != nil {
				return fmt.Errorf("enabling cloud-init service %s: %w", s.svc, err)
			}
		}
	}

	// Step 4: Install mise.
	fmt.Fprintf(out, "Installing mise...\n")
	if err := c.ExecStream(ctx, builder, []string{"sh", "-c",
		"curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh",
	}, out, out); err != nil {
		return fmt.Errorf("installing mise: %w", err)
	}

	// Step 4: Configure /etc/skel.
	fmt.Fprintf(out, "Configuring /etc/skel...\n")
	for _, cmd := range [][]string{
		{"mkdir", "-p", "/etc/skel/.local/bin"},
		{"sh", "-c", `echo 'export PATH="$HOME/.local/bin:$PATH"' > /etc/skel/.zshrc`},
		{"sh", "-c", `echo 'eval "$(mise activate zsh)"' >> /etc/skel/.zshrc`},
	} {
		if err := c.ExecStream(ctx, builder, cmd, out, out); err != nil {
			return fmt.Errorf("configuring skel: %w", err)
		}
	}

	// Step 5 (dev only): dev tools.
	if opts.DevTools {
		if err := installDevTools(ctx, c, builder, opts.Distro, out); err != nil {
			return fmt.Errorf("installing dev tools: %w", err)
		}
	}

	// Step 6: Stop builder.
	fmt.Fprintf(out, "Stopping builder...\n")
	if err := c.StopInstance(ctx, builder); err != nil {
		return fmt.Errorf("stopping builder: %w", err)
	}

	// Step 7: Publish (replacing any existing image with the same alias).
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
		if err := c.ExecStream(ctx, builder, []string{"apk", "update"}, out, out); err != nil {
			return err
		}
		return c.ExecStream(ctx, builder, append([]string{"apk", "add", "--no-cache"}, pkgs...), out, out)
	default: // ubuntu
		if err := c.ExecStream(ctx, builder, []string{"apt-get", "update", "-q"}, out, out); err != nil {
			return err
		}
		return c.ExecStream(ctx, builder, append([]string{"apt-get", "install", "-y", "-q"}, pkgs...), out, out)
	}
}

func installDevTools(ctx context.Context, c BuildClient, builder, distro string, out io.Writer) error {
	fmt.Fprintf(out, "Installing dev tools (oh-my-zsh, fzf, bat, docker)...\n")

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

func installDockerPackages(ctx context.Context, c BuildClient, builder, distro string, out io.Writer) error {
	switch distro {
	case "alpine":
		return c.ExecStream(ctx, builder,
			[]string{"apk", "add", "--no-cache", "docker", "docker-compose"}, out, out)
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
		return nil
	}
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
