# Plan: myringa — Incus TUI Dashboard

## Context
A read-only terminal dashboard that shows all Incus containers and VMs with live stats. Polls every 2 seconds. Built with BubbleTea in Go. `/workspace` is empty — greenfield.

---

## Project Structure

```
/workspace/
├── go.mod
├── main.go
└── internal/
    ├── incus/
    │   └── client.go   # Incus API + data types (no BubbleTea)
    └── ui/
        ├── model.go    # BubbleTea model + all TUI logic
        └── styles.go   # Lipgloss vars
```

Module: `myringa`

---

## Dependencies

```
github.com/charmbracelet/bubbletea
github.com/charmbracelet/bubbles
github.com/charmbracelet/lipgloss
github.com/lxc/incus/v6
```

---

## Step 1: Initialize

```bash
go mod init myringa
go get github.com/charmbracelet/bubbletea \
       github.com/charmbracelet/bubbles \
       github.com/charmbracelet/lipgloss \
       github.com/lxc/incus/v6
```

---

## Step 2: `internal/incus/client.go`

All Incus API calls. No BubbleTea.

```go
type InstanceRow struct {
    Name, Type, Status, CPU, Memory, Disk, IPv4 string
}

type CPUSnapshot struct {
    UsageNS   int64
    Timestamp time.Time
}

func Connect() (client.InstanceServer, error)
func FetchInstances(c client.InstanceServer, prev map[string]CPUSnapshot) ([]InstanceRow, map[string]CPUSnapshot, error)
```

**`FetchInstances`** calls `c.GetInstancesFull(api.InstanceTypeAny)` — one round-trip for everything. Iterates results, computes CPU delta, formats fields.

**CPU%:** `float64(curNS-prevNS) / (elapsedSec * 1e9) * 100`. Shows "—" on first poll, stopped instances, and negative deltas (restart between polls). Can exceed 100% on multi-core; that's correct — it matches `incus top` behavior.

**Nil guard:** if `inst.State == nil`, all metric fields are "—". This is normal for stopped instances.

**Unexported helpers:**
- `formatBytes(int64) string` — KiB/MiB/GiB
- `formatMemory(used, total int64) string` — "256 MiB / 1 GiB"
- `primaryIPv4(map[string]api.InstanceStateNetwork) string` — first non-loopback inet addr
- `rootDiskUsage(map[string]api.InstanceStateDisk) string` — "root" key, then sum, then "—"
- `instanceType(api.InstanceType) string` — "CT" or "VM"

---

## Step 3: `internal/ui/styles.go`

Just vars:

```go
var (
    HeaderStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
    SelectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("57")).Foreground(lipgloss.Color("255"))
    RunningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
    StoppedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
    HelpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
    ErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
)

func StatusStyle(status string) lipgloss.Style  // returns the right style per status
```

---

## Step 4: `internal/ui/model.go`

### Model

```go
type Model struct {
    conn         client.InstanceServer
    rows         []incus.InstanceRow
    cpuSnapshots map[string]incus.CPUSnapshot
    lastUpdated  time.Time
    table        table.Model
    spinner      spinner.Model
    loading      bool   // true until first successful fetch
    err          error  // last error (nil = healthy); shown as banner, doesn't blank the table
    width, height int
}
```

**One error field.** Not `connErr` + `fetchErr`. If there's an error, it's shown as a banner. Stale data stays visible (better than a blank screen). Connection is re-attempted on every tick when `conn == nil`.

### Messages

```go
type tickMsg       time.Time
type connectDoneMsg struct { conn client.InstanceServer; err error }
type fetchDoneMsg  struct { rows []incus.InstanceRow; snapshots map[string]incus.CPUSnapshot; err error }
```

### Polling loop

```
Init() → tea.Batch(spinner.Tick, connectCmd())
connectDoneMsg ok  → store conn, return fetchCmd()
connectDoneMsg err → set err, loading=false, return tickCmd()  ← retry in 2s
fetchDoneMsg ok    → update rows/snapshots, loading=false, err=nil, return tickCmd()
fetchDoneMsg err   → set err, nil conn (force reconnect), return tickCmd()
tickMsg            → conn==nil → connectCmd(); else → fetchCmd()
```

Tick fires only after fetch completes — no concurrent API calls, no goroutine pile-up.

### Commands

```go
func connectCmd() tea.Cmd   // wraps incus.Connect()
func fetchCmd(conn client.InstanceServer, prev map[string]incus.CPUSnapshot) tea.Cmd  // shallow-copies prev before goroutine
func tickCmd() tea.Cmd      // tea.Tick(2*time.Second, ...)
```

### Update() — key cases

```go
case tea.KeyMsg:
    switch msg.String() {
    case "q", "ctrl+c":  return m, tea.Quit
    case "r":            return m, connectCmd() or fetchCmd()
    default:             m.table, cmd = m.table.Update(msg); return m, cmd
    }
case tea.WindowSizeMsg:  store size, m.table.SetColumns(tableColumns(width)), m.table.SetHeight(height-4)
case spinner.TickMsg:    m.spinner, cmd = m.spinner.Update(msg); return m, cmd
// ... connect/fetch/tick cases
default:                 m.table, cmd = m.table.Update(msg); return m, cmd  // table scroll
```

`j`/`k`, arrows, `pgup`/`pgdn` all handled by table for free.

### View()

```
[Title: "myringa"]
[ErrorStyle banner if err != nil]
[Spinner "Connecting..." if loading, else table.View()]
["Last updated: HH:MM:SS"  |  "j/k scroll  r refresh  q quit"]
```

When `loading=true` and no connection yet, banner says "Connecting to Incus...". The spinner and connecting message are the same state.

### Table columns

```
NAME (fills remaining) | TYPE (4) | STATUS (9) | CPU (7) | MEMORY (20) | DISK (10) | IPv4 (15)
```

NAME gets: `termWidth - 75 - separators`. Minimum 10. `table.DefaultStyles()` with `HeaderStyle` and `SelectedStyle` overrides.

Status cells embed `StatusStyle(r.Status).Render(r.Status)` — Lipgloss ANSI sequences in cell values. The `bubbles/table` package handles this correctly (uses `lipgloss.Width()` for measurement).

---

## Step 5: `main.go`

```go
func main() {
    p := tea.NewProgram(ui.NewModel(), tea.WithAltScreen())
    if _, err := p.Run(); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}
```

---

## Build & Run

```bash
go mod tidy
go build -o myringa .
./myringa
```

Needs access to Incus Unix socket. Run as user in `incus-admin` group or with `sudo`.

---

## Verification

1. `./myringa` — spinner briefly, then table of instances
2. Start/stop an instance — table updates within ~2s
3. CPU shows "—" on first poll, values on second
4. Press `r` — immediate refresh
5. Press `q` — clean exit, terminal restored
6. Kill Incus daemon — error banner + stale table; restart daemon, press `r` — reconnects
7. Narrow terminal — NAME column shrinks, nothing panics
