package provision_test

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"

	"myringa/internal/provision"
)

// cloudInitData is a minimal struct to parse cloud-init YAML for assertions.
type cloudInitData struct {
	Users []struct {
		Name         string      `yaml:"name"`
		UID          string      `yaml:"uid"`
		Groups       interface{} `yaml:"groups"` // cloud-init allows list or string
		Shell        string      `yaml:"shell"`
		Sudo         string      `yaml:"sudo"`
		NoCreateHome bool        `yaml:"no_create_home"`
	} `yaml:"users"`
	WriteFiles []struct {
		Path    string `yaml:"path"`
		Owner   string `yaml:"owner"`
		Content string `yaml:"content"`
	} `yaml:"write_files"`
	Runcmd []interface{} `yaml:"runcmd"`
}

// groupsStr converts the groups field (list or string) to a flat string for assertions.
func groupsStr(groups interface{}) string {
	switch v := groups.(type) {
	case string:
		return v
	case []interface{}:
		parts := make([]string, len(v))
		for i, g := range v {
			parts[i] = fmt.Sprint(g)
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(v)
	}
}

func parseCloudInit(t *testing.T, out string) cloudInitData {
	t.Helper()
	// Strip the #cloud-config header line before parsing YAML.
	body, _ := strings.CutPrefix(out, "#cloud-config\n")
	var data cloudInitData
	if err := yaml.Unmarshal([]byte(body), &data); err != nil {
		t.Fatalf("cloud-init output is not valid YAML: %v\noutput:\n%s", err, out)
	}
	return data
}

func TestRenderCloudInit_BareMinimum(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "testuser",
		UID:       1000,
		GID:       1000,
		Sudo:      true,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasPrefix(out, "#cloud-config\n") {
		t.Error("output must start with #cloud-config header")
	}

	data := parseCloudInit(t, out)

	if len(data.Users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(data.Users))
	}
	u := data.Users[0]
	if u.Name != "testuser" {
		t.Errorf("user name: got %q, want %q", u.Name, "testuser")
	}
	if u.UID != "1000" {
		t.Errorf("user UID: got %q, want %q", u.UID, "1000")
	}
	if u.Shell != "/bin/zsh" {
		t.Errorf("user shell: got %q, want /bin/zsh", u.Shell)
	}
	if u.NoCreateHome {
		t.Error("no_create_home must be false")
	}

	// write_files: .zprofile and .zshrc for the user
	paths := make(map[string]bool)
	for _, f := range data.WriteFiles {
		paths[f.Path] = true
	}
	if !paths["/home/testuser/.zprofile"] {
		t.Error("missing .zprofile in write_files")
	}
	if !paths["/home/testuser/.zshrc"] {
		t.Error("missing .zshrc in write_files")
	}

	// runcmd: mise install, no docker commands
	if strings.Contains(out, "systemctl") {
		t.Error("bare minimum must not contain systemctl (no docker)")
	}
	if strings.Contains(out, "curl") {
		t.Error("bare minimum must not contain curl (no install scripts)")
	}
}

func TestRenderCloudInit_SudoEnabled(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      true,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := parseCloudInit(t, out)
	if len(data.Users) == 0 {
		t.Fatal("no users in output")
	}
	if data.Users[0].Sudo != "ALL=(ALL) NOPASSWD:ALL" {
		t.Errorf("sudo: got %q, want ALL=(ALL) NOPASSWD:ALL", data.Users[0].Sudo)
	}
}

func TestRenderCloudInit_SudoDisabled(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      false,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must not contain NOPASSWD anywhere
	if strings.Contains(out, "NOPASSWD") {
		t.Error("sudo disabled: output must not contain NOPASSWD")
	}
	if strings.Contains(out, "sudo:") {
		t.Error("sudo disabled: output must not contain sudo: key")
	}

	// Still valid YAML
	parseCloudInit(t, out)
}

func TestRenderCloudInit_DockerEnabled(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      true,
		Docker:    true,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := parseCloudInit(t, out)

	// User must be in docker group
	if !strings.Contains(groupsStr(data.Users[0].Groups), "docker") {
		t.Errorf("docker enabled: user groups must include docker, got: %s", groupsStr(data.Users[0].Groups))
	}

	// runcmd must enable docker service
	if !strings.Contains(out, "systemctl") {
		t.Error("docker enabled: runcmd must contain systemctl to enable docker")
	}
	if !strings.Contains(out, "enable") {
		t.Error("docker enabled: runcmd must enable docker service")
	}

	// Must NOT use curl|sh or download Docker packages
	if strings.Contains(out, "curl") {
		t.Error("docker must NOT install via curl (packages are baked into image)")
	}
	if strings.Contains(out, "apt-get") || strings.Contains(out, "apk add") {
		t.Error("docker runcmd must NOT install packages (already in image)")
	}
}

