package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lxc/incus/v6/shared/api"
	"golang.org/x/term"

	"ring/internal/images"
	"ring/internal/incus"
	"ring/internal/provision"
	"ring/internal/ui"
)

type command int

const (
	cmdTUI         command = iota
	cmdLaunch      command = iota
	cmdImagesBuild command = iota
	cmdEnter       command = iota
	cmdUnknown     command = iota
)

func main() {
	cmd, args := parseArgs(os.Args)
	switch cmd {
	case cmdTUI:
		runTUI()
	case cmdLaunch:
		runLaunch(args)
	case cmdImagesBuild:
		runImagesBuild(args)
	case cmdEnter:
		runEnter(args)
	case cmdUnknown:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\nUsage:\n  ring                              TUI dashboard\n  ring launch (or \"l\") <name>       create a dev container\n  ring enter (or \"e\") <name>        shell into a container (starts if stopped)\n  ring images build <distro>        build a custom image\n", args[0])
		os.Exit(1)
	}
}

func runTUI() {
	u, err := user.Current()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot determine current user:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(ui.NewModel(u.Username), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runLaunch(args []string) {
	opts, err := parseLaunchFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ring launch:", err)
		fmt.Fprint(os.Stderr, launchUsage)
		os.Exit(1)
	}

	if opts.DryRun {
		plan, err := provision.DryRun(context.Background(), opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ring launch:", err)
			os.Exit(1)
		}
		fmt.Print(plan)
		return
	}

	c, err := incus.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to Incus:", err)
		os.Exit(1)
	}

	spin := NewSpinner(os.Stderr, isTTY(os.Stderr))
	spin.Start("Launching " + opts.Name + "...")

	client := &incusProvisionAdapter{c: c}
	if err := launchWithAutoBuild(context.Background(), c, client, opts, spin); err != nil {
		spin.Stop()
		fmt.Fprintln(os.Stderr, "ring launch:", err)
		os.Exit(1)
	}

	spin.Stop()
	fmt.Printf("Container %q is ready.\n", opts.Name)
	if opts.GHToken != "" {
		fmt.Printf("  GitHub: GH_TOKEN set — git identity %s <%s>\n", opts.GHUserName, opts.GHUserEmail)
	}
}

// parseArgs returns the subcommand and remaining arguments.
func parseArgs(args []string) (command, []string) {
	if len(args) < 2 {
		return cmdTUI, nil
	}
	switch args[1] {
	case "launch":
		return cmdLaunch, args[2:]
	case "l":
		return cmdLaunch, args[2:]
	case "enter", "e":
		return cmdEnter, args[2:]
	case "images":
		if len(args) >= 3 && args[2] == "build" {
			return cmdImagesBuild, args[3:]
		}
		return cmdUnknown, args[1:]
	default:
		return cmdUnknown, args[1:]
	}
}

// parseLaunchFlags parses args for the launch subcommand into LaunchOpts.
// The container name is the first non-flag argument and may appear anywhere in args.
// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

