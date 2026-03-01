package images_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"ring/internal/images"
)

// ── Mock BuildClient ───────────────────────────────────────────────────────────

type mockBuildClient struct {
	calls          []string
	errs           map[string]error
	publishedAlias string
}

func newMock() *mockBuildClient {
	return &mockBuildClient{errs: make(map[string]error)}
}

func (m *mockBuildClient) record(call string) { m.calls = append(m.calls, call) }
func (m *mockBuildClient) err(key string) error {
	if e, ok := m.errs[key]; ok {
		return e
	}
	return nil
}

func (m *mockBuildClient) LaunchBuilder(_ context.Context, name, server, _, alias string) error {
	m.record("LaunchBuilder:" + name + ":" + server + ":" + alias)
	return m.err("LaunchBuilder")
}

func (m *mockBuildClient) ExecStream(_ context.Context, name string, cmd []string, _, _ io.Writer) error {
	m.record("ExecStream:" + strings.Join(cmd, " "))
	return m.err("ExecStream:" + cmd[0])
}

func (m *mockBuildClient) StopInstance(_ context.Context, name string) error {
	m.record("StopInstance:" + name)
	return m.err("StopInstance")
}

func (m *mockBuildClient) PublishInstance(_ context.Context, name, alias string) error {
	m.record("PublishInstance:" + name + ":" + alias)
	m.publishedAlias = alias
	return m.err("PublishInstance")
}

func (m *mockBuildClient) DeleteInstance(_ context.Context, name string) error {
	m.record("DeleteInstance:" + name)
	return m.err("DeleteInstance")
}

func (m *mockBuildClient) ImageAliasExists(_ context.Context, alias string) (bool, error) {
	m.record("ImageAliasExists:" + alias)
	if e := m.err("ImageAliasExists"); e != nil {
		return false, e
	}
	// Return true if the errs map has a "DeleteImageAlias" key set,
	// so tests can simulate the overwrite path. Default: alias does not exist.
	_, exists := m.errs["aliasExists"]
	return exists, nil
}

func (m *mockBuildClient) DeleteImageAlias(_ context.Context, alias string) error {
	m.record("DeleteImageAlias:" + alias)
	return m.err("DeleteImageAlias")
}

