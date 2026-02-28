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
	got := enterShellArgs("/usr/bin/incus", "mydev", "chad", true)
	want := []string{"/usr/bin/incus", "exec", "mydev", "--", "su", "-", "chad"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEnterShellArgs_UserMissing_FallsBackToRoot(t *testing.T) {
	got := enterShellArgs("/usr/bin/incus", "mydev", "chad", false)
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
	if opts.Sudo {
		t.Error("default sudo: should be false (opt-in via --enable-sudo)")
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

func TestParseLaunchFlags_DockerImpliesDevTools(t *testing.T) {
	opts, err := parseLaunchFlags([]string{"mydev", "--docker"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.Docker {
		t.Error("--docker: Docker should be true")
	}
	if !opts.DevTools {
		t.Error("--docker: DevTools must be implied (Docker is baked into -dev images)")
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

func TestParseLaunchFlags_CustomMountPath(t *testing.T) {
	opts, err := parseLaunchFlags([]string{"mydev", "--mount-path", "/code"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.MountPath != "/code" {
		t.Errorf("mount-path: got %q, want /code", opts.MountPath)
	}
}
