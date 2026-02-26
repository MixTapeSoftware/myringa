# Plan 4: Host Setup & Networking

> Diagnose and fix host configuration (storage, firewall, networking), simplify port forwarding, surface network state in the TUI.

## Who This Is For

Same user as Plan 3 — a developer running Incus containers on their own machine. The two biggest pain points for Incus newcomers are storage backend setup (defaulting to slow loopback) and networking (UFW blocks container traffic, port forwarding is verbose). Myringa should help users get their host right before they create their first container, and make network state visible once they're running.

---

## Part 1: Storage Pool Diagnostics & Setup

### The Problem

Default Incus installs use a `dir` or `btrfs` loopback file as the storage backend. This works but is slow (no copy-on-write for dir, loopback adds overhead) and makes snapshots expensive. ZFS on a real partition is dramatically better — instant snapshots, free clones, transparent compression — but requires partitioning decisions that intimidate newcomers.

This is a **first-run decision**: migrating storage pools later requires moving all instances, which is destructive. Get it right before creating containers.

### `myringa doctor` storage checks

```
myringa doctor

Storage:
  ✓ Storage pool "default" exists
  ⚠ Backend: dir (slow, no copy-on-write snapshots)
    → Consider ZFS for better performance. Run: myringa setup storage
  ✓ Pool has 45 GiB free
```

| Check | How | Pass | Warn/Fail |
|-------|-----|------|-----------|
| Pool exists | SDK `GetStoragePools()` | At least one pool | "No storage pool configured — run: myringa setup storage" |
| Backend type | Pool driver field | zfs or btrfs | dir → warn about performance; loopback → warn about overhead |
| Free space | Pool usage stats | >10 GiB free | Warn if low |

### `myringa setup storage` (guided creation)

Interactive (allowed here — this is a one-time setup command, not a launch-time prompt):

```
myringa setup storage

Choose a storage backend:

  1. dir         — Simple, no setup required. Slow snapshots. (current)
  2. btrfs       — Good performance, uses a loopback file. Default Incus choice.
  3. zfs-loopback — ZFS on a loopback file. Fast snapshots, compression.
                   Better than btrfs, no spare disk needed.
  4. zfs-disk    — ZFS on a dedicated partition or disk. Best performance.
                   Requires an available partition (e.g., /dev/sdb).

Choice [1-4]:
```

For option 3 (zfs-loopback):
```
ZFS loopback file size [50GiB]:
Pool name [default]:

Will run:
  incus storage create default zfs size=50GiB

Proceed? [y/N]
```

For option 4 (zfs-disk):
```
Available disks/partitions:
  /dev/sdb   100 GiB  (unused)
  /dev/sdc1   50 GiB  (unused)

Select device: /dev/sdb
Pool name [default]:

⚠ This will FORMAT /dev/sdb. All data on this device will be lost.

Will run:
  incus storage create default zfs source=/dev/sdb

Proceed? [y/N]
```

### Edge cases

- Pool already exists with instances → warn that migration is destructive, suggest creating a second pool instead
- ZFS kernel module not loaded → detect and suggest `sudo modprobe zfs` or `sudo apt install zfsutils-linux`
- No spare disks for zfs-disk → gracefully fall back to recommending zfs-loopback
- Non-Ubuntu hosts → ZFS availability varies; detect and skip if not available

### Implementation

- Fold into `internal/doctor/` alongside network checks
- Storage-specific: `CheckStoragePool()`, `SetupStorage()`
- Interactive prompts only in `myringa setup storage`, never in `myringa doctor` (doctor is read-only diagnostic)
- ~100-150 lines for checks, ~100 lines for guided setup

---

## Part 2: Network Diagnostics (`myringa doctor`)

A diagnostic command that checks host networking health and guides the user to fix issues. Does NOT silently modify firewall rules — diagnoses, explains, and offers explicit opt-in fixes.

### Checks (in order)

```
myringa doctor

Storage:
  ✓ Storage pool "default" (zfs)
  ✓ 45 GiB free

Network:
  ✓ Incus daemon running
  ✓ Bridge incusbr0 exists (10.104.73.1/24)
  ✓ UFW active
  ✗ UFW FORWARD policy blocks container traffic
    → Run: ufw route allow in on incusbr0
    → Run: ufw route allow out on incusbr0
    → Or: myringa doctor --fix (applies both rules)
  ✓ NAT masquerade rule present
  ✗ Container internet test failed (ping 1.1.1.1)
    → Likely caused by UFW FORWARD issue above
```

### Check details

| Check | How | Pass | Fail action |
|-------|-----|------|-------------|
| Incus daemon | Connect via SDK | Connected | "Is Incus installed and running?" |
| Bridge exists | `ip link show incusbr0` or SDK network info | Has IPv4 | "Run: incus network create incusbr0" |
| UFW status | `ufw status` or parse `/etc/ufw/ufw.conf` | Active or inactive | If inactive, skip UFW checks |
| UFW FORWARD | Check `ufw route` rules for incusbr0 | Rules exist | Print exact `ufw route allow` commands |
| NAT masquerade | Check iptables/nftables for MASQUERADE on bridge subnet | Rule exists | "Incus should set this up — try: incus network edit incusbr0" |
| Internet test | Launch throwaway container, exec `ping -c1 1.1.1.1` | Pong | "Container can't reach internet — check firewall rules above" |