func (m *mockBuildClient) hasCall(prefix string) bool {
	for _, c := range m.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// ── Validation ─────────────────────────────────────────────────────────────────

func TestBuildOpts_Validate_AcceptsValidDistros(t *testing.T) {
	for _, distro := range []string{"alpine", "ubuntu"} {
		opts := images.BuildOpts{Distro: distro, Tag: "latest"}
		if err := opts.Validate(); err != nil {
			t.Errorf("distro=%q: unexpected error: %v", distro, err)
		}
	}
}

func TestBuildOpts_Validate_RejectsUnknownDistro(t *testing.T) {
	opts := images.BuildOpts{Distro: "fedora", Tag: "latest"}
	if err := opts.Validate(); err == nil {
		t.Error("expected error for unknown distro")
	}
}

func TestBuildOpts_Validate_DefaultsTag(t *testing.T) {
	opts := images.BuildOpts{Distro: "alpine"}
	if err := opts.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Tag != "latest" {
		t.Errorf("Tag: got %q, want latest", opts.Tag)
	}
}

// ── UpstreamLabel ─────────────────────────────────────────────────────────────

func TestUpstreamLabel_Alpine(t *testing.T) {
	got := images.UpstreamLabel("alpine")
	if got != "images:alpine/3.23" {
		t.Errorf("got %q, want images:alpine/3.23", got)
	}
}

func TestUpstreamLabel_Ubuntu(t *testing.T) {
	got := images.UpstreamLabel("ubuntu")
	if got != "images:ubuntu/24.04" {
		t.Errorf("got %q, want images:ubuntu/24.04", got)
	}
}

// ── Target alias ──────────────────────────────────────────────────────────────

func TestTargetAlias(t *testing.T) {
	cases := []struct{ distro, tag, want string }{
		{"alpine", "latest", "ring/alpine:latest"},
		{"ubuntu", "latest", "ring/ubuntu:latest"},
		{"alpine", "v2", "ring/alpine:v2"},
	}
	for _, c := range cases {
		got := images.TargetAlias(c.distro, c.tag)
		if got != c.want {
			t.Errorf("distro=%q tag=%q: got %q, want %q", c.distro, c.tag, got, c.want)
		}
	}
}

// ── Package lists ─────────────────────────────────────────────────────────────

func TestLoadPackages_Alpine(t *testing.T) {
	pkgs, err := images.LoadPackages("alpine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) == 0 {
		t.Error("expected non-empty package list for alpine")
	}
	found := make(map[string]bool)
	for _, p := range pkgs {
		found[p] = true
	}
	for _, must := range []string{"zsh", "git", "curl"} {
		if !found[must] {
			t.Errorf("alpine packages must include %q", must)
		}
	}
	for _, p := range pkgs {
		if p == "" || strings.HasPrefix(p, "#") {
			t.Errorf("package list must not include blank/comment lines, got %q", p)
		}
	}
}

func TestLoadPackages_Ubuntu(t *testing.T) {
	pkgs, err := images.LoadPackages("ubuntu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) == 0 {
		t.Error("expected non-empty package list for ubuntu")
	}
}

func TestLoadPackages_UnknownDistro(t *testing.T) {
	_, err := images.LoadPackages("fedora")
	if err == nil {
		t.Error("expected error for unknown distro")
	}
}

// ── Build() orchestration ─────────────────────────────────────────────────────

func TestBuild_LaunchesBuilderWithCorrectRemoteSource(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// LaunchBuilder must be called with the linuxcontainers server and correct alias
	found := false
	for _, c := range mc.calls {
		if strings.HasPrefix(c, "LaunchBuilder:") &&
			strings.Contains(c, "images.linuxcontainers.org") &&
			strings.Contains(c, "alpine/3.23") {
			found = true
		}
	}
	if !found {
		t.Errorf("LaunchBuilder must use images.linuxcontainers.org + alpine/3.23, calls: %v", mc.calls)
	}
}

func TestBuild_LaunchBeforeExec(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	launchIdx, execIdx := -1, -1
	for i, c := range mc.calls {
		if strings.HasPrefix(c, "LaunchBuilder:") && launchIdx == -1 {
			launchIdx = i
		}
		if strings.HasPrefix(c, "ExecStream:") && execIdx == -1 {
			execIdx = i
		}
	}
	if launchIdx == -1 {
		t.Fatal("LaunchBuilder not called")
	}
	if execIdx == -1 {
		t.Fatal("ExecStream not called")
	}
	if launchIdx > execIdx {
		t.Errorf("LaunchBuilder (idx %d) must come before ExecStream (idx %d)", launchIdx, execIdx)
	}
}

func TestBuild_InstallsPackages_Alpine(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if !mc.hasCall("ExecStream:apk add") {
		t.Errorf("alpine build must run apk add, calls: %v", mc.calls)
	}
	if mc.hasCall("ExecStream:apt-get") {
		t.Error("alpine build must not use apt-get")
	}
}

func TestBuild_InstallsPackages_Ubuntu(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "ubuntu", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if !mc.hasCall("ExecStream:apt-get") {
		t.Errorf("ubuntu build must run apt-get, calls: %v", mc.calls)
	}
	if mc.hasCall("ExecStream:apk") {
		t.Error("ubuntu build must not use apk")
	}
}

func TestBuild_InstallsMise(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	found := false
	for _, c := range mc.calls {
		if strings.HasPrefix(c, "ExecStream:") && strings.Contains(c, "mise") {
			found = true
		}
	}
	if !found {
		t.Errorf("mise must be installed during build, calls: %v", mc.calls)
	}
}

func TestBuild_InstallsOhMyZsh(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	found := false
	for _, c := range mc.calls {
		if strings.Contains(c, "oh-my-zsh") || strings.Contains(c, "ohmyzsh") {
			found = true
		}
	}
	if !found {
		t.Errorf("build must always install oh-my-zsh, calls: %v", mc.calls)
	}
}

func TestBuild_InstallsDockerPackages(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "ubuntu", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	found := false
	for _, c := range mc.calls {
		if strings.Contains(c, "docker") {
			found = true
		}
	}
	if !found {
		t.Errorf("build must always install docker packages, calls: %v", mc.calls)
	}
}

func TestBuild_NoDockerCurlSh(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "ubuntu", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	for _, c := range mc.calls {
		if strings.Contains(c, "get.docker.com") {
			t.Errorf("docker must not be installed via curl|sh, got: %s", c)
		}
	}
}

func TestBuild_StopsBeforePublish(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	stopIdx, publishIdx := -1, -1
	for i, c := range mc.calls {
		if strings.HasPrefix(c, "StopInstance:") && stopIdx == -1 {
			stopIdx = i
		}
		if strings.HasPrefix(c, "PublishInstance:") {
			publishIdx = i
		}
	}
	if stopIdx == -1 {
		t.Fatal("StopInstance not called")
	}
	if publishIdx == -1 {
		t.Fatal("PublishInstance not called")
	}
	if stopIdx > publishIdx {
		t.Errorf("StopInstance (idx %d) must come before PublishInstance (idx %d)", stopIdx, publishIdx)
	}
}

func TestBuild_PublishesCorrectAlias(t *testing.T) {
	cases := []struct {
		distro string
		tag    string
		want   string
	}{
		{"alpine", "latest", "ring/alpine:latest"},
		{"ubuntu", "latest", "ring/ubuntu:latest"},
		{"alpine", "v2", "ring/alpine:v2"},
	}

	for _, c := range cases {
		mc := newMock()
		opts := images.BuildOpts{Distro: c.distro, Tag: c.tag}
		var out bytes.Buffer

		if err := images.Build(context.Background(), mc, opts, &out); err != nil {
			t.Fatalf("distro=%q tag=%q: Build failed: %v", c.distro, c.tag, err)
		}
		if mc.publishedAlias != c.want {
			t.Errorf("distro=%q tag=%q: published alias = %q, want %q",
				c.distro, c.tag, mc.publishedAlias, c.want)
		}
	}
}

func TestBuild_DeletesBuilderAfterSuccess(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if !mc.hasCall("DeleteInstance:") {
		t.Error("builder instance must be deleted after successful build")
	}
}

func TestBuild_DeletesBuilderAfterFailure(t *testing.T) {
	mc := newMock()
	mc.errs["ExecStream:apk"] = errors.New("apk failed")
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	err := images.Build(context.Background(), mc, opts, &out)
	if err == nil {
		t.Fatal("expected Build to fail when exec fails")
	}
	if !mc.hasCall("DeleteInstance:") {
		t.Error("builder instance must be deleted even when build fails")
	}
}

func TestBuild_StreamsOutputToWriter(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if out.Len() == 0 {
		t.Error("Build must write progress output to the writer")
	}
}

func TestBuild_OverwritesExistingAlias(t *testing.T) {
	mc := newMock()
	mc.errs["aliasExists"] = errors.New("sentinel") // causes ImageAliasExists to return true
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if !mc.hasCall("DeleteImageAlias:") {
		t.Error("Build must delete existing alias before publishing when alias exists")
	}
	if !mc.hasCall("PublishInstance:") {
		t.Error("Build must still publish after deleting existing alias")
	}
}

func TestBuild_SkipsDeleteWhenAliasAbsent(t *testing.T) {
	mc := newMock()
	// no "aliasExists" key → ImageAliasExists returns false
	opts := images.BuildOpts{Distro: "alpine", Tag: "latest"}
	var out bytes.Buffer

	if err := images.Build(context.Background(), mc, opts, &out); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if mc.hasCall("DeleteImageAlias:") {
		t.Error("Build must not call DeleteImageAlias when alias does not exist")
	}
}

func TestBuild_ValidatesOptsFirst(t *testing.T) {
	mc := newMock()
	opts := images.BuildOpts{Distro: "fedora", Tag: "latest"}
	var out bytes.Buffer

	err := images.Build(context.Background(), mc, opts, &out)
	if err == nil {
		t.Error("Build must return error for invalid distro")
	}
	if len(mc.calls) != 0 {
		t.Errorf("Build must not call client when validation fails, got calls: %v", mc.calls)
	}
}
