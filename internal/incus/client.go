package incus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	"gopkg.in/yaml.v2"

	"ring/internal/format"
)

// InstanceRow holds formatted data for a single instance.
type InstanceRow struct {
	Name, Type, Status, CPU, Memory, Disk, IPv4 string
	CPULimit    string
	MemoryLimit string
}

// SnapshotInfo holds summary data for an instance snapshot.
type SnapshotInfo struct {
	Name      string
	CreatedAt time.Time
	Stateful  bool
	Size      int64
}

// ImageInfo holds summary data for an image alias.
type ImageInfo struct {
	Alias       string
	Description string
}

// ProfileInfo holds summary data for a profile.
type ProfileInfo struct {
	Name        string
	Description string
}

// CPUSnapshot stores the last known CPU usage for delta calculation.
type CPUSnapshot struct {
	UsageNS   int64
	Timestamp time.Time
}

// Client is the interface for all Incus operations.
type Client interface {
	// UI operations
	FetchInstances(ctx context.Context, prev map[string]CPUSnapshot) ([]InstanceRow, map[string]CPUSnapshot, error)
	StartInstance(ctx context.Context, name string) error
	StopInstance(ctx context.Context, name string) error
	RestartInstance(ctx context.Context, name string) error
	DeleteInstance(ctx context.Context, name string) error
	CreateSnapshot(ctx context.Context, instanceName, snapshotName string) error
	RestoreSnapshot(ctx context.Context, instanceName, snapshotName string) error
	DeleteSnapshot(ctx context.Context, instanceName, snapshotName string) error
	ListSnapshots(ctx context.Context, instanceName string) ([]SnapshotInfo, error)
	ListImages(ctx context.Context) ([]ImageInfo, error)
	ListProfiles(ctx context.Context) ([]ProfileInfo, error)
	CreateInstance(ctx context.Context, name, imageAlias, profile string) error

	// GetInstanceState returns the status string ("Running", "Stopped", etc.)
	// for the named instance.
	GetInstanceState(ctx context.Context, name string) (string, error)

	// Provisioning operations
	ProfileExists(ctx context.Context, name string) (bool, error)
	CreateProfile(ctx context.Context, name string, yamlData string) error
	ImageAliasExists(ctx context.Context, alias string) (bool, error)
	DeleteImageAlias(ctx context.Context, alias string) error
	// LaunchBuilder creates and starts an instance from a remote image source.
	// Used by the image builder; server is a full URL, protocol is "simplestreams".
	LaunchBuilder(ctx context.Context, name, server, protocol, alias string) error
	ExecStream(ctx context.Context, name string, cmd []string, stdout, stderr io.Writer) error
	PublishInstance(ctx context.Context, name, alias string) error
	CreateInstanceFull(ctx context.Context, req api.InstancesPost) error
	UpdateInstanceConfig(ctx context.Context, name string, config map[string]string) error
	AddDevice(ctx context.Context, instanceName, deviceName string, device map[string]string) error
	ExecInstance(ctx context.Context, instanceName string, cmd []string) ([]byte, error)
	WriteFile(ctx context.Context, instance, path string, content []byte, uid, gid int, mode os.FileMode) error
}

// client implements Client using the Incus SDK over a local Unix socket.
type client struct {
	conn incusclient.InstanceServer
}

// Connect returns a Client connected to the local Incus daemon.
func Connect() (Client, error) {
	conn, err := incusclient.ConnectIncusUnix("", nil)
	if err != nil {
		return nil, err
	}
	return &client{conn: conn}, nil
}

