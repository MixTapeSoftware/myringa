# Plan: Add `--mount` flag to `ring launch`

## Context

Enable per-container selective access to host directories (e.g., Dropbox-synced folders) via a repeatable `--mount` flag. The user's setup: Dropbox syncs folders to a remote Linux host, and containers need read/write access to specific subfolders. This is independent of the existing `--workspace` mount.

## Changes

### 1. New `MountSpec` type + `ExtraMounts` field on `LaunchOpts`

**File:** `internal/provision/provision.go`

```go
type MountSpec struct {
    HostPath      string // absolute host path
    ContainerPath string // absolute container path
}
```

Add `ExtraMounts []MountSpec` to `LaunchOpts`.

### 2. Validation

**File:** `internal/provision/provision.go` — `Validate()`

For each `MountSpec`:
- Both `HostPath` and `ContainerPath` must be absolute paths
- `ContainerPath` must not conflict with `MountPath` (the workspace mount)
- No duplicate `ContainerPath` values across extra mounts
- `HostPath` must exist on the host (`os.Stat` check) — fail early with a clear error rather than a confusing Incus error

### 3. Mount logic in `Launch()`

**File:** `internal/provision/provision.go`

The existing workspace mount determines shift support at the instance level. After the workspace device is added, add each extra mount using the same strategy:

- If shift=true succeeded for workspace → add extras with shift=true
- If shift fell back → add extras without shift (they inherit instance-level raw.idmap or security.privileged)

Device names: `"mount-0"`, `"mount-1"`, etc. (workspace stays `"workspace"`).

Refactor: extract a `shiftWorked` bool from the workspace mount block, then loop over `ExtraMounts`.

### 4. DryRun output

**File:** `internal/provision/provision.go` — `DryRun()`

Add a line per extra mount: `  Mount[0]:  /host/path → /container/path`

### 5. CLI flag parsing

**File:** `main.go`

Go's `flag` package doesn't natively support repeatable flags. Add a `stringSlice` type implementing `flag.Value`:

```go
type stringSlice []string
func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }
```

Register: `fs.Var(&mounts, "mount", "/host/path:/container/path (repeatable)")`

Parse each value by splitting on `:` → `MountSpec`. Validate format (must have exactly one `:` separating two absolute paths).

Update `isBoolFlag` — `"mount"` is NOT a bool flag.

Update `launchUsage` to document `--mount`.

### 6. Tests

**File:** `internal/provision/provision_test.go`

- `TestValidate_RejectsRelativeExtraMountHostPath`
- `TestValidate_RejectsRelativeExtraMountContainerPath`
- `TestValidate_RejectsDuplicateExtraMountContainerPath`
- `TestValidate_RejectsExtraMountConflictingWithWorkspace`
- `TestValidate_AcceptsValidExtraMounts`
- `TestLaunch_AddsExtraMounts_ShiftTrue` — verify devices `mount-0`, `mount-1` with shift=true
- `TestLaunch_AddsExtraMounts_ShiftFallback` — verify devices added without shift when `shiftUnsupported`
- `TestDryRun_ShowsExtraMounts`

**File:** `cmd_test.go`

- `TestParseLaunchFlags_Mount_Single`
- `TestParseLaunchFlags_Mount_Multiple`
- `TestParseLaunchFlags_Mount_InvalidFormat` (no colon, relative paths)

Note: Host path existence check happens at CLI parse time (`main.go`), not in `Validate()`, since `Validate()` is a pure validation function used by tests with mock paths. The `os.Stat` check goes in `parseLaunchFlags` alongside other CLI-level validation.

## Verification

1. `go test ./...` — all existing + new tests pass
2. `go build` — compiles cleanly
3. Manual: `ring launch mydev --mount /home/chad/Dropbox/notes:/notes --dry-run` shows the extra mount in output
