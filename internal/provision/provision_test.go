package provision_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"ring/internal/provision"
)

// ── Mock client ────────────────────────────────────────────────────────────────

type mockClient struct {
	profiles         map[string]bool // tracks created/existing profiles
	instances        []string        // created instance names
	lastImageAlias   string          // image alias from most recent CreateInstanceFull
	instanceConfigs  map[string]map[string]string
	instanceDevices  map[string]map[string]map[string]string
	startedInstances []string
	startCallCount   int
	writtenFiles     map[string][]byte

	// Configurable errors
	createProfileErr      error
	createInstanceFullErr error
	updateConfigErr       error
	addDeviceErr          error
	shiftUnsupported      bool  // if true, AddDevice fails when device has shift=true
	startErr              error // returned on every StartInstance call
	startIdmapErrOnFirst  bool  // if true, first StartInstance returns an idmapping error
	writeFileErr          error
	execResults           map[string]execResult
}

type execResult struct {
	out []byte
	err error
}

func newMockClient() *mockClient {
	return &mockClient{
		profiles:        make(map[string]bool),
		instanceConfigs: make(map[string]map[string]string),
		instanceDevices: make(map[string]map[string]map[string]string),
		execResults:     make(map[string]execResult),
		writtenFiles:    make(map[string][]byte),
	}
}

func (m *mockClient) ProfileExists(_ context.Context, name string) (bool, error) {
	return m.profiles[name], nil
}

func (m *mockClient) ImageAliasExists(_ context.Context, _ string) (bool, error) {
	return true, nil // always available in tests
}

func (m *mockClient) CreateProfile(_ context.Context, name string, _ string) error {
	if m.createProfileErr != nil {
		return m.createProfileErr
	}
	m.profiles[name] = true
	return nil
}

func (m *mockClient) CreateInstanceFull(_ context.Context, req provision.InstanceRequest) error {
	if m.createInstanceFullErr != nil {
		return m.createInstanceFullErr
	}
	m.instances = append(m.instances, req.Name)
	m.lastImageAlias = req.ImageAlias
	m.instanceConfigs[req.Name] = req.Config
	return nil
}

func (m *mockClient) UpdateInstanceConfig(_ context.Context, name string, config map[string]string) error {
	if m.updateConfigErr != nil {
		return m.updateConfigErr
	}
	if m.instanceConfigs[name] == nil {
		m.instanceConfigs[name] = make(map[string]string)
	}
	for k, v := range config {
		m.instanceConfigs[name][k] = v
	}
	return nil
}

func (m *mockClient) AddDevice(_ context.Context, instanceName, deviceName string, device map[string]string) error {
	if m.shiftUnsupported && device["shift"] == "true" {
		return errors.New("shift=true not supported on this host")
	}
	if m.addDeviceErr != nil {
		return m.addDeviceErr
	}
	if m.instanceDevices[instanceName] == nil {
		m.instanceDevices[instanceName] = make(map[string]map[string]string)
	}
	m.instanceDevices[instanceName][deviceName] = device
	return nil
}

func (m *mockClient) StartInstance(_ context.Context, name string) error {
	m.startCallCount++
	if m.startIdmapErrOnFirst && m.startCallCount == 1 {
		return errors.New("Failed to setup device mount: idmapping abilities are required but aren't supported on system")
	}
	if m.startErr != nil {
		return m.startErr
	}
	m.startedInstances = append(m.startedInstances, name)
	return nil
}

func (m *mockClient) ExecInstance(_ context.Context, name string, cmd []string) ([]byte, error) {
	if len(cmd) >= 3 && cmd[0] == "getent" && cmd[1] == "passwd" {
		return []byte(cmd[2] + ":x:1000:1000::/home/" + cmd[2] + ":/bin/zsh\n"), nil
	}
	key := name + ":" + cmd[0]
	if r, ok := m.execResults[key]; ok {
		return r.out, r.err
	}
	return nil, nil
}

