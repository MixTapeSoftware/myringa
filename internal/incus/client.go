package incus

import (
	"fmt"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
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

// Connect returns an Incus client connected via the local Unix socket.
func Connect() (incus.InstanceServer, error) {
	return incus.ConnectIncusUnix("", nil)
}

// FetchInstances retrieves all instances and their state in one call,
// computes CPU deltas from prev snapshots, and returns formatted rows.
func FetchInstances(c incus.InstanceServer, prev map[string]CPUSnapshot) ([]InstanceRow, map[string]CPUSnapshot, error) {
	instances, err := c.GetInstancesFull(api.InstanceTypeAny)
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

		// Resource limits from config
		if v := inst.Config["limits.cpu"]; v != "" {
			row.CPULimit = v
		}
		if v := inst.Config["limits.memory"]; v != "" {
			row.MemoryLimit = v
		}

		if inst.State != nil {
			// CPU
			curNS := inst.State.CPU.Usage
			snaps[inst.Name] = CPUSnapshot{UsageNS: curNS, Timestamp: now}

			if p, ok := prev[inst.Name]; ok && inst.Status == "Running" {
				deltaNS := curNS - p.UsageNS
				elapsed := now.Sub(p.Timestamp).Seconds()
				if deltaNS >= 0 && elapsed > 0 {
					pct := float64(deltaNS) / (elapsed * 1e9) * 100
					row.CPU = fmt.Sprintf("%.1f%%", pct)
				}
			}

			// Memory
			if inst.State.Memory.Usage > 0 || inst.State.Memory.Total > 0 {
				row.Memory = formatMemory(inst.State.Memory.Usage, inst.State.Memory.Total)
			}

			// Disk
			row.Disk = rootDiskUsage(inst.State.Disk)

			// IPv4
			row.IPv4 = allIPv4(inst.State.Network)
		}

		rows = append(rows, row)
	}

	return rows, snaps, nil
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.0fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func formatMemory(used, total int64) string {
	if total > 0 {
		return fmt.Sprintf("%s/%s", formatBytes(used), formatBytes(total))
	}
	return formatBytes(used)
}

func allIPv4(networks map[string]api.InstanceStateNetwork) string {
	// Sort interface names for stable output
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
		return formatBytes(d.Usage)
	}
	// Sum all disk usage as fallback
	var total int64
	for _, d := range disks {
		total += d.Usage
	}
	if total > 0 {
		return formatBytes(total)
	}
	return "—"
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

// StartInstance starts a stopped instance.
func StartInstance(c incus.InstanceServer, name string) error {
	op, err := c.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}, "")
	if err != nil {
		return err
	}
	return op.Wait()
}

// StopInstance stops a running instance.
func StopInstance(c incus.InstanceServer, name string) error {
	op, err := c.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  "stop",
		Timeout: 30,
	}, "")
	if err != nil {
		return err
	}
	return op.Wait()
}

// RestartInstance restarts an instance.
func RestartInstance(c incus.InstanceServer, name string) error {
	op, err := c.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  "restart",
		Timeout: 30,
	}, "")
	if err != nil {
		return err
	}
	return op.Wait()
}

// DeleteInstance removes an instance. It must be stopped first.
func DeleteInstance(c incus.InstanceServer, name string) error {
	op, err := c.DeleteInstance(name)
	if err != nil {
		return err
	}
	return op.Wait()
}

// ListImages returns all image aliases available on the server.
func ListImages(c incus.InstanceServer) ([]ImageInfo, error) {
	aliases, err := c.GetImageAliases()
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

// ListProfiles returns all profiles available on the server.
func ListProfiles(c incus.InstanceServer) ([]ProfileInfo, error) {
	profiles, err := c.GetProfiles()
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

// CreateInstance creates a new instance from an image alias and profile.
func CreateInstance(c incus.InstanceServer, name, imageAlias, profile string) error {
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
	op, err := c.CreateInstance(req)
	if err != nil {
		return err
	}
	return op.Wait()
}

// CreateSnapshot takes a snapshot of an instance.
func CreateSnapshot(c incus.InstanceServer, instanceName, snapshotName string) error {
	op, err := c.CreateInstanceSnapshot(instanceName, api.InstanceSnapshotsPost{
		Name: snapshotName,
	})
	if err != nil {
		return err
	}
	return op.Wait()
}

// RestoreSnapshot restores an instance to a snapshot.
func RestoreSnapshot(c incus.InstanceServer, instanceName, snapshotName string) error {
	op, err := c.UpdateInstance(instanceName, api.InstancePut{
		Restore: snapshotName,
	}, "")
	if err != nil {
		return err
	}
	return op.Wait()
}

// DeleteSnapshot removes a snapshot from an instance.
func DeleteSnapshot(c incus.InstanceServer, instanceName, snapshotName string) error {
	op, err := c.DeleteInstanceSnapshot(instanceName, snapshotName)
	if err != nil {
		return err
	}
	return op.Wait()
}

// ListSnapshots returns all snapshots for an instance.
func ListSnapshots(c incus.InstanceServer, instanceName string) ([]SnapshotInfo, error) {
	snapshots, err := c.GetInstanceSnapshots(instanceName)
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
