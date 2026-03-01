package provision

import (
	"context"
	"embed"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

//go:embed embed/profiles
var profilesFS embed.FS

// ErrImageNotFound is returned by Launch when the required local image alias
// doesn't exist. The caller can use this to trigger an auto-build.
type ErrImageNotFound struct {
	Alias  string
	Distro string
}

func (e *ErrImageNotFound) Error() string {
	return fmt.Sprintf("image %q not found in local Incus image store", e.Alias)
}

// Client is the interface provision needs from the Incus connection.
// The real implementation lives in internal/incus; tests use a mock.
type Client interface {
	ProfileExists(ctx context.Context, name string) (bool, error)
	CreateProfile(ctx context.Context, name string, yamlData string) error
	ImageAliasExists(ctx context.Context, alias string) (bool, error)
	CreateInstanceFull(ctx context.Context, req InstanceRequest) error
	UpdateInstanceConfig(ctx context.Context, name string, config map[string]string) error
	AddDevice(ctx context.Context, instanceName, deviceName string, device map[string]string) error
	StartInstance(ctx context.Context, name string) error
	ExecInstance(ctx context.Context, name string, cmd []string) ([]byte, error)
	WriteFile(ctx context.Context, instance, path string, content []byte, uid, gid int, mode os.FileMode) error
}

// InstanceRequest describes the parameters for creating a new instance.
type InstanceRequest struct {
	Name       string
	ImageAlias string
	Profiles   []string
	Config     map[string]string
}

// LaunchOpts holds all parameters for launching a new dev container.
type LaunchOpts struct {
	Name      string // required; [a-zA-Z0-9][a-zA-Z0-9-]*
	Distro    string // "alpine" or "ubuntu"
	Sudo      bool   // NOPASSWD sudo (default: true)
	Proxy     string // "host:port" or empty
	Workspace string // absolute host path (default: cwd)
	MountPath string // absolute container path (default: /workspace)
	Username  string // POSIX [a-z_][a-z0-9_-]*
	UID       int
	GID       int
	DryRun    bool // if true, validate only — make no API calls
}

var (
	nameRe     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`)
	usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	proxyRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*:[0-9]+$`)
)

// Validate checks all fields for safe, consistent values.
// Called at the top of Launch() before any API calls.
func (o LaunchOpts) Validate() error {
	if o.Name == "" {
		return fmt.Errorf("Name must not be empty")
	}
	if !nameRe.MatchString(o.Name) {
		return fmt.Errorf("Name %q is invalid: must match [a-zA-Z0-9][a-zA-Z0-9-]*", o.Name)
	}
	if !usernameRe.MatchString(o.Username) {
		return fmt.Errorf("Username %q is invalid: must match POSIX [a-z_][a-z0-9_-]*", o.Username)
	}
	if o.Distro != "alpine" && o.Distro != "ubuntu" {
		return fmt.Errorf("Distro %q is not supported: must be alpine or ubuntu", o.Distro)
	}
	if !strings.HasPrefix(o.Workspace, "/") {
		return fmt.Errorf("Workspace must be an absolute path, got %q", o.Workspace)
	}
	if !strings.HasPrefix(o.MountPath, "/") {
		return fmt.Errorf("MountPath must be an absolute path, got %q", o.MountPath)
	}
	if o.Proxy != "" && !proxyRe.MatchString(o.Proxy) {
		return fmt.Errorf("Proxy %q is invalid: must be host:port (no scheme)", o.Proxy)
	}
	return nil
}

// ImageAlias resolves the local Incus image alias.
func ImageAlias(distro string) string {
	return fmt.Sprintf("ring/%s:latest", distro)
}

// BuildProfiles returns the ordered profile list for a new instance.
// ring-docker is always included — all ring containers run with Docker support.
func BuildProfiles() []string {
	return []string{"default", "ring-base", "ring-docker"}
}

// WorkspaceDevice returns an Incus device map for a workspace disk mount.
func WorkspaceDevice(hostPath, containerPath string) map[string]string {
	return map[string]string{
		"type":   "disk",
		"source": hostPath,
		"path":   containerPath,
	}
}