// FetchInstances retrieves all instances and their state in one call,
// computes CPU deltas from prev snapshots, and returns formatted rows
// sorted by name.
func (c *client) FetchInstances(_ context.Context, prev map[string]CPUSnapshot) ([]InstanceRow, map[string]CPUSnapshot, error) {
	instances, err := c.conn.GetInstancesFull(api.InstanceTypeAny)
	if err != nil {
		return nil, nil, err
	}

	rows := make([]InstanceRow, 0, len(instances))
	snaps := make(map[string]CPUSnapshot, len(instances))
	now := time.Now()

	for _, inst := range instances {
		row := InstanceRow{
			Name:   inst.Name,
			Type:   instanceType(inst.Type),
			Status: inst.Status,
			CPU:    "—",
			Memory: "—",
			Disk:   "—",
			IPv4:   "—",
		}

		if v := inst.Config["limits.cpu"]; v != "" {
			row.CPULimit = v
		}
		if v := inst.Config["limits.memory"]; v != "" {
			row.MemoryLimit = v
		}

		if inst.State != nil {
			// CPU — delta from previous snapshot
			curNS := inst.State.CPU.Usage
			snaps[inst.Name] = CPUSnapshot{UsageNS: curNS, Timestamp: now}

			if p, ok := prev[inst.Name]; ok && inst.Status == "Running" {
				deltaNS := curNS - p.UsageNS
				elapsed := now.Sub(p.Timestamp).Seconds()
				if deltaNS >= 0 && elapsed > 0 {
					pct := float64(deltaNS) / (elapsed * 1e9) * 100
					if cores, ok := parseCPULimit(row.CPULimit); ok {
						pct /= cores
					}
					row.CPU = fmt.Sprintf("%.1f%%", pct)
				}
			}

			// Memory
			if inst.State.Memory.Usage > 0 || inst.State.Memory.Total > 0 {
				row.Memory = format.Memory(inst.State.Memory.Usage, inst.State.Memory.Total)
			}

			// Disk
			row.Disk = rootDiskUsage(inst.State.Disk)

			// IPv4
			row.IPv4 = allIPv4(inst.State.Network)
		}

		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})

	return rows, snaps, nil
}

func (c *client) StartInstance(ctx context.Context, name string) error {
	op, err := c.conn.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}, "")
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) StopInstance(ctx context.Context, name string) error {
	op, err := c.conn.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  "stop",
		Timeout: 30,
	}, "")
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) RestartInstance(ctx context.Context, name string) error {
	op, err := c.conn.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  "restart",
		Timeout: 30,
	}, "")
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) DeleteInstance(ctx context.Context, name string) error {
	op, err := c.conn.DeleteInstance(name)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) ListImages(_ context.Context) ([]ImageInfo, error) {
	aliases, err := c.conn.GetImageAliases()
	if err != nil {
		return nil, err
	}
	images := make([]ImageInfo, 0, len(aliases))
	for _, a := range aliases {
		images = append(images, ImageInfo{
			Alias:       a.Name,
			Description: a.Description,
		})
	}
	sort.Slice(images, func(i, j int) bool {
		return images[i].Alias < images[j].Alias
	})
	return images, nil
}