func (m *mockClient) WriteFile(_ context.Context, _, path string, content []byte, _, _ int, _ os.FileMode) error {
	if m.writeFileErr != nil {
		return m.writeFileErr
	}
	m.writtenFiles[path] = content
	return nil
}

// ── Validation tests ───────────────────────────────────────────────────────────

func TestValidate_RejectsEmptyName(t *testing.T) {
	opts := provision.LaunchOpts{
		Name:      "",
		Distro:    "alpine",
		Username:  "chad",
		UID:       1000,
		GID:       1000,
		Workspace: "/home/chad/project",
		MountPath: "/workspace",
		Sudo:      true,
	}
	if err := opts.Validate(); err == nil {
		t.Error("expected error for empty Name")
	}
}

func TestValidate_RejectsInvalidName(t *testing.T) {
	bad := []string{
		"-starts-with-dash",
		"has space",
		"has.dot",
		"has/slash",
		"has_underscore", // Incus names: alphanumeric + dash only
	}
	for _, name := range bad {
		opts := provision.LaunchOpts{
			Name:      name,
			Distro:    "alpine",
			Username:  "chad",
			UID:       1000,
			GID:       1000,
			Workspace: "/home/chad/project",
			MountPath: "/workspace",
			Sudo:      true,
		}
		if err := opts.Validate(); err == nil {
			t.Errorf("expected error for Name=%q", name)
		}
	}
}

func TestValidate_AcceptsValidName(t *testing.T) {
	good := []string{"mydev", "my-dev", "dev1", "a", "MYDEV", "My-Dev-1"}
	for _, name := range good {
		opts := provision.LaunchOpts{
			Name:      name,
			Distro:    "alpine",
			Username:  "chad",
			UID:       1000,
			GID:       1000,
			Workspace: "/home/chad/project",
			MountPath: "/workspace",
			Sudo:      true,
		}
		if err := opts.Validate(); err != nil {
			t.Errorf("unexpected error for Name=%q: %v", name, err)
		}
	}
}

func TestValidate_RejectsInvalidUsername(t *testing.T) {
	bad := []string{
		"",
		"0startsdigit",
		"Has-Upper",
		"has space",
		"has.dot",
	}
	for _, u := range bad {
		opts := provision.LaunchOpts{
			Name:      "mydev",
			Distro:    "alpine",
			Username:  u,
			UID:       1000,
			GID:       1000,
			Workspace: "/home/chad/project",
			MountPath: "/workspace",
			Sudo:      true,
		}
		if err := opts.Validate(); err == nil {
			t.Errorf("expected error for Username=%q", u)
		}
	}
}

func TestValidate_AcceptsValidUsername(t *testing.T) {
	good := []string{"chad", "user1", "_admin", "my-user", "a"}
	for _, u := range good {
		opts := provision.LaunchOpts{
			Name:      "mydev",
			Distro:    "alpine",
			Username:  u,
			UID:       1000,
			GID:       1000,
			Workspace: "/home/chad/project",
			MountPath: "/workspace",
			Sudo:      true,
		}
		if err := opts.Validate(); err != nil {
			t.Errorf("unexpected error for Username=%q: %v", u, err)
		}
	}
}

func TestValidate_RejectsInvalidDistro(t *testing.T) {
	opts := provision.LaunchOpts{
		Name:      "mydev",
		Distro:    "fedora",
		Username:  "chad",
		UID:       1000,
		GID:       1000,
		Workspace: "/home/chad/project",
		MountPath: "/workspace",
		Sudo:      true,
	}
	if err := opts.Validate(); err == nil {
		t.Error("expected error for unsupported distro")
	}
}