func parseLaunchFlags(args []string) (provision.LaunchOpts, error) {
	// Separate the container name (first non-flag arg) from flag args so that
	// flag.Parse sees only flags, regardless of where the name appears.
	name, flagArgs := extractName(args)

	fs := flag.NewFlagSet("launch", flag.ContinueOnError)

	distro := fs.String("distro", "alpine", "OS distro: alpine, ubuntu")
	enableSudo := fs.Bool("enable-sudo", true, "Grant passwordless sudo/doas inside the container")
	proxy := fs.String("proxy", "", "HTTP proxy host:port")
	workspace := fs.String("workspace", "", "Host directory to mount (default: cwd)")
	mountPath := fs.String("mount-path", "/workspace", "Container mount point")
	dryRun := fs.Bool("dry-run", false, "Show what would be done without making changes")
	ghToken := fs.Bool("gh-token", false, "Configure GitHub CLI + git auth (prompts for PAT and git identity)")
	var mounts stringSlice
	fs.Var(&mounts, "mount", "/host/path:/container/path (repeatable)")

	if err := fs.Parse(flagArgs); err != nil {
		return provision.LaunchOpts{}, err
	}

	if name == "" {
		return provision.LaunchOpts{}, fmt.Errorf("container name is required")
	}

	// Default workspace to cwd.
	ws := *workspace
	if ws == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return provision.LaunchOpts{}, fmt.Errorf("getting cwd: %w", err)
		}
		ws = cwd
	}

	// Parse --mount values into MountSpecs.
	var extraMounts []provision.MountSpec
	for _, raw := range mounts {
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) != 2 {
			return provision.LaunchOpts{}, fmt.Errorf("--mount %q: expected /host/path:/container/path", raw)
		}
		hostPath, containerPath := parts[0], parts[1]
		if !strings.HasPrefix(hostPath, "/") || !strings.HasPrefix(containerPath, "/") {
			return provision.LaunchOpts{}, fmt.Errorf("--mount %q: both paths must be absolute", raw)
		}
		if info, err := os.Stat(hostPath); err != nil {
			return provision.LaunchOpts{}, fmt.Errorf("--mount %q: host path does not exist: %w", raw, err)
		} else if !info.IsDir() {
			return provision.LaunchOpts{}, fmt.Errorf("--mount %q: host path is not a directory", raw)
		}
		extraMounts = append(extraMounts, provision.MountSpec{
			HostPath:      hostPath,
			ContainerPath: containerPath,
		})
	}

	// Default username/UID/GID from current user.
	u, err := user.Current()
	if err != nil {
		return provision.LaunchOpts{}, fmt.Errorf("getting current user: %w", err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	username := u.Username

	opts := provision.LaunchOpts{
		Name:        name,
		Distro:      *distro,
		Sudo:        *enableSudo,
		Proxy:       *proxy,
		Workspace:   ws,
		MountPath:   *mountPath,
		ExtraMounts: extraMounts,
		Username:    username,
		UID:         uid,
		GID:         gid,
		DryRun:      *dryRun,
	}

	if *ghToken {
		creds, err := promptGHCredentials()
		if err != nil {
			return provision.LaunchOpts{}, err
		}
		opts.GHToken = creds.token
		opts.GHUserName = creds.name
		opts.GHUserEmail = creds.email
	}

	return opts, nil
}

const launchUsage = `
Usage: ring launch [flags] <name>

  --distro string       OS distro: alpine, ubuntu (default "alpine")
  --enable-sudo         Grant passwordless sudo/doas (default: true)
  --proxy string        HTTP proxy host:port
  --workspace string    Host directory to mount (default: cwd)
  --mount-path string   Container mount point (default: /workspace)
  --mount string        Extra bind mount /host/path:/container/path (repeatable)
  --gh-token            Configure GitHub CLI + git auth (prompts for PAT and git identity)
  --dry-run             Show what would be done

Examples:
  ring launch mydev
  ring launch mydev --distro ubuntu
  ring launch mydev --mount /home/chad/Dropbox/notes:/notes
  ring launch mydev --dry-run
`

// launchWithAutoBuild calls provision.Launch and, if the required image is missing,
// automatically builds it then retries — no separate command needed.
func launchWithAutoBuild(ctx context.Context, c incus.Client, client *incusProvisionAdapter, opts provision.LaunchOpts, spin *Spinner) error {
	err := provision.Launch(ctx, client, opts, io.Discard)
	if err == nil {
		return nil
	}
	var imgErr *provision.ErrImageNotFound
	if !errors.As(err, &imgErr) {
		return err
	}

	// Image missing — pause spinner, stream build output, then resume.
	spin.Pause()
	fmt.Printf("Image %q not found. Building it now (this takes a few minutes)...\n\n", imgErr.Alias)
	buildOpts := images.BuildOpts{Distro: imgErr.Distro}
	if err := images.Build(ctx, c, buildOpts, os.Stdout); err != nil {
		return fmt.Errorf("building image: %w", err)
	}
	fmt.Println()
	spin.Resume("Launching " + opts.Name + "...")
	return provision.Launch(ctx, client, opts, io.Discard)
}

func runImagesBuild(args []string) {
	fs := flag.NewFlagSet("images build", flag.ContinueOnError)
	tag := fs.String("tag", "latest", "Image tag")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "ring images build:", err)
		os.Exit(1)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "ring images build: distro is required (alpine or ubuntu)")
		fmt.Fprintln(os.Stderr, "\nUsage: ring images build <distro> [--tag <tag>]")
		os.Exit(1)
	}

	opts := images.BuildOpts{
		Distro: fs.Arg(0),
		Tag:    *tag,
	}

	c, err := incus.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to Incus:", err)
		os.Exit(1)
	}

	if err := images.Build(context.Background(), c, opts, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ring images build:", err)
		os.Exit(1)
	}
}