func TestRenderCloudInit_DockerDisabled_NoDockerGroup(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      true,
		Docker:    false,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := parseCloudInit(t, out)
	if strings.Contains(groupsStr(data.Users[0].Groups), "docker") {
		t.Error("docker disabled: user groups must not include docker")
	}
}

func TestRenderCloudInit_DevToolsEnabled(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      true,
		DevTools:  true,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// oh-my-zsh config must be in .zshrc
	if !strings.Contains(out, "oh-my-zsh") {
		t.Error("devtools enabled: .zshrc must reference oh-my-zsh")
	}
	if !strings.Contains(out, "ZSH_THEME") {
		t.Error("devtools enabled: .zshrc must set ZSH_THEME")
	}
	if !strings.Contains(out, "fzf") {
		t.Error("devtools enabled: .zshrc must reference fzf")
	}
	if !strings.Contains(out, "bat") {
		t.Error("devtools enabled: .zshrc must reference bat")
	}

	// Must NOT curl|sh install anything
	if strings.Contains(out, "curl") {
		t.Error("devtools must NOT install via curl (tools are baked into image)")
	}

	parseCloudInit(t, out)
}

func TestRenderCloudInit_DevToolsDisabled(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      true,
		DevTools:  false,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(out, "oh-my-zsh") {
		t.Error("devtools disabled: must not reference oh-my-zsh")
	}
	if strings.Contains(out, "ZSH_THEME") {
		t.Error("devtools disabled: must not set ZSH_THEME")
	}
}

func TestRenderCloudInit_DockerAndDevTools(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      true,
		Docker:    true,
		DevTools:  true,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := parseCloudInit(t, out)

	// Docker group
	if !strings.Contains(groupsStr(data.Users[0].Groups), "docker") {
		t.Error("docker+devtools: user must be in docker group")
	}

	// oh-my-zsh in zshrc
	if !strings.Contains(out, "oh-my-zsh") {
		t.Error("docker+devtools: must reference oh-my-zsh")
	}

	// systemctl enable docker
	if !strings.Contains(out, "systemctl") {
		t.Error("docker+devtools: runcmd must enable docker")
	}

	// No curl|sh
	if strings.Contains(out, "curl") {
		t.Error("docker+devtools: must not use curl installs")
	}
}

func TestRenderCloudInit_ValidYAML_AllCombos(t *testing.T) {
	cases := []provision.CloudInitOpts{
		{Username: "u", UID: 1000, GID: 1000, Sudo: false, Docker: false, DevTools: false, SudoGroup: "sudo"},
		{Username: "u", UID: 1000, GID: 1000, Sudo: true, Docker: false, DevTools: false, SudoGroup: "sudo"},
		{Username: "u", UID: 1000, GID: 1000, Sudo: true, Docker: true, DevTools: true, SudoGroup: "sudo"},
		{Username: "u", UID: 1000, GID: 1000, Sudo: false, Docker: true, DevTools: true, SudoGroup: "wheel"},
		{Username: "u", UID: 1000, GID: 1000, Sudo: true, Docker: false, DevTools: true, SudoGroup: "wheel"},
	}

	for _, opts := range cases {
		out, err := provision.RenderCloudInit(opts)
		if err != nil {
			t.Errorf("opts=%+v: unexpected error: %v", opts, err)
			continue
		}
		body, _ := strings.CutPrefix(out, "#cloud-config\n")
		var data map[string]interface{}
		if err := yaml.Unmarshal([]byte(body), &data); err != nil {
			t.Errorf("opts=%+v: invalid YAML: %v\noutput:\n%s", opts, err, out)
		}
	}
}

func TestRenderCloudInit_AlpineUsesWheelGroup(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      true,
		SudoGroup: "wheel",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := parseCloudInit(t, out)
	groups := groupsStr(data.Users[0].Groups)
	if !strings.Contains(groups, "wheel") {
		t.Errorf("Alpine sudo group must be wheel, got: %s", groups)
	}
	if strings.Contains(groups, "sudo") {
		t.Errorf("Alpine must not use sudo group, got: %s", groups)
	}
}

func TestRenderCloudInit_ZshrcContainsMiseActivate(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      true,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "mise activate zsh") {
		t.Error(".zshrc must activate mise")
	}
	if !strings.Contains(out, "MISE_TRUSTED_CONFIG_PATHS") {
		t.Error(".zshrc must set MISE_TRUSTED_CONFIG_PATHS")
	}
}

func TestRenderCloudInit_ZprofileChangesDir(t *testing.T) {
	opts := provision.CloudInitOpts{
		Username:  "chad",
		UID:       1001,
		GID:       1001,
		Sudo:      true,
		SudoGroup: "sudo",
	}

	out, err := provision.RenderCloudInit(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "/workspace") {
		t.Error(".zprofile must cd to /workspace")
	}
}
