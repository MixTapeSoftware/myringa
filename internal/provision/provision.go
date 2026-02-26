package provision

import (
	"context"
	"embed"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

//go:embed embed/profiles
var profilesFS embed.FS

// ErrImageNotFound is returned by Launch when the required local image alias
// doesn't exist. The caller can use this to trigger an auto-build.
type ErrImageNotFound struct {
	Alias    string
	Distro   string
	DevTools bool
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
	Docker    bool   // requires DevTools=true
	DevTools  bool   // selects the -dev image variant
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
	if o.Docker && !o.DevTools {
		return fmt.Errorf("Docker=true requires DevTools=true: Docker packages are baked into -dev images")
	}
	return nil
}

// ImageAlias resolves the local Incus image alias.
func ImageAlias(distro string, devTools bool) string {
	if devTools {
		return fmt.Sprintf("myringa/%s-dev:latest", distro)
	}
	return fmt.Sprintf("myringa/%s:latest", distro)
}

// BuildProfiles returns the ordered profile list for a new instance.
func BuildProfiles(docker bool) []string {
	profiles := []string{"default", "myringa-base"}
	if docker {
		profiles = append(profiles, "myringa-docker")
	}
	return profiles
}

// WorkspaceDevice returns an Incus device map for a workspace disk mount.
func WorkspaceDevice(hostPath, containerPath string) map[string]string {
	return map[string]string{
		"type":   "disk",
		"source": hostPath,
		"path":   containerPath,
	}
}

// SyncProfiles ensures both myringa static profiles exist in the Incus daemon.
// Profiles are embedded from embed/profiles/. Existing profiles are skipped.
func SyncProfiles(ctx context.Context, c Client) error {
	profiles := []struct {
		name string
		path string
	}{
		{"myringa-base", "embed/profiles/myringa-base.yaml"},
		{"myringa-docker", "embed/profiles/myringa-docker.yaml"},
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
// Steps: validate → sync profiles → create instance → idmap → workspace → start.
func Launch(ctx context.Context, c Client, opts LaunchOpts, out io.Writer) error {
	if err := opts.Validate(); err != nil {
		return fmt.Errorf("invalid opts: %w", err)
	}

	if err := SyncProfiles(ctx, c); err != nil {
		return fmt.Errorf("syncing profiles: %w", err)
	}

	cloudInit, err := RenderCloudInit(CloudInitOpts{
		Username:  opts.Username,
		UID:       opts.UID,
		GID:       opts.GID,
		Sudo:      opts.Sudo,
		Docker:    opts.Docker,
		DevTools:  opts.DevTools,
		SudoGroup: sudoGroup(opts.Distro),
	})
	if err != nil {
		return fmt.Errorf("rendering cloud-init: %w", err)
	}

	imageAlias := ImageAlias(opts.Distro, opts.DevTools)

	exists, err := c.ImageAliasExists(ctx, imageAlias)
	if err != nil {
		return fmt.Errorf("checking image alias: %w", err)
	}
	if !exists {
		return &ErrImageNotFound{Alias: imageAlias, Distro: opts.Distro, DevTools: opts.DevTools}
	}

	config := map[string]string{
		"cloud-init.user-data": cloudInit,
	}
	if opts.Proxy != "" {
		proxyURL := "http://" + opts.Proxy
		config["environment.HTTP_PROXY"] = proxyURL
		config["environment.HTTPS_PROXY"] = proxyURL
	}

	req := InstanceRequest{
		Name:       opts.Name,
		ImageAlias: imageAlias,
		Profiles:   BuildProfiles(opts.Docker),
		Config:     config,
	}
	if err := c.CreateInstanceFull(ctx, req); err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	// Idmap negotiation: try raw.idmap, fall back silently if unsupported.
	idmapErr := c.UpdateInstanceConfig(ctx, opts.Name, map[string]string{
		"raw.idmap": fmt.Sprintf("both %d %d", opts.UID, opts.GID),
	})
	if idmapErr != nil {
		// Non-fatal: workspace files may appear owned by root inside container.
		fmt.Fprintf(out, "WARNING: raw.idmap not supported on this host — workspace files may appear owned by root inside the container (%v)\n", idmapErr)
	}

	device := WorkspaceDevice(opts.Workspace, opts.MountPath)
	if err := c.AddDevice(ctx, opts.Name, "workspace", device); err != nil {
		return fmt.Errorf("adding workspace device: %w", err)
	}

	if err := c.StartInstance(ctx, opts.Name); err != nil {
		return fmt.Errorf("starting instance: %w", err)
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
	fmt.Fprintf(&b, "  Image:     %s\n", ImageAlias(opts.Distro, opts.DevTools))
	fmt.Fprintf(&b, "  Profiles:  %s\n", strings.Join(BuildProfiles(opts.Docker), ", "))
	fmt.Fprintf(&b, "  User:      %s (UID=%d, GID=%d)\n", opts.Username, opts.UID, opts.GID)
	fmt.Fprintf(&b, "  Sudo:      %s\n", strconv.FormatBool(opts.Sudo))
	fmt.Fprintf(&b, "  Docker:    %s\n", strconv.FormatBool(opts.Docker))
	fmt.Fprintf(&b, "  DevTools:  %s\n", strconv.FormatBool(opts.DevTools))
	fmt.Fprintf(&b, "  Workspace: %s → %s\n", opts.Workspace, opts.MountPath)
	if opts.Proxy != "" {
		fmt.Fprintf(&b, "  Proxy:     %s\n", opts.Proxy)
	}
	return b.String(), nil
}