func runEnter(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "ring enter: container name is required")
		fmt.Fprintln(os.Stderr, "\nUsage: ring enter <name>")
		os.Exit(1)
	}
	name := args[0]

	u, err := user.Current()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot determine current user:", err)
		os.Exit(1)
	}

	c, err := incus.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to Incus:", err)
		os.Exit(1)
	}

	state, err := c.GetInstanceState(context.Background(), name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ring enter:", err)
		os.Exit(1)
	}

	if state != "Running" {
		fmt.Fprintf(os.Stderr, "starting %s...\n", name)
		if err := c.StartInstance(context.Background(), name); err != nil {
			fmt.Fprintln(os.Stderr, "ring enter: start:", err)
			os.Exit(1)
		}
	}

	incusBin, err := exec.LookPath("incus")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ring enter: incus not found in PATH")
		os.Exit(1)
	}

	// Check whether the provisioned user exists. ExecInstance doesn't surface
	// exit codes, so we check for non-empty stdout from getent.
	// A short poll handles the rare case where ring enter is called immediately
	// after ring launch and the instance hasn't fully initialized yet.
	userExists := waitForUser(context.Background(), c, name, u.Username, 5*time.Second)

	uid, _ := strconv.Atoi(u.Uid)
	argv := enterShellArgs(incusBin, name, u.Username, uid, userExists)
	if err := syscall.Exec(incusBin, argv, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "ring enter:", err)
		os.Exit(1)
	}
}

// waitForUser polls getent inside the container until the named user appears
// or the timeout is reached. Returns true if the user exists.
func waitForUser(ctx context.Context, c incus.Client, container, username string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		out, _ := c.ExecInstance(ctx, container, []string{"getent", "passwd", username})
		if len(strings.TrimSpace(string(out))) > 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		fmt.Fprintf(os.Stderr, "waiting for user %q...\n", username)
		time.Sleep(time.Second)
	}
}

// enterShellArgs returns the argv for exec-ing into a container.
// When userExists is true it opens a login shell as that user using
// incus exec --user/--group/--cwd, which preserves Incus environment.*
// variables (su - would wipe them). Otherwise falls back to a root shell.
func enterShellArgs(incusBin, container, username string, uid int, userExists bool) []string {
	if userExists {
		return []string{
			incusBin, "exec", container,
			"--user", strconv.Itoa(uid),
			"--group", strconv.Itoa(uid),
			"--cwd", "/home/" + username,
			"--env", "HOME=/home/" + username,
			"--env", "USER=" + username,
			"--env", "SHELL=/bin/zsh",
			"--", "/bin/zsh", "-l",
		}
	}
	return []string{incusBin, "exec", container, "--", "/bin/zsh"}
}