// WorkspaceDeviceWithShift returns a workspace device map with shift=true,
// which uses kernel-level idmapped mounts (Linux 5.12+, no subuid/subgid required).
func WorkspaceDeviceWithShift(hostPath, containerPath string) map[string]string {
	return map[string]string{
		"type":   "disk",
		"source": hostPath,
		"path":   containerPath,
		"shift":  "true",
	}
}

// SyncProfiles ensures both ring static profiles exist in the Incus daemon.
// Profiles are embedded from embed/profiles/. Existing profiles are skipped.
func SyncProfiles(ctx context.Context, c Client) error {
	profiles := []struct {
		name string
		path string
	}{
		{"ring-base", "embed/profiles/ring-base.yaml"},
		{"ring-docker", "embed/profiles/ring-docker.yaml"},
	}

	for _, p := range profiles {
		exists, err := c.ProfileExists(ctx, p.name)
		if err != nil {
			return fmt.Errorf("checking profile %q: %w", p.name, err)
		}
		if exists {
			continue
		}
		data, err := profilesFS.ReadFile(p.path)
		if err != nil {
			return fmt.Errorf("reading embedded profile %q: %w", p.path, err)
		}
		if err := c.CreateProfile(ctx, p.name, string(data)); err != nil {
			return fmt.Errorf("creating profile %q: %w", p.name, err)
		}
	}
	return nil
}

// sudoGroup returns the distro-specific group name that grants passwordless sudo.
// Alpine uses wheel; Ubuntu (and Debian-family) uses sudo.
func sudoGroup(distro string) string {
	if distro == "alpine" {
		return "wheel"
	}
	return "sudo"
}

// Launch provisions a new Incus dev container with the given opts.
// Progress and warnings are written to out.
// Steps: validate → sync profiles → create instance → idmap → workspace → start → provision user.
func Launch(ctx context.Context, c Client, opts LaunchOpts, out io.Writer) error {
	if err := opts.Validate(); err != nil {
		return fmt.Errorf("invalid opts: %w", err)
	}

	if err := SyncProfiles(ctx, c); err != nil {
		return fmt.Errorf("syncing profiles: %w", err)
	}

	imageAlias := ImageAlias(opts.Distro)

	exists, err := c.ImageAliasExists(ctx, imageAlias)
	if err != nil {
		return fmt.Errorf("checking image alias: %w", err)
	}
	if !exists {
		return &ErrImageNotFound{Alias: imageAlias, Distro: opts.Distro}
	}

	config := map[string]string{}
	if opts.Proxy != "" {
		proxyURL := "http://" + opts.Proxy
		config["environment.HTTP_PROXY"] = proxyURL
		config["environment.HTTPS_PROXY"] = proxyURL
	}

	req := InstanceRequest{
		Name:       opts.Name,
		ImageAlias: imageAlias,
		Profiles:   BuildProfiles(),
		Config:     config,
	}
	if err := c.CreateInstanceFull(ctx, req); err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	// Workspace mount strategy (try best option, degrade gracefully):
	//   1. shift=true  — kernel idmapped mounts (Linux 5.12+); cleanest, no subuid/subgid needed.
	//   2. raw.idmap   — AddDevice succeeds but StartInstance may still fail on older kernels.
	//   3. plain mount — no UID mapping; workspace files appear owned by root inside container.
	if err := c.AddDevice(ctx, opts.Name, "workspace", WorkspaceDeviceWithShift(opts.Workspace, opts.MountPath)); err != nil {
		// shift=true not supported; try raw.idmap + plain device.
		_ = c.UpdateInstanceConfig(ctx, opts.Name, map[string]string{
			"raw.idmap": fmt.Sprintf("both %d %d", opts.UID, opts.GID),
		})
		if addErr := c.AddDevice(ctx, opts.Name, "workspace", WorkspaceDevice(opts.Workspace, opts.MountPath)); addErr != nil {
			return fmt.Errorf("adding workspace device: %w", addErr)
		}
	}

	if err := c.StartInstance(ctx, opts.Name); err != nil {
		if strings.Contains(err.Error(), "idmap") {
			// Kernel doesn't support idmapped mounts. Fall back to a privileged container:
			// security.privileged=true removes the user namespace so UIDs pass through
			// directly (host UID 1000 = container UID 1000) and disk mounts work without
			// any idmapping. Acceptable trade-off for a local dev container.
			fmt.Fprintf(out, "WARNING: idmapped mounts not supported — using security.privileged=true\n")
			_ = c.UpdateInstanceConfig(ctx, opts.Name, map[string]string{
				"raw.idmap":           "",
				"security.privileged": "true",
			})
			if err2 := c.StartInstance(ctx, opts.Name); err2 != nil {
				return fmt.Errorf("starting instance: %w", err2)
			}
		} else {
			return fmt.Errorf("starting instance: %w", err)
		}
	}

	if err := provisionUser(ctx, c, opts); err != nil {
		return fmt.Errorf("provisioning user: %w", err)
	}

	return nil
}