func TestValidate_AcceptsAlpineAndUbuntu(t *testing.T) {
	for _, distro := range []string{"alpine", "ubuntu"} {
		opts := provision.LaunchOpts{
			Name:      "mydev",
			Distro:    distro,
			Username:  "chad",
			UID:       1000,
			GID:       1000,
			Workspace: "/home/chad/project",
			MountPath: "/workspace",
			Sudo:      true,
		}
		if err := opts.Validate(); err != nil {
			t.Errorf("unexpected error for distro=%q: %v", distro, err)
		}
	}
}

func TestValidate_RejectsRelativeWorkspace(t *testing.T) {
	opts := provision.LaunchOpts{
		Name:      "mydev",
		Distro:    "alpine",
		Username:  "chad",
		UID:       1000,
		GID:       1000,
		Workspace: "relative/path",
		MountPath: "/workspace",
		Sudo:      true,
	}
	if err := opts.Validate(); err == nil {
		t.Error("expected error for relative Workspace path")
	}
}

func TestValidate_RejectsRelativeMountPath(t *testing.T) {
	opts := provision.LaunchOpts{
		Name:      "mydev",
		Distro:    "alpine",
		Username:  "chad",
		UID:       1000,
		GID:       1000,
		Workspace: "/home/chad/project",
		MountPath: "workspace",
		Sudo:      true,
	}
	if err := opts.Validate(); err == nil {
		t.Error("expected error for relative MountPath")
	}
}

func TestValidate_RejectsMalformedProxy(t *testing.T) {
	bad := []string{
		"notaproxy",
		"host:",
		":8080",
		"host:notaport",
		"http://host:8080", // scheme not allowed — plain host:port only
	}
	for _, proxy := range bad {
		opts := provision.LaunchOpts{
			Name:      "mydev",
			Distro:    "alpine",
			Username:  "chad",
			UID:       1000,
			GID:       1000,
			Workspace: "/home/chad/project",
			MountPath: "/workspace",
			Proxy:     proxy,
			Sudo:      true,
		}
		if err := opts.Validate(); err == nil {
			t.Errorf("expected error for Proxy=%q", proxy)
		}
	}
}

func TestValidate_AcceptsValidProxy(t *testing.T) {
	good := []string{"", "localhost:8080", "proxy.corp.com:3128", "10.0.0.1:8080"}
	for _, proxy := range good {
		opts := provision.LaunchOpts{
			Name:      "mydev",
			Distro:    "alpine",
			Username:  "chad",
			UID:       1000,
			GID:       1000,
			Workspace: "/home/chad/project",
			MountPath: "/workspace",
			Proxy:     proxy,
			Sudo:      true,
		}
		if err := opts.Validate(); err != nil {
			t.Errorf("unexpected error for Proxy=%q: %v", proxy, err)
		}
	}
}


// ── Image alias resolution ─────────────────────────────────────────────────────

func TestImageAlias(t *testing.T) {
	cases := []struct {
		distro string
		want   string
	}{
		{"alpine", "ring/alpine:latest"},
		{"ubuntu", "ring/ubuntu:latest"},
	}
	for _, c := range cases {
		got := provision.ImageAlias(c.distro)
		if got != c.want {
			t.Errorf("distro=%q: got %q, want %q", c.distro, got, c.want)
		}
	}
}

// ── Profile sync ──────────────────────────────────────────────────────────────

func TestSyncProfiles_CreatesIfMissing(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	if err := provision.SyncProfiles(ctx, mc); err != nil {
		t.Fatalf("SyncProfiles failed: %v", err)
	}

	if !mc.profiles["ring-base"] {
		t.Error("ring-base profile was not created")
	}
	if !mc.profiles["ring-docker"] {
		t.Error("ring-docker profile was not created")
	}
}

func TestSyncProfiles_SkipsExisting(t *testing.T) {
	mc := newMockClient()
	mc.profiles["ring-base"] = true
	mc.profiles["ring-docker"] = true

	// Track create calls by making createProfile fail if called
	mc.createProfileErr = errors.New("should not be called")

	ctx := context.Background()
	if err := provision.SyncProfiles(ctx, mc); err != nil {
		t.Fatalf("SyncProfiles failed: %v", err)
	}
	// No error means CreateProfile was NOT called for existing profiles.
}