func (c *client) ListProfiles(_ context.Context) ([]ProfileInfo, error) {
	profiles, err := c.conn.GetProfiles()
	if err != nil {
		return nil, err
	}
	result := make([]ProfileInfo, 0, len(profiles))
	for _, p := range profiles {
		result = append(result, ProfileInfo{
			Name:        p.Name,
			Description: p.Description,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (c *client) CreateInstance(ctx context.Context, name, imageAlias, profile string) error {
	req := api.InstancesPost{
		Name: name,
		Source: api.InstanceSource{
			Type:  "image",
			Alias: imageAlias,
		},
		InstancePut: api.InstancePut{
			Profiles: []string{profile},
		},
	}
	op, err := c.conn.CreateInstance(req)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) CreateSnapshot(ctx context.Context, instanceName, snapshotName string) error {
	op, err := c.conn.CreateInstanceSnapshot(instanceName, api.InstanceSnapshotsPost{
		Name: snapshotName,
	})
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) RestoreSnapshot(ctx context.Context, instanceName, snapshotName string) error {
	op, err := c.conn.UpdateInstance(instanceName, api.InstancePut{
		Restore: snapshotName,
	}, "")
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) DeleteSnapshot(ctx context.Context, instanceName, snapshotName string) error {
	op, err := c.conn.DeleteInstanceSnapshot(instanceName, snapshotName)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) ListSnapshots(_ context.Context, instanceName string) ([]SnapshotInfo, error) {
	snapshots, err := c.conn.GetInstanceSnapshots(instanceName)
	if err != nil {
		return nil, err
	}
	result := make([]SnapshotInfo, 0, len(snapshots))
	for _, s := range snapshots {
		result = append(result, SnapshotInfo{
			Name:      s.Name,
			CreatedAt: s.CreatedAt,
			Stateful:  s.Stateful,
			Size:      s.Size,
		})
	}
	return result, nil
}

func (c *client) GetInstanceState(_ context.Context, name string) (string, error) {
	state, _, err := c.conn.GetInstanceState(name)
	if err != nil {
		return "", err
	}
	return state.Status, nil
}

func (c *client) ProfileExists(_ context.Context, name string) (bool, error) {
	profiles, err := c.conn.GetProfileNames()
	if err != nil {
		return false, err
	}
	for _, p := range profiles {
		if p == name {
			return true, nil
		}
	}
	return false, nil
}

func (c *client) ImageAliasExists(_ context.Context, alias string) (bool, error) {
	_, _, err := c.conn.GetImageAlias(alias)
	if err != nil {
		// Incus returns a 404-style error when the alias doesn't exist.
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *client) DeleteImageAlias(_ context.Context, alias string) error {
	return c.conn.DeleteImageAlias(alias)
}

func (c *client) CreateProfile(_ context.Context, name string, yamlData string) error {
	var req api.ProfilesPost
	req.Name = name
	// Parse YAML into profile config/devices via api types.
	// We use the Incus API's profile YAML format directly.
	var profilePut api.ProfilePut
	if err := parseProfileYAML(yamlData, &profilePut); err != nil {
		return fmt.Errorf("parsing profile YAML: %w", err)
	}
	req.ProfilePut = profilePut
	return c.conn.CreateProfile(req)
}

func (c *client) CreateInstanceFull(ctx context.Context, req api.InstancesPost) error {
	op, err := c.conn.CreateInstance(req)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) UpdateInstanceConfig(ctx context.Context, name string, config map[string]string) error {
	inst, etag, err := c.conn.GetInstance(name)
	if err != nil {
		return err
	}
	if inst.Config == nil {
		inst.Config = make(map[string]string)
	}
	for k, v := range config {
		inst.Config[k] = v
	}
	op, err := c.conn.UpdateInstance(name, inst.InstancePut, etag)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) AddDevice(ctx context.Context, instanceName, deviceName string, device map[string]string) error {
	inst, etag, err := c.conn.GetInstance(instanceName)
	if err != nil {
		return err
	}
	if inst.Devices == nil {
		inst.Devices = make(map[string]map[string]string)
	}
	inst.Devices[deviceName] = device
	op, err := c.conn.UpdateInstance(instanceName, inst.InstancePut, etag)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) WriteFile(_ context.Context, instance, path string, content []byte, uid, gid int, mode os.FileMode) error {
	args := incusclient.InstanceFileArgs{
		Content:   bytes.NewReader(content),
		UID:       int64(uid),
		GID:       int64(gid),
		Mode:      int(mode),
		Type:      "file",
		WriteMode: "overwrite",
	}
	return c.conn.CreateInstanceFile(instance, path, args)
}

func (c *client) ExecInstance(_ context.Context, instanceName string, cmd []string) ([]byte, error) {
	var stdout, stderr strings.Builder
	execReq := api.InstanceExecPost{
		Command:      cmd,
		WaitForWS:    true, // required for stdout/stderr capture via websocket
		Interactive:  false,
		RecordOutput: false,
	}
	args := incusclient.InstanceExecArgs{
		Stdout: &stringWriter{&stdout},
		Stderr: &stringWriter{&stderr},
	}
	op, err := c.conn.ExecInstance(instanceName, execReq, &args)
	if err != nil {
		return nil, err
	}
	if err := op.Wait(); err != nil {
		return nil, err
	}
	// Surface non-zero exit codes — op.Wait() succeeds even when the command fails.
	if meta := op.Get().Metadata; meta != nil {
		if retVal, ok := meta["return"]; ok {
			if code, ok := retVal.(float64); ok && code != 0 {
				combined := strings.TrimSpace(stdout.String() + stderr.String())
				return []byte(combined), fmt.Errorf("exit %d: %s", int(code), combined)
			}
		}
	}
	return []byte(stdout.String()), nil
}

func (c *client) LaunchBuilder(ctx context.Context, name, server, protocol, alias string) error {
	req := api.InstancesPost{
		Name: name,
		Source: api.InstanceSource{
			Type:     "image",
			Server:   server,
			Protocol: protocol,
			Alias:    alias,
		},
		InstancePut: api.InstancePut{
			Profiles: []string{"default"},
		},
	}
	op, err := c.conn.CreateInstance(req)
	if err != nil {
		return err
	}
	if err := op.WaitContext(ctx); err != nil {
		return err
	}
	return c.StartInstance(ctx, name)
}

func (c *client) ExecStream(_ context.Context, name string, cmd []string, stdout, stderr io.Writer) error {
	req := api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true, // required to connect I/O and receive exit code
		Interactive: false,
	}
	args := &incusclient.InstanceExecArgs{
		Stdout: stdout,
		Stderr: stderr,
	}
	op, err := c.conn.ExecInstance(name, req, args)
	if err != nil {
		return err
	}
	if err := op.Wait(); err != nil {
		return err
	}
	// op.Wait() succeeds even when the command exits non-zero; check metadata.
	if meta := op.Get().Metadata; meta != nil {
		if retVal, ok := meta["return"]; ok {
			if code, ok := retVal.(float64); ok && code != 0 {
				return fmt.Errorf("exit %d", int(code))
			}
		}
	}
	return nil
}

func (c *client) PublishInstance(ctx context.Context, name, alias string) error {
	req := api.ImagesPost{
		Source: &api.ImagesPostSource{
			Type: "instance",
			Name: name,
		},
		Aliases: []api.ImageAlias{{Name: alias}},
	}
	op, err := c.conn.CreateImage(req, nil)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

// stringWriter adapts strings.Builder to io.Writer.
type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) {
	return w.b.Write(p)
}

// isNotFound reports whether an Incus SDK error is a 404 / not-found response.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "not found") || strings.Contains(s, "404")
}

// parseProfileYAML parses a ring profile YAML into an api.ProfilePut.
// Our YAML format mirrors the Incus profile format: config: {} and devices: {}.
func parseProfileYAML(data string, out *api.ProfilePut) error {
	// Use gopkg.in/yaml.v2 compatible intermediate struct.
	var raw struct {
		Description string                       `yaml:"description"`
		Config      map[string]string            `yaml:"config"`
		Devices     map[string]map[string]string `yaml:"devices"`
	}
	if err := yaml.Unmarshal([]byte(data), &raw); err != nil {
		return err
	}
	out.Description = raw.Description
	out.Config = raw.Config
	out.Devices = raw.Devices
	return nil
}

// Unexported helpers

func allIPv4(networks map[string]api.InstanceStateNetwork) string {
	names := make([]string, 0, len(networks))
	for name := range networks {
		if name == "lo" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var ips []string
	for _, name := range names {
		for _, addr := range networks[name].Addresses {
			if addr.Family == "inet" && addr.Scope == "global" {
				ips = append(ips, addr.Address)
			}
		}
	}
	if len(ips) == 0 {
		return "—"
	}
	return strings.Join(ips, ", ")
}

func rootDiskUsage(disks map[string]api.InstanceStateDisk) string {
	if d, ok := disks["root"]; ok && d.Usage > 0 {
		return format.Bytes(d.Usage)
	}
	var total int64
	for _, d := range disks {
		total += d.Usage
	}
	if total > 0 {
		return format.Bytes(total)
	}
	return "—"
}

// parseCPULimit parses an Incus limits.cpu value into a core count.
// Accepts an integer ("4") or a CPU range ("0-3"). Returns false if
// the value is empty or cannot be parsed.
func parseCPULimit(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	if i := strings.Index(s, "-"); i >= 0 {
		lo, err1 := strconv.Atoi(s[:i])
		hi, err2 := strconv.Atoi(s[i+1:])
		if err1 != nil || err2 != nil || hi < lo {
			return 0, false
		}
		return float64(hi-lo+1), true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return float64(n), true
}

func instanceType(t string) string {
	switch t {
	case "container":
		return "CT"
	case "virtual-machine":
		return "VM"
	default:
		return t
	}
}