// DryRun validates opts and returns a human-readable description of what Launch would do.
// Makes no API calls.
func DryRun(_ context.Context, opts LaunchOpts) (string, error) {
	if err := opts.Validate(); err != nil {
		return "", fmt.Errorf("invalid opts: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Dry-run: would create instance %q\n", opts.Name)
	fmt.Fprintf(&b, "  Image:     %s\n", ImageAlias(opts.Distro))
	fmt.Fprintf(&b, "  Profiles:  %s\n", strings.Join(BuildProfiles(), ", "))
	fmt.Fprintf(&b, "  User:      %s (UID=%d, GID=%d)\n", opts.Username, opts.UID, opts.GID)
	fmt.Fprintf(&b, "  Sudo:      %s\n", strconv.FormatBool(opts.Sudo))
	fmt.Fprintf(&b, "  Workspace: %s → %s\n", opts.Workspace, opts.MountPath)
	if opts.Proxy != "" {
		fmt.Fprintf(&b, "  Proxy:     %s\n", opts.Proxy)
	}
	return b.String(), nil
}

// provisionUser creates the dev user and configures their environment
// synchronously after the container starts — no cloud-init required.
func provisionUser(ctx context.Context, c Client, opts LaunchOpts) error {
	name := opts.Name
	u := opts.Username
	uid := opts.UID
	gid := opts.GID

	switch opts.Distro {
	case "alpine":
		// Evict any existing user/group occupying our target UID/GID.
		c.ExecInstance(ctx, name, []string{"sh", "-c", fmt.Sprintf(
			`g=$(getent group %d 2>/dev/null | cut -d: -f1); [ -n "$g" ] && [ "$g" != %q ] && delgroup "$g" 2>/dev/null; true`, gid, u)})
		c.ExecInstance(ctx, name, []string{"sh", "-c", fmt.Sprintf(
			`u=$(getent passwd %d 2>/dev/null | cut -d: -f1); [ -n "$u" ] && [ "$u" != %q ] && deluser "$u" 2>/dev/null; true`, uid, u)})
		// Alpine's adduser validates the shell against /etc/shells; zsh isn't added automatically.
		c.ExecInstance(ctx, name, []string{"sh", "-c", "grep -qxF /bin/zsh /etc/shells || echo /bin/zsh >> /etc/shells"})
		c.ExecInstance(ctx, name, []string{"addgroup", "-g", strconv.Itoa(gid), u})
		if _, err := c.ExecInstance(ctx, name, []string{
			"adduser", "-D", "-u", strconv.Itoa(uid), "-G", u, "-s", "/bin/zsh", u,
		}); err != nil {
			return fmt.Errorf("adduser: %w", err)
		}
		c.ExecInstance(ctx, name, []string{"adduser", u, sudoGroup(opts.Distro)}) // best-effort
	default: // ubuntu
		// Evict any existing user/group occupying our target UID/GID.
		c.ExecInstance(ctx, name, []string{"sh", "-c", fmt.Sprintf(
			`u=$(getent passwd %d 2>/dev/null | cut -d: -f1); [ -n "$u" ] && [ "$u" != %q ] && userdel -r "$u" 2>/dev/null; true`, uid, u)})
		c.ExecInstance(ctx, name, []string{"sh", "-c", fmt.Sprintf(
			`g=$(getent group %d 2>/dev/null | cut -d: -f1); [ -n "$g" ] && [ "$g" != %q ] && groupdel "$g" 2>/dev/null; true`, gid, u)})
		c.ExecInstance(ctx, name, []string{"groupadd", "-g", strconv.Itoa(gid), u}) // best-effort
		if _, err := c.ExecInstance(ctx, name, []string{
			"useradd", "-m", "-u", strconv.Itoa(uid), "-g", strconv.Itoa(gid), "-s", "/bin/zsh", u,
		}); err != nil {
			return fmt.Errorf("useradd: %w", err)
		}
		c.ExecInstance(ctx, name, []string{"usermod", "-aG", sudoGroup(opts.Distro), u}) // best-effort
	}

	if opts.Sudo {
		if opts.Distro == "alpine" {
			// Alpine uses doas, not sudo.
			doasConf := []byte("permit nopass " + u + "\n")
			if err := c.WriteFile(ctx, name, "/etc/doas.conf", doasConf, 0, 0, 0644); err != nil {
				return fmt.Errorf("writing doas.conf: %w", err)
			}
		} else {
			c.ExecInstance(ctx, name, []string{"mkdir", "-p", "/etc/sudoers.d"})
			sudoLine := []byte(u + " ALL=(ALL) NOPASSWD:ALL\n")
			if err := c.WriteFile(ctx, name, "/etc/sudoers.d/"+u, sudoLine, 0, 0, 0440); err != nil {
				return fmt.Errorf("writing sudoers: %w", err)
			}
		}
	}

	zprofile := []byte("export MISE_TRUSTED_CONFIG_PATHS=\"/workspace\"\n[[ -d /workspace ]] && cd /workspace\n")
	if err := c.WriteFile(ctx, name, "/home/"+u+"/.zprofile", zprofile, uid, gid, 0644); err != nil {
		return fmt.Errorf("writing .zprofile: %w", err)
	}

	zshrc := []byte(renderZshrc(opts))
	if err := c.WriteFile(ctx, name, "/home/"+u+"/.zshrc", zshrc, uid, gid, 0644); err != nil {
		return fmt.Errorf("writing .zshrc: %w", err)
	}

	// Verify the user was actually created.
	out, _ := c.ExecInstance(ctx, name, []string{"getent", "passwd", u})
	if len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("user %q was not created — check container logs", u)
	}

	// Run mise install best-effort (don't fail launch on mise errors).
	c.ExecInstance(ctx, name, []string{"su", "-", u, "-c", "mise install 2>/dev/null || true"})

	return nil
}