### `myringa doctor --fix`

Applies recommended fixes with explicit confirmation:

```
myringa doctor --fix

The following changes will be made:
  1. ufw route allow in on incusbr0
  2. ufw route allow out on incusbr0

These rules allow containers on the Incus bridge to forward
traffic through the host. This does NOT expose container ports
to your network — it only enables outbound NAT.

Apply? [y/N]
```

Requires sudo. If the user isn't root, print the commands and tell them to run with sudo.

### Implementation

- `internal/doctor/` package — checks return a structured result (pass/fail/fix command)
- Each check is a function: `CheckDaemon()`, `CheckBridge()`, `CheckUFW()`, `CheckNAT()`, `CheckInternet()` (plus storage checks from Part 1)
- CLI: `myringa doctor [--fix]`, `myringa setup storage`
- ~200-300 lines total (storage + network diagnostics combined)

---

## Part 2: Port Forwarding

Thin CLI wrapper around Incus proxy devices. Reduces 14 words to 3.

### CLI

```
myringa expose <instance> <port>              # host:port → container:port (same port)
myringa expose <instance> <host-port>:<container-port>   # different ports
myringa unexpose <instance> <host-port>       # remove forwarding
myringa ports <instance>                      # list forwarded ports
```

### What it does under the hood

```go
// myringa expose mydev 8080
conn.UpdateInstance("mydev", addDevice("proxy-8080", map[string]string{
    "type":    "proxy",
    "listen":  "tcp:0.0.0.0:8080",
    "connect": "tcp:127.0.0.1:8080",
}))

// myringa unexpose mydev 8080
conn.UpdateInstance("mydev", removeDevice("proxy-8080"))

// myringa ports mydev
// → reads instance devices, filters type=proxy, formats output
```

### Edge cases

- Port already in use on host → Incus returns an error, surface it clearly: "Port 8080 is already in use on the host"
- Instance not running → proxy devices can be added but won't work until started. Warn but allow.
- Expose on 0.0.0.0 vs 127.0.0.1 → default to 0.0.0.0 (accessible from host network). Consider a `--localhost` flag for host-only binding.

### Implementation

- Extend `internal/incus/client.go` with device add/remove helpers
- `internal/ports/` package or fold into `internal/provision/` — TBD based on size
- ~80-100 lines for CLI, ~50 lines for the Incus device wrappers

---

## Part 3: TUI Network Visibility

### Main table: PORTS column

Add a `PORTS` column to the instance table:

```
NAME        TYPE  STATUS   CPU    MEMORY          DISK       IPv4            PORTS
mydev       CT    Running  12%    256 MiB / 4 GiB  2.1 GiB   10.104.73.42   8080,3000
worker-1    CT    Running  3%     128 MiB / 4 GiB  1.2 GiB   10.104.73.43   —
```

Implementation: scan instance devices for `type=proxy`, extract listen port, comma-join. Show `—` if none.

### Detail view: port management

When pressing `Enter` on an instance (Plan 2's detail view), show ports and allow add/remove:

```
Ports:
  8080 → 8080  (0.0.0.0)
  3000 → 3000  (0.0.0.0)

[a]dd port  [d]elete port  [b]ack
```

`a` → text input for port (e.g., `8080` or `8080:3000`) → adds proxy device
`d` on selected port → removes proxy device

---

## Migration Path

### Phase 1: Host diagnostics (`myringa doctor`)
1. Implement check functions in `internal/doctor/` — storage checks (pool exists, backend type, free space) and network checks (daemon, bridge, UFW, NAT, internet)
2. Wire up `myringa doctor` CLI with structured output (pass/warn/fail per check)
3. Wire up `myringa doctor --fix` for network fixes (UFW rules)
4. Test: run on host with dir backend → warns about performance
5. Test: run on host with UFW blocking → shows fix commands
6. Test: `--fix` applies UFW rules, containers can reach internet

### Phase 2: Storage setup (`myringa setup storage`)
1. Implement guided storage pool creation in `internal/doctor/`
2. Detect available backends (dir always, btrfs if available, zfs if kernel module loaded)
3. For zfs-disk: enumerate available disks/partitions
4. Wire up `myringa setup storage` CLI with interactive prompts
5. Test: create zfs-loopback pool, verify Incus uses it

### Phase 3: Port forwarding CLI
1. Add device add/remove to `internal/incus/client.go`
2. Implement `myringa expose`, `myringa unexpose`, `myringa ports`
3. Test: expose a port, curl from host to container service, verify connectivity
4. Test: unexpose, verify port no longer reachable

### Phase 4: TUI integration
1. Add PORTS column to main table (read proxy devices from instance state)
2. Add port management to detail view (Plan 2 dependency)
3. Test: expose via CLI, verify TUI shows it; add via TUI, verify `myringa ports` shows it

---

## Dependencies

- **Plan 2** (lifecycle TUI): Detail view must exist before TUI port management can be added
- **Plan 3** (provisioning): Independent — networking works on any container, not just myringa-provisioned ones
- **`myringa doctor`**: Should ideally run before first `myringa launch` to catch host config issues early. Consider having `myringa launch` run a quick network check and warn if UFW is blocking.
