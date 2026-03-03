package main

import (
	"testing"
)

func TestSubcommandRoute_NoArgs_RunsTUI(t *testing.T) {
	cmd, _ := parseArgs([]string{"ring"})
	if cmd != cmdTUI {
		t.Errorf("no args: expected cmdTUI, got %v", cmd)
	}
}

func TestSubcommandRoute_LaunchSubcommand(t *testing.T) {
	cmd, _ := parseArgs([]string{"ring", "launch", "mydev"})
	if cmd != cmdLaunch {
		t.Errorf("launch subcommand: expected cmdLaunch, got %v", cmd)
	}
}

func TestSubcommandRoute_UnknownSubcommand(t *testing.T) {
	cmd, _ := parseArgs([]string{"ring", "unknown"})
	if cmd != cmdUnknown {
		t.Errorf("unknown subcommand: expected cmdUnknown, got %v", cmd)
	}
}

func TestSubcommandRoute_EnterSubcommand(t *testing.T) {
	cmd, args := parseArgs([]string{"ring", "enter", "mydev"})
	if cmd != cmdEnter {
		t.Errorf("enter subcommand: expected cmdEnter, got %v", cmd)
	}
	if len(args) != 1 || args[0] != "mydev" {
		t.Errorf("enter subcommand: expected args [mydev], got %v", args)
	}
}

func TestSubcommandRoute_EnterAlias(t *testing.T) {
	cmd, args := parseArgs([]string{"ring", "e", "mydev"})
	if cmd != cmdEnter {
		t.Errorf("e alias: expected cmdEnter, got %v", cmd)
	}
	if len(args) != 1 || args[0] != "mydev" {
		t.Errorf("e alias: expected args [mydev], got %v", args)
	}
}

func TestEnterShellArgs_UserExists(t *testing.T) {
	got := enterShellArgs("/usr/bin/incus", "mydev", "chad", 1000, true)
	// Should use incus exec --user/--group/--cwd instead of su -
	if got[0] != "/usr/bin/incus" || got[1] != "exec" || got[2] != "mydev" {
		t.Errorf("unexpected prefix: %v", got[:3])
	}
	// Must contain --user 1000
	found := false
	for i, arg := range got {
		if arg == "--user" && i+1 < len(got) && got[i+1] == "1000" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --user 1000 in args, got %v", got)
	}
	// Must end with /bin/zsh -l
	if got[len(got)-2] != "/bin/zsh" || got[len(got)-1] != "-l" {
		t.Errorf("expected login shell at end, got %v", got[len(got)-2:])
	}
}