// extractName splits args into the first non-flag arg (name) and remaining flag args.
// This allows the container name to appear before, after, or between flags.
func extractName(args []string) (name string, flagArgs []string) {
	skip := false
	for i, arg := range args {
		if skip {
			flagArgs = append(flagArgs, arg)
			skip = false
			continue
		}
		if arg == "--" {
			flagArgs = append(flagArgs, args[i:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			// If the flag takes a value (no = sign), mark next arg as its value.
			if !strings.Contains(arg, "=") && isBoolFlag(arg) {
				// bool flags don't consume the next arg
			} else if !strings.Contains(arg, "=") {
				skip = true
			}
			continue
		}
		if name == "" {
			name = arg
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}
	return name, flagArgs
}

// isBoolFlag reports whether arg names a boolean flag (no value argument follows).
func isBoolFlag(arg string) bool {
	// Strip leading dashes
	name := strings.TrimLeft(arg, "-")
	// Known boolean flags for the launch subcommand
	boolFlags := map[string]bool{
		"enable-sudo": true,
		"dry-run":     true,
		"gh-token":    true,
	}
	return boolFlags[name]
}

// ── GitHub credential prompts ────────────────────────────────────────────────

type ghCreds struct{ token, name, email string }

func promptGHCredentials() (ghCreds, error) {
	fmt.Println("GitHub fine-grained PAT:")
	fmt.Println("  https://github.com/settings/tokens?type=beta")
	fmt.Print("PAT: ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return ghCreds{}, fmt.Errorf("reading token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return ghCreds{}, fmt.Errorf("token must not be empty")
	}

	defaultName := gitConfigValue("user.name")
	defaultEmail := gitConfigValue("user.email")
	name := promptLine(fmt.Sprintf("git user.name [%s]: ", defaultName), defaultName)
	email := promptLine(fmt.Sprintf("git user.email [%s]: ", defaultEmail), defaultEmail)

	if name == "" {
		return ghCreds{}, fmt.Errorf("git user.name must not be empty")
	}
	if email == "" {
		return ghCreds{}, fmt.Errorf("git user.email must not be empty")
	}
	return ghCreds{token: token, name: name, email: email}, nil
}

// gitConfigValue reads a key from the host's global git config. Returns "" on any error.
func gitConfigValue(key string) string {
	out, err := exec.Command("git", "config", "--global", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// promptLine reads a trimmed line from stdin, returning defaultVal on empty input.
func promptLine(prompt, defaultVal string) string {
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if v := strings.TrimSpace(line); v != "" {
		return v
	}
	return defaultVal
}

// incusProvisionAdapter bridges incus.Client to provision.Client.
// incus.Client uses api.InstancesPost; provision.Client uses provision.InstanceRequest.
type incusProvisionAdapter struct {
	c incus.Client
}

func (a *incusProvisionAdapter) ProfileExists(ctx context.Context, name string) (bool, error) {
	return a.c.ProfileExists(ctx, name)
}

func (a *incusProvisionAdapter) ImageAliasExists(ctx context.Context, alias string) (bool, error) {
	return a.c.ImageAliasExists(ctx, alias)
}

func (a *incusProvisionAdapter) CreateProfile(ctx context.Context, name string, yamlData string) error {
	return a.c.CreateProfile(ctx, name, yamlData)
}

func (a *incusProvisionAdapter) CreateInstanceFull(ctx context.Context, req provision.InstanceRequest) error {
	return a.c.CreateInstanceFull(ctx, api.InstancesPost{
		Name: req.Name,
		Source: api.InstanceSource{
			Type:  "image",
			Alias: req.ImageAlias,
		},
		InstancePut: api.InstancePut{
			Profiles: req.Profiles,
			Config:   req.Config,
		},
	})
}

func (a *incusProvisionAdapter) UpdateInstanceConfig(ctx context.Context, name string, config map[string]string) error {
	return a.c.UpdateInstanceConfig(ctx, name, config)
}

func (a *incusProvisionAdapter) AddDevice(ctx context.Context, instanceName, deviceName string, device map[string]string) error {
	return a.c.AddDevice(ctx, instanceName, deviceName, device)
}

func (a *incusProvisionAdapter) StartInstance(ctx context.Context, name string) error {
	return a.c.StartInstance(ctx, name)
}

func (a *incusProvisionAdapter) ExecInstance(ctx context.Context, name string, cmd []string) ([]byte, error) {
	return a.c.ExecInstance(ctx, name, cmd)
}

func (a *incusProvisionAdapter) WriteFile(ctx context.Context, instance, path string, content []byte, uid, gid int, mode os.FileMode) error {
	return a.c.WriteFile(ctx, instance, path, content, uid, gid, mode)
}