// renderZshrc returns the complete .zshrc content for the provisioned user.
func renderZshrc(opts LaunchOpts) string {
	var b strings.Builder
	b.WriteString("export PATH=\"/usr/local/bin:$HOME/.local/bin:$PATH\"\n")
	b.WriteString("eval \"$(mise activate zsh)\"\n")
	if opts.Sudo {
		switch opts.Distro {
		case "alpine":
			b.WriteString("alias apk='doas apk'\n")
		case "ubuntu":
			b.WriteString("alias apt='sudo apt'\n")
			b.WriteString("alias apt-get='sudo apt-get'\n")
		}
	}
	b.WriteString("[[ -f ~/.oh-my-zsh/oh-my-zsh.sh ]] && {\n")
	b.WriteString("  export ZSH=\"$HOME/.oh-my-zsh\"\n")
	b.WriteString("  ZSH_THEME=\"dpoggi\"\n")
	b.WriteString("  plugins=(git zsh-autosuggestions)\n")
	b.WriteString("  source $ZSH/oh-my-zsh.sh\n")
	b.WriteString("  PROMPT=\"%{$fg[cyan]%}[incus]%{$reset_color%} ${PROMPT}\"\n")
	b.WriteString("}\n")
	b.WriteString("alias f=\"fzf --preview 'bat {-1} --color=always'\"\n")
	return b.String()
}