func TestEnterShellArgs_UserMissing_FallsBackToRoot(t *testing.T) {
	got := enterShellArgs("/usr/bin/incus", "mydev", "chad", 1000, false)
	want := []string{"/usr/bin/incus", "exec", "mydev", "--", "/bin/zsh"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseLaunchFlags_Name(t *testing.T) {
	opts, err := parseLaunchFlags([]string{"mydev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Name != "mydev" {
		t.Errorf("name: got %q, want mydev", opts.Name)
	}
}

func TestParseLaunchFlags_Defaults(t *testing.T) {
	opts, err := parseLaunchFlags([]string{"mydev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Distro != "alpine" {
		t.Errorf("default distro: got %q, want alpine", opts.Distro)
	}
	if !opts.Sudo {
		t.Error("default sudo: should be true")
	}
	if opts.MountPath != "/workspace" {
		t.Errorf("default mount path: got %q, want /workspace", opts.MountPath)
	}
}

func TestParseLaunchFlags_Ubuntu(t *testing.T) {
	opts, err := parseLaunchFlags([]string{"mydev", "--distro", "ubuntu"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Distro != "ubuntu" {
		t.Errorf("distro: got %q, want ubuntu", opts.Distro)
	}
}

func TestParseLaunchFlags_EnableSudo(t *testing.T) {
	opts, err := parseLaunchFlags([]string{"mydev", "--enable-sudo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.Sudo {
		t.Error("--enable-sudo: Sudo should be true")
	}
}

func TestParseLaunchFlags_DryRun(t *testing.T) {
	opts, err := parseLaunchFlags([]string{"mydev", "--dry-run"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.DryRun {
		t.Error("--dry-run: DryRun should be true")
	}
}

func TestParseLaunchFlags_MissingName(t *testing.T) {
	_, err := parseLaunchFlags([]string{})
	if err == nil {
		t.Error("expected error when name is missing")
	}
}

func TestParseLaunchFlags_Proxy(t *testing.T) {
	opts, err := parseLaunchFlags([]string{"mydev", "--proxy", "proxy.corp.com:3128"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Proxy != "proxy.corp.com:3128" {
		t.Errorf("proxy: got %q, want proxy.corp.com:3128", opts.Proxy)
	}
}

func TestParseLaunchFlags_GHToken(t *testing.T) {
	// --gh-token is a bool flag; it shouldn't consume the next arg.
	// We can't fully test the interactive prompt here, but we can verify
	// the flag is recognized without error when no prompt is attached.
	// Since promptGHCredentials reads stdin, we just test isBoolFlag.
	if !isBoolFlag("--gh-token") {
		t.Error("--gh-token must be recognized as a bool flag")
	}
}

func TestIsBoolFlag_GHToken(t *testing.T) {
	if !isBoolFlag("--gh-token") {
		t.Error("isBoolFlag must return true for --gh-token")
	}
	if !isBoolFlag("-gh-token") {
		t.Error("isBoolFlag must return true for -gh-token")
	}
}

func TestParseLaunchFlags_Mount_Single(t *testing.T) {
	dir := t.TempDir()
	opts, err := parseLaunchFlags([]string{"mydev", "--mount", dir + ":/notes"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts.ExtraMounts) != 1 {
		t.Fatalf("expected 1 extra mount, got %d", len(opts.ExtraMounts))
	}
	if opts.ExtraMounts[0].HostPath != dir {
		t.Errorf("host path: got %q, want %q", opts.ExtraMounts[0].HostPath, dir)
	}
	if opts.ExtraMounts[0].ContainerPath != "/notes" {
		t.Errorf("container path: got %q, want /notes", opts.ExtraMounts[0].ContainerPath)
	}
}

func TestParseLaunchFlags_Mount_Multiple(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	opts, err := parseLaunchFlags([]string{"mydev", "--mount", dir1 + ":/notes", "--mount", dir2 + ":/docs"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts.ExtraMounts) != 2 {
		t.Fatalf("expected 2 extra mounts, got %d", len(opts.ExtraMounts))
	}
	if opts.ExtraMounts[0].ContainerPath != "/notes" {
		t.Errorf("mount 0 container path: got %q, want /notes", opts.ExtraMounts[0].ContainerPath)
	}
	if opts.ExtraMounts[1].ContainerPath != "/docs" {
		t.Errorf("mount 1 container path: got %q, want /docs", opts.ExtraMounts[1].ContainerPath)
	}
}

func TestParseLaunchFlags_Mount_InvalidFormat(t *testing.T) {
	// No colon
	_, err := parseLaunchFlags([]string{"mydev", "--mount", "/tmp/notes"})
	if err == nil {
		t.Error("expected error for mount without colon")
	}

	// Relative host path
	_, err = parseLaunchFlags([]string{"mydev", "--mount", "relative:/notes"})
	if err == nil {
		t.Error("expected error for relative host path in mount")
	}

	// Relative container path
	_, err = parseLaunchFlags([]string{"mydev", "--mount", "/tmp/notes:notes"})
	if err == nil {
		t.Error("expected error for relative container path in mount")
	}
}

func TestParseLaunchFlags_CustomMountPath(t *testing.T) {
	opts, err := parseLaunchFlags([]string{"mydev", "--mount-path", "/code"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.MountPath != "/code" {
		t.Errorf("mount-path: got %q, want /code", opts.MountPath)
	}
}
