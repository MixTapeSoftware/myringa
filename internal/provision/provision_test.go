package provision_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"myringa/internal/provision"
)

// ── Mock client ────────────────────────────────────────────────────────────────

type mockClient struct {
	profiles         map[string]bool // tracks created/existing profiles
	instances        []string        // created instance names
	lastImageAlias   string          // image alias from most recent CreateInstanceFull
	instanceConfigs  map[string]map[string]string
	instanceDevices  map[string]map[string]map[string]string
	startedInstances []string

	// Configurable errors
	createProfileErr      error
	createInstanceFullErr error
	updateConfigErr       error
	addDeviceErr          error
	startErr              error
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
	if m.startErr != nil {
		return m.startErr
	}
	m.startedInstances = append(m.startedInstances, name)
	return nil
}

func (m *mockClient) ExecInstance(_ context.Context, name string, cmd []string) ([]byte, error) {
	key := name + ":" + cmd[0]
	if r, ok := m.execResults[key]; ok {
		return r.out, r.err
	}
	return nil, nil
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

func TestValidate_RejectsDockerWithoutDevTools(t *testing.T) {
	opts := provision.LaunchOpts{
		Name:      "mydev",
		Distro:    "alpine",
		Username:  "chad",
		UID:       1000,
		GID:       1000,
		Workspace: "/home/chad/project",
		MountPath: "/workspace",
		Docker:    true,
		DevTools:  false,
		Sudo:      true,
	}
	if err := opts.Validate(); err == nil {
		t.Error("expected error: Docker=true requires DevTools=true (Docker is baked into -dev images)")
	}
}

func TestValidate_AcceptsDockerWithDevTools(t *testing.T) {
	opts := provision.LaunchOpts{
		Name:      "mydev",
		Distro:    "alpine",
		Username:  "chad",
		UID:       1000,
		GID:       1000,
		Workspace: "/home/chad/project",
		MountPath: "/workspace",
		Docker:    true,
		DevTools:  true,
		Sudo:      true,
	}
	if err := opts.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── Image alias resolution ─────────────────────────────────────────────────────

func TestImageAlias_NoDevTools(t *testing.T) {
	cases := []struct {
		distro string
		want   string
	}{
		{"alpine", "myringa/alpine:latest"},
		{"ubuntu", "myringa/ubuntu:latest"},
	}
	for _, c := range cases {
		got := provision.ImageAlias(c.distro, false)
		if got != c.want {
			t.Errorf("distro=%q devtools=false: got %q, want %q", c.distro, got, c.want)
		}
	}
}

func TestImageAlias_WithDevTools(t *testing.T) {
	cases := []struct {
		distro string
		want   string
	}{
		{"alpine", "myringa/alpine-dev:latest"},
		{"ubuntu", "myringa/ubuntu-dev:latest"},
	}
	for _, c := range cases {
		got := provision.ImageAlias(c.distro, true)
		if got != c.want {
			t.Errorf("distro=%q devtools=true: got %q, want %q", c.distro, got, c.want)
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

	if !mc.profiles["myringa-base"] {
		t.Error("myringa-base profile was not created")
	}
	if !mc.profiles["myringa-docker"] {
		t.Error("myringa-docker profile was not created")
	}
}

func TestSyncProfiles_SkipsExisting(t *testing.T) {
	mc := newMockClient()
	mc.profiles["myringa-base"] = true
	mc.profiles["myringa-docker"] = true

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
	mc.profiles["myringa-base"] = true
	// myringa-docker is missing

	// Only docker create should be called; don't fail on that.
	// But base should not be called (set error that would catch base being called
	// only when it doesn't already exist — can't easily distinguish, so just check result).
	ctx := context.Background()
	if err := provision.SyncProfiles(ctx, mc); err != nil {
		t.Fatalf("SyncProfiles failed: %v", err)
	}
	if !mc.profiles["myringa-docker"] {
		t.Error("myringa-docker should have been created")
	}
}

// ── Profile list assembly ─────────────────────────────────────────────────────

func TestBuildProfiles_NoDocker(t *testing.T) {
	got := provision.BuildProfiles(false)
	want := []string{"default", "myringa-base"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildProfiles_WithDocker(t *testing.T) {
	got := provision.BuildProfiles(true)
	want := []string{"default", "myringa-base", "myringa-docker"}
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

	if !mc.profiles["myringa-base"] {
		t.Error("myringa-base profile must be synced before launch")
	}
}

func TestLaunch_UsesCorrectImageAlias(t *testing.T) {
	cases := []struct {
		distro   string
		devTools bool
		want     string
	}{
		{"alpine", false, "myringa/alpine:latest"},
		{"ubuntu", false, "myringa/ubuntu:latest"},
		{"alpine", true, "myringa/alpine-dev:latest"},
		{"ubuntu", true, "myringa/ubuntu-dev:latest"},
	}

	for _, c := range cases {
		mc := newMockClient()
		opts := baseOpts()
		opts.Distro = c.distro
		opts.DevTools = c.devTools

		if err := provision.Launch(context.Background(), mc, opts, io.Discard); err != nil {
			t.Fatalf("distro=%q dev=%v: Launch failed: %v", c.distro, c.devTools, err)
		}
		if mc.lastImageAlias != c.want {
			t.Errorf("distro=%q dev=%v: image alias = %q, want %q",
				c.distro, c.devTools, mc.lastImageAlias, c.want)
		}
	}
}

func TestLaunch_SetsCloudInitUserData(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	if err := provision.Launch(ctx, mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	cfg := mc.instanceConfigs["mydev"]
	if cfg == nil {
		t.Fatal("no config for mydev")
	}
	if _, ok := cfg["cloud-init.user-data"]; !ok {
		t.Error("cloud-init.user-data must be set on the instance")
	}
	userData := cfg["cloud-init.user-data"]
	if userData == "" {
		t.Error("cloud-init.user-data must not be empty")
	}
	if !containsStr(userData, "#cloud-config") {
		t.Error("cloud-init.user-data must start with #cloud-config")
	}
}

func TestLaunch_MountsWorkspace(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	if err := provision.Launch(ctx, mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	devs := mc.instanceDevices["mydev"]
	if devs == nil {
		t.Fatal("no devices added to mydev")
	}
	ws, ok := devs["workspace"]
	if !ok {
		t.Error("workspace device not added")
	}
	if ws["source"] != "/home/chad/project" {
		t.Errorf("workspace source: got %q, want /home/chad/project", ws["source"])
	}
	if ws["path"] != "/workspace" {
		t.Errorf("workspace path: got %q, want /workspace", ws["path"])
	}
}

func TestLaunch_SetsIdmap(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	opts := baseOpts()
	opts.UID = 1234
	opts.GID = 5678

	if err := provision.Launch(ctx, mc, opts, io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	cfg := mc.instanceConfigs["mydev"]
	idmap, ok := cfg["raw.idmap"]
	if !ok {
		t.Error("raw.idmap must be set when idmap succeeds")
	}
	if idmap != "both 1234 5678" {
		t.Errorf("raw.idmap: got %q, want %q", idmap, "both 1234 5678")
	}
}

func TestLaunch_IdmapFallback_NoError(t *testing.T) {
	mc := newMockClient()
	mc.updateConfigErr = errors.New("idmap not supported")
	ctx := context.Background()

	// Launch should succeed even when idmap fails (fallback path).
	// The warning is logged but not returned as an error.
	if err := provision.Launch(ctx, mc, baseOpts(), io.Discard); err != nil {
		t.Fatalf("Launch must succeed even on idmap failure (fallback): %v", err)
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

func TestLaunch_WithDocker_DockerProfileIncluded(t *testing.T) {
	mc := newMockClient()
	ctx := context.Background()

	opts := baseOpts()
	opts.Docker = true
	opts.DevTools = true

	if err := provision.Launch(ctx, mc, opts, io.Discard); err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	if !mc.profiles["myringa-docker"] {
		t.Error("myringa-docker profile must be synced when Docker=true")
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
