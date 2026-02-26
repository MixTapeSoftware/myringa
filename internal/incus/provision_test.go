package incus

import (
	"testing"

	"github.com/lxc/incus/v6/shared/api"
)

func TestParseProfileYAML_BaseProfile(t *testing.T) {
	data := `
description: Base myringa dev environment
config:
  limits.cpu: "4"
  limits.memory: 4GiB
devices:
  root:
    type: disk
    pool: default
    path: /
    size: 20GiB
`
	var out api.ProfilePut
	if err := parseProfileYAML(data, &out); err != nil {
		t.Fatalf("parseProfileYAML failed: %v", err)
	}

	if out.Description != "Base myringa dev environment" {
		t.Errorf("description: got %q, want %q", out.Description, "Base myringa dev environment")
	}

	if out.Config["limits.cpu"] != "4" {
		t.Errorf("limits.cpu: got %q, want 4", out.Config["limits.cpu"])
	}
	if out.Config["limits.memory"] != "4GiB" {
		t.Errorf("limits.memory: got %q, want 4GiB", out.Config["limits.memory"])
	}

	root, ok := out.Devices["root"]
	if !ok {
		t.Fatal("devices.root missing")
	}
	if root["type"] != "disk" {
		t.Errorf("root device type: got %q, want disk", root["type"])
	}
	if root["size"] != "20GiB" {
		t.Errorf("root device size: got %q, want 20GiB", root["size"])
	}
}

func TestParseProfileYAML_DockerProfile(t *testing.T) {
	data := `
description: Docker-in-Incus support (requires AppArmor unconfined)
config:
  security.nesting: "true"
  security.syscalls.intercept.mknod: "true"
  security.syscalls.intercept.setxattr: "true"
  raw.lxc: lxc.apparmor.profile=unconfined
devices: {}
`
	var out api.ProfilePut
	if err := parseProfileYAML(data, &out); err != nil {
		t.Fatalf("parseProfileYAML failed: %v", err)
	}

	if out.Config["security.nesting"] != "true" {
		t.Errorf("security.nesting: got %q, want true", out.Config["security.nesting"])
	}
	if out.Config["raw.lxc"] != "lxc.apparmor.profile=unconfined" {
		t.Errorf("raw.lxc: got %q", out.Config["raw.lxc"])
	}
}

func TestParseProfileYAML_EmptyConfig(t *testing.T) {
	data := `description: Empty profile`
	var out api.ProfilePut
	if err := parseProfileYAML(data, &out); err != nil {
		t.Fatalf("parseProfileYAML failed: %v", err)
	}
	if out.Description != "Empty profile" {
		t.Errorf("description: got %q", out.Description)
	}
	// Config and Devices may be nil — that's fine.
}

func TestParseProfileYAML_InvalidYAML(t *testing.T) {
	data := `this: is: not: valid: yaml: :`
	var out api.ProfilePut
	if err := parseProfileYAML(data, &out); err == nil {
		t.Error("expected error for invalid YAML")
	}
}

// ── parseCPULimit ─────────────────────────────────────────────────────────────

func TestParseCPULimit(t *testing.T) {
	cases := []struct {
		input string
		cores float64
		ok    bool
	}{
		{"4", 4, true},
		{"1", 1, true},
		{"0-3", 4, true},  // 4 cores: 0,1,2,3
		{"2-5", 4, true},  // 4 cores: 2,3,4,5
		{"0-0", 1, true},  // single core as range
		{"", 0, false},
		{"0", 0, false},   // zero cores is invalid
		{"-1", 0, false},  // negative
		{"abc", 0, false},
		{"3-1", 0, false}, // hi < lo
	}

	for _, c := range cases {
		got, ok := parseCPULimit(c.input)
		if ok != c.ok {
			t.Errorf("parseCPULimit(%q): ok=%v, want %v", c.input, ok, c.ok)
			continue
		}
		if ok && got != c.cores {
			t.Errorf("parseCPULimit(%q): cores=%.0f, want %.0f", c.input, got, c.cores)
		}
	}
}