func TestSyncProfiles_PartiallyMissing(t *testing.T) {
	mc := newMockClient()
	mc.profiles["ring-base"] = true
	// ring-docker is missing

	// Only docker create should be called; don't fail on that.
	// But base should not be called (set error that would catch base being called
	// only when it doesn't already exist — can't easily distinguish, so just check result).
	ctx := context.Background()
	if err := provision.SyncProfiles(ctx, mc); err != nil {
		t.Fatalf("SyncProfiles failed: %v", err)
	}
	if !mc.profiles["ring-docker"] {
		t.Error("ring-docker should have been created")
	}
}

// ── Profile list assembly ─────────────────────────────────────────────────────

func TestBuildProfiles(t *testing.T) {
	got := provision.BuildProfiles()
	want := []string{"default", "ring-base", "ring-docker"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

// ── Workspace device ──────────────────────────────────────────────────────────

func TestWorkspaceDevice_CorrectSourceAndPath(t *testing.T) {
	dev := provision.WorkspaceDevice("/home/chad/project", "/workspace")
	if dev["type"] != "disk" {
		t.Errorf("device type: got %q, want disk", dev["type"])
	}
	if dev["source"] != "/home/chad/project" {
		t.Errorf("device source: got %q, want /home/chad/project", dev["source"])
	}
	if dev["path"] != "/workspace" {
		t.Errorf("device path: got %q, want /workspace", dev["path"])
	}
}

// ── Full Launch() ─────────────────────────────────────────────────────────────

func baseOpts() provision.LaunchOpts {
	return provision.LaunchOpts{
		Name:      "mydev",
		Distro:    "alpine",
		Username:  "chad",
		UID:       1000,
		GID:       1000,
		Workspace: "/home/chad/project",
		MountPath: "/workspace",
		Sudo:      true,
	}
}

func TestLaunch_CreatesInstance(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	if err := provision.Launch(ctx, mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	if len(mc.instances) != 1 || mc.instances[0] != "mydev" {
		t.Errorf("expected instance 'mydev' to be created, got: %v", mc.instances)
	}
}

func TestLaunch_SyncsProfiles(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	if err := provision.Launch(ctx, mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	if !mc.profiles["ring-base"] {
		t.Error("ring-base profile must be synced before launch")
	}
}

func TestLaunch_UsesCorrectImageAlias(t *testing.T) {
	cases := []struct {
		distro string
		want   string
	}{
		{"alpine", "ring/alpine:latest"},
		{"ubuntu", "ring/ubuntu:latest"},
	}

	for _, c := range cases {
		mc := newMockClient()
		opts := baseOpts()
		opts.Distro = c.distro

		if err := provision.Launch(context.Background(), mc, opts, io.Discard); err != nil {
			t.Fatalf("distro=%q: Launch failed: %v", c.distro, err)
		}
		if mc.lastImageAlias != c.want {
			t.Errorf("distro=%q: image alias = %q, want %q", c.distro, mc.lastImageAlias, c.want)
		}
	}
}

func TestLaunch_WritesZprofile(t *testing.T) {
	mc := newMockClient()
	if err := provision.Launch(context.Background(), mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	content, ok := mc.writtenFiles["/home/chad/.zprofile"]
	if !ok {
		t.Fatal(".zprofile was not written")
	}
	if !containsStr(string(content), "/workspace") {
		t.Errorf(".zprofile must cd to /workspace, got: %s", content)
	}
}

func TestLaunch_WritesZshrc(t *testing.T) {
	mc := newMockClient()
	if err := provision.Launch(context.Background(), mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	content, ok := mc.writtenFiles["/home/chad/.zshrc"]
	if !ok {
		t.Fatal(".zshrc was not written")
	}
	if !containsStr(string(content), "mise activate zsh") {
		t.Errorf(".zshrc must activate mise, got: %s", content)
	}
}

func TestLaunch_WritesSudoersWhenSudoEnabled(t *testing.T) {
	// Alpine uses doas; Ubuntu uses sudoers.
	cases := []struct {
		distro   string
		wantPath string
		wantStr  string
	}{
		{"alpine", "/etc/doas.conf", "permit nopass"},
		{"ubuntu", "/etc/sudoers.d/chad", "NOPASSWD"},
	}
	for _, tc := range cases {
		t.Run(tc.distro, func(t *testing.T) {
			mc := newMockClient()
			opts := baseOpts()
			opts.Distro = tc.distro
			opts.Sudo = true
			if err := provision.Launch(context.Background(), mc, opts, io.Discard); err != nil {
				t.Fatalf("Launch failed: %v", err)
			}
			content, ok := mc.writtenFiles[tc.wantPath]
			if !ok {
				t.Fatalf("privilege config %q was not written when Sudo=true", tc.wantPath)
			}
			if !containsStr(string(content), tc.wantStr) {
				t.Errorf("expected %q in %s, got: %s", tc.wantStr, tc.wantPath, content)
			}
		})
	}
}

func TestLaunch_NoSudoers_WhenSudoDisabled(t *testing.T) {
	mc := newMockClient()
	opts := baseOpts()
	opts.Sudo = false
	if err := provision.Launch(context.Background(), mc, opts, io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	if _, ok := mc.writtenFiles["/etc/doas.conf"]; ok {
		t.Error("doas.conf must not be written when Sudo=false")
	}
	if _, ok := mc.writtenFiles["/etc/sudoers.d/chad"]; ok {
		t.Error("sudoers file must not be written when Sudo=false")
	}
}

func TestLaunch_MountsWorkspace_ShiftTrue(t *testing.T) {
	// Default: shift=true is supported; device should have shift=true set.
	mc := newMockClient()
	if err := provision.Launch(context.Background(), mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	ws := mc.instanceDevices["mydev"]["workspace"]
	if ws == nil {
		t.Fatal("workspace device not added")
	}
	if ws["shift"] != "true" {
		t.Errorf("expected shift=true on workspace device, got %q", ws["shift"])
	}
	if ws["source"] != "/home/chad/project" {
		t.Errorf("workspace source: got %q, want /home/chad/project", ws["source"])
	}
	if ws["path"] != "/workspace" {
		t.Errorf("workspace path: got %q, want /workspace", ws["path"])
	}
}

func TestLaunch_ShiftTrue_SkipsIdmap(t *testing.T) {
	// When shift=true succeeds, raw.idmap must NOT be set.
	mc := newMockClient()
	opts := baseOpts()
	opts.UID = 1234
	opts.GID = 5678
	if err := provision.Launch(context.Background(), mc, opts, io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	if _, ok := mc.instanceConfigs["mydev"]["raw.idmap"]; ok {
		t.Error("raw.idmap must not be set when shift=true succeeds")
	}
}

func TestLaunch_ShiftFallback_SetsIdmap(t *testing.T) {
	// When shift=true fails, fall back to raw.idmap.
	mc := newMockClient()
	mc.shiftUnsupported = true
	opts := baseOpts()
	opts.UID = 1234
	opts.GID = 5678
	if err := provision.Launch(context.Background(), mc, opts, io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	idmap := mc.instanceConfigs["mydev"]["raw.idmap"]
	if idmap != "both 1234 5678" {
		t.Errorf("raw.idmap: got %q, want %q", idmap, "both 1234 5678")
	}
	// Device must be present without shift.
	ws := mc.instanceDevices["mydev"]["workspace"]
	if ws["shift"] == "true" {
		t.Error("device must not have shift=true when falling back to raw.idmap")
	}
}

func TestLaunch_IdmapFallback_NoError(t *testing.T) {
	// shift=true fails AND raw.idmap fails — launch still succeeds with a warning.
	mc := newMockClient()
	mc.shiftUnsupported = true
	mc.updateConfigErr = errors.New("idmap not supported")
	if err := provision.Launch(context.Background(), mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch must succeed even when both shift and idmap fail: %v", err)
	}
}

func TestLaunch_IdmapStartFails_FallsBackToPrivileged(t *testing.T) {
	// shift=true fails → raw.idmap set → StartInstance fails with idmap error →
	// security.privileged=true set, raw.idmap cleared → second StartInstance succeeds.
	mc := newMockClient()
	mc.shiftUnsupported = true
	mc.startIdmapErrOnFirst = true
	if err := provision.Launch(context.Background(), mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch must succeed after privileged fallback: %v", err)
	}
	if mc.startCallCount != 2 {
		t.Errorf("expected 2 StartInstance calls (first fails, retry succeeds), got %d", mc.startCallCount)
	}
	cfg := mc.instanceConfigs["mydev"]
	if cfg["security.privileged"] != "true" {
		t.Errorf("security.privileged must be set after idmap start failure, got %q", cfg["security.privileged"])
	}
	if cfg["raw.idmap"] != "" {
		t.Errorf("raw.idmap must be cleared after idmap start failure, got %q", cfg["raw.idmap"])
	}
}

func TestLaunch_SetsProxy(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	opts := baseOpts()
	opts.Proxy = "proxy.corp.com:3128"

	if err := provision.Launch(ctx, mc, opts, io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	cfg := mc.instanceConfigs["mydev"]
	if cfg["environment.HTTP_PROXY"] != "http://proxy.corp.com:3128" {
		t.Errorf("HTTP_PROXY: got %q, want http://proxy.corp.com:3128", cfg["environment.HTTP_PROXY"])
	}
	if cfg["environment.HTTPS_PROXY"] != "http://proxy.corp.com:3128" {
		t.Errorf("HTTPS_PROXY: got %q, want http://proxy.corp.com:3128", cfg["environment.HTTPS_PROXY"])
	}
}

func TestLaunch_NoProxy_NoProxyConfig(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	if err := provision.Launch(ctx, mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	cfg := mc.instanceConfigs["mydev"]
	if _, ok := cfg["environment.HTTP_PROXY"]; ok {
		t.Error("HTTP_PROXY must not be set when no proxy configured")
	}
}

func TestLaunch_StartsInstance(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	if err := provision.Launch(ctx, mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	if len(mc.startedInstances) != 1 || mc.startedInstances[0] != "mydev" {
		t.Errorf("expected mydev to be started, got: %v", mc.startedInstances)
	}
}

func TestLaunch_ValidatesInputFirst(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	opts := baseOpts()
	opts.Name = "" // invalid

	if err := provision.Launch(ctx, mc, opts, io.Discard); err == nil {
		t.Error("Launch must return error for invalid opts")
	}
	if len(mc.instances) != 0 {
		t.Error("Launch must not create any instance when validation fails")
	}
}

func TestLaunch_DryRun_DoesNothing(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	opts := baseOpts()
	opts.DryRun = true

	plan, err := provision.DryRun(ctx, opts)
	if err != nil {
		t.Fatalf("DryRun failed: %v", err)
	}

	// Nothing should have been created
	if len(mc.instances) != 0 {
		t.Error("DryRun must not create instances")
	}
	if len(mc.startedInstances) != 0 {
		t.Error("DryRun must not start instances")
	}

	// Plan must describe what would happen
	if plan == "" {
		t.Error("DryRun must return a non-empty plan description")
	}
	if !containsStr(plan, "mydev") {
		t.Errorf("plan must reference instance name, got: %s", plan)
	}
}

func TestLaunch_DockerProfileAlwaysIncluded(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	if err := provision.Launch(ctx, mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	if !mc.profiles["ring-docker"] {
		t.Error("ring-docker profile must always be synced")
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsBytes(s, substr))
}

func containsBytes(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
