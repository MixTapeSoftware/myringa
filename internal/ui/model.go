package ui

import (
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	incusclient "github.com/lxc/incus/v6/client"

	"myringa/internal/incus"
)

// shellCmd wraps exec.Cmd to satisfy tea.ExecCommand.
type shellCmd struct {
	cmd *exec.Cmd
}

func (s *shellCmd) Run() error              { return s.cmd.Run() }
func (s *shellCmd) SetStdin(r io.Reader)    { s.cmd.Stdin = r }
func (s *shellCmd) SetStdout(w io.Writer)   { s.cmd.Stdout = w }
func (s *shellCmd) SetStderr(w io.Writer)   { s.cmd.Stderr = w }

// incusClient is a type alias for readability.
type incusClient = incusclient.InstanceServer

// View modes
type viewMode int

const (
	viewTable  viewMode = iota // main instance table
	viewDetail                 // instance detail with snapshots
)

// Messages

type tickMsg time.Time

type connectDoneMsg struct {
	conn incusClient
	err  error
}

type fetchDoneMsg struct {
	rows      []incus.InstanceRow
	snapshots map[string]incus.CPUSnapshot
	err       error
	gen       int // ticker generation — only the active generation chains the next tick
}

type actionDoneMsg struct {
	action string
	name   string
	err    error
}

type execDoneMsg struct {
	err error
}

type snapshotsMsg struct {
	snapshots []incus.SnapshotInfo
	err       error
}

type snapshotActionMsg struct {
	action string
	err    error
}

// Model is the BubbleTea model for the TUI.
type Model struct {
	conn         incusClient
	rows         []incus.InstanceRow
	cpuSnapshots map[string]incus.CPUSnapshot
	lastUpdated  time.Time
	table        table.Model
	spinner      spinner.Model
	loading      bool
	err          error
	width        int
	height       int

	// View state
	view viewMode

	// Confirm overlay (works on any view)
	confirmActive bool
	confirmMsg    string
	confirmAction string // "delete-instance", "restore-snapshot", "delete-snapshot"
	confirmName   string // target name (instance or snapshot)

	// Snapshot name prompt overlay
	snapshotPromptActive bool
	snapshotInput        textinput.Model

	// Action feedback
	actionPending string // e.g. "Restarting foo…" while action in-flight
	fetchGen      int    // generation counter — prevents duplicate ticker chains

	// Detail view state
	detailName      string
	detailSnapshots []incus.SnapshotInfo
	detailLoading   bool
	snapshotTable   table.Model
}

// NewModel creates the initial model.
func NewModel() Model {
	s := spinner.New()
	s.Spinner = spinner.Dot

	t := table.New(
		table.WithFocused(true),
	)
	ts := table.DefaultStyles()
	ts.Header = HeaderStyle
	ts.Selected = SelectedStyle
	t.SetStyles(ts)

	si := textinput.New()
	si.Placeholder = "snapshot-name"
	si.CharLimit = 64

	return Model{
		cpuSnapshots:  make(map[string]incus.CPUSnapshot),
		table:         t,
		spinner:       s,
		loading:       true,
		snapshotInput: si,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, connectCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	// -- Global messages (handled regardless of view) --

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.table.SetColumns(m.tableColumns())
		m.table.SetHeight(m.height - m.headerHeight())
		if m.view == viewDetail {
			m.snapshotTable.SetColumns(m.snapshotTableColumns())
			m.snapshotTable.SetHeight(m.height - m.detailHeaderHeight())
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case connectDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.loading = false
			return m, tickCmd()
		}
		m.conn = msg.conn
		m.err = nil
		prev := copySnapshots(m.cpuSnapshots)
		return m, fetchCmd(m.conn, prev, m.fetchGen)

	case fetchDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.conn = nil
			if msg.gen == m.fetchGen {
				return m, tickCmd()
			}
			return m, nil
		}
		m.rows = msg.rows
		m.cpuSnapshots = msg.snapshots
		m.lastUpdated = time.Now()
		m.loading = false
		m.err = nil
		m.table.SetRows(toTableRows(m.rows))
		// Only the active generation continues the ticker chain
		if msg.gen == m.fetchGen {
			return m, tickCmd()
		}
		return m, nil

	case tickMsg:
		if m.conn == nil {
			return m, connectCmd()
		}
		prev := copySnapshots(m.cpuSnapshots)
		return m, fetchCmd(m.conn, prev, m.fetchGen)

	case actionDoneMsg:
		m.actionPending = ""
		if msg.err != nil {
			m.err = fmt.Errorf("%s %s: %w", msg.action, msg.name, msg.err)
		} else {
			m.err = nil
		}
		// Immediate refresh — bump generation to kill the old ticker chain
		if m.conn != nil {
			m.fetchGen++
			prev := copySnapshots(m.cpuSnapshots)
			return m, fetchCmd(m.conn, prev, m.fetchGen)
		}
		return m, nil

	case execDoneMsg:
		if msg.err != nil {
			m.err = fmt.Errorf("exec: %w", msg.err)
		}
		return m, nil

	case snapshotsMsg:
		m.detailLoading = false
		if msg.err != nil {
			m.err = fmt.Errorf("snapshots: %w", msg.err)
			return m, nil
		}
		m.err = nil
		m.detailSnapshots = msg.snapshots
		m.snapshotTable.SetRows(toSnapshotRows(msg.snapshots))
		return m, nil

	case snapshotActionMsg:
		if msg.err != nil {
			m.err = fmt.Errorf("snapshot %s: %w", msg.action, msg.err)
		} else {
			m.err = nil
		}
		// Refresh snapshot list
		if m.view == viewDetail && m.conn != nil {
			return m, fetchSnapshotsCmd(m.conn, m.detailName)
		}
		return m, nil

	// -- Key messages: route to view --

	case tea.KeyMsg:
		// Global quit
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

		// Confirm overlay takes priority
		if m.confirmActive {
			return m.updateConfirm(msg)
		}

		// Snapshot name prompt takes priority
		if m.snapshotPromptActive {
			return m.updateSnapshotPrompt(msg)
		}

		// Route to view
		switch m.view {
		case viewTable:
			return m.updateTableKeys(msg)
		case viewDetail:
			return m.updateDetail(msg)
		}
	}

	return m, nil
}

func (m Model) View() string {
	switch m.view {
	case viewDetail:
		return m.viewDetail()
	default:
		return m.viewTableScreen()
	}
}

// viewTableScreen renders the main table view with any active overlays.
func (m Model) viewTableScreen() string {
	var s string

	// Title bar
	title := TitleStyle.Render("myringa")
	var meta string
	if len(m.rows) > 0 {
		meta += DimStyle.Render(fmt.Sprintf("  %d instances", len(m.rows)))
	}
	if !m.lastUpdated.IsZero() {
		meta += DimStyle.Render(fmt.Sprintf("  %s", m.lastUpdated.Format("15:04:05")))
	}
	s += title + meta + "\n"

	// Error banner
	if m.err != nil {
		s += ErrorStyle.Render(fmt.Sprintf("  error: %v", m.err)) + "\n"
	}

	// Action in progress
	if m.actionPending != "" {
		s += "  " + m.spinner.View() + " " + DimStyle.Render(m.actionPending) + "\n"
	}

	// Main content
	if m.loading && len(m.rows) == 0 {
		s += m.spinner.View() + " Connecting to Incus...\n"
	} else {
		s += m.table.View() + "\n"
	}

	// Footer — changes based on overlay state
	if m.confirmActive {
		s += PromptStyle.Render(fmt.Sprintf("  %s (y/n)", m.confirmMsg))
	} else if m.snapshotPromptActive {
		s += PromptStyle.Render("  Snapshot name: ") + m.snapshotInput.View()
	} else {
		s += HelpStyle.Render("  s start/stop  r restart  e enter  d details  x delete  S snapshot  q quit")
	}

	return s
}

// updateTableKeys handles key events in the table view.
func (m Model) updateTableKeys(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit

	case "s":
		row, ok := m.selectedRow()
		if !ok || m.conn == nil {
			return m, nil
		}
		if row.Status == "Running" {
			m.actionPending = fmt.Sprintf("Stopping %s…", row.Name)
			return m, stopInstanceCmd(m.conn, row.Name)
		}
		if row.Status == "Stopped" {
			m.actionPending = fmt.Sprintf("Starting %s…", row.Name)
			return m, startInstanceCmd(m.conn, row.Name)
		}
		return m, nil

	case "r":
		row, ok := m.selectedRow()
		if !ok || m.conn == nil || row.Status != "Running" {
			return m, nil
		}
		m.actionPending = fmt.Sprintf("Restarting %s…", row.Name)
		return m, restartInstanceCmd(m.conn, row.Name)

	case "e":
		row, ok := m.selectedRow()
		if !ok {
			return m, nil
		}
		return m, execCmd(row.Name)

	case "d", "enter":
		row, ok := m.selectedRow()
		if !ok || m.conn == nil || row.Status != "Running" {
			return m, nil
		}
		return m.enterDetail(row.Name)

	case "x":
		row, ok := m.selectedRow()
		if !ok {
			return m, nil
		}
		m.confirmActive = true
		m.confirmAction = "delete-instance"
		m.confirmName = row.Name
		m.confirmMsg = fmt.Sprintf("Delete '%s'?", row.Name)
		return m, nil

	case "S":
		row, ok := m.selectedRow()
		if !ok {
			return m, nil
		}
		m.snapshotPromptActive = true
		m.snapshotInput.SetValue(m.defaultSnapshotName())
		m.confirmName = row.Name // reuse for target instance
		cmd := m.snapshotInput.Focus()
		return m, cmd

	default:
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
}

// updateConfirm handles key events for the confirmation overlay.
func (m Model) updateConfirm(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		action := m.confirmAction
		name := m.confirmName
		m.clearConfirm()

		if m.conn == nil {
			return m, nil
		}

		switch action {
		case "delete-instance":
			m.actionPending = fmt.Sprintf("Deleting %s…", name)
			row, _ := m.findRow(name)
			return m, deleteInstanceCmd(m.conn, name, row.Status == "Running")
		case "restore-snapshot":
			return m, restoreSnapshotCmd(m.conn, m.detailName, name)
		case "delete-snapshot":
			return m, deleteSnapshotCmd(m.conn, m.detailName, name)
		}
		return m, nil

	case "n", "N", "esc":
		m.clearConfirm()
		return m, nil
	}

	return m, nil
}

// updateSnapshotPrompt handles key events for the snapshot name input.
func (m Model) updateSnapshotPrompt(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		name := m.snapshotInput.Value()
		target := m.confirmName // instance name stored here
		if m.view == viewDetail {
			target = m.detailName
		}
		m.clearSnapshotPrompt()

		if name == "" || m.conn == nil {
			return m, nil
		}
		return m, createSnapshotCmd(m.conn, target, name)

	case "esc":
		m.clearSnapshotPrompt()
		return m, nil

	default:
		var cmd tea.Cmd
		m.snapshotInput, cmd = m.snapshotInput.Update(msg)
		return m, cmd
	}
}

// enterDetail switches to the detail view for the given instance.
func (m Model) enterDetail(name string) (Model, tea.Cmd) {
	m.view = viewDetail
	m.detailName = name
	m.detailSnapshots = nil
	m.detailLoading = true

	// Initialize snapshot table
	st := table.New(table.WithFocused(true))
	sts := table.DefaultStyles()
	sts.Header = HeaderStyle
	sts.Selected = SelectedStyle
	st.SetStyles(sts)
	st.SetColumns(m.snapshotTableColumns())
	st.SetHeight(m.height - m.detailHeaderHeight())
	m.snapshotTable = st

	return m, fetchSnapshotsCmd(m.conn, name)
}

// Helpers

func (m *Model) clearConfirm() {
	m.confirmActive = false
	m.confirmMsg = ""
	m.confirmAction = ""
	m.confirmName = ""
}

func (m *Model) clearSnapshotPrompt() {
	m.snapshotPromptActive = false
	m.snapshotInput.Blur()
	m.snapshotInput.SetValue("")
}

func (m Model) selectedRow() (incus.InstanceRow, bool) {
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.rows) {
		return incus.InstanceRow{}, false
	}
	return m.rows[idx], true
}

func (m Model) findRow(name string) (incus.InstanceRow, bool) {
	for _, r := range m.rows {
		if r.Name == name {
			return r, true
		}
	}
	return incus.InstanceRow{}, false
}

func (m Model) defaultSnapshotName() string {
	return time.Now().Format("snap-20060102-150405")
}

func (m Model) headerHeight() int {
	h := 3 // title + table header + footer
	if m.err != nil {
		h++
	}
	if m.actionPending != "" {
		h++
	}
	return h
}

func (m Model) detailHeaderHeight() int {
	// title + error? + blank + status + metrics + blank + snapshots label + footer
	h := 8
	if m.err != nil {
		h++
	}
	return h
}

func copySnapshots(m map[string]incus.CPUSnapshot) map[string]incus.CPUSnapshot {
	c := make(map[string]incus.CPUSnapshot, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (m Model) tableColumns() []table.Column {
	const (
		nameW = 32
		cpuW  = 8
		memW  = 12
		diskW = 8
	)
	ipWidth := m.width - (nameW + cpuW + memW + diskW)
	if ipWidth < 16 {
		ipWidth = 16
	}

	return []table.Column{
		{Title: "NAME", Width: nameW},
		{Title: "CPU", Width: cpuW},
		{Title: "MEM", Width: memW},
		{Title: "DISK", Width: diskW},
		{Title: "IPv4", Width: ipWidth},
	}
}

func toTableRows(rows []incus.InstanceRow) []table.Row {
	trs := make([]table.Row, len(rows))
	for i, r := range rows {
		name := fmt.Sprintf("%s %s  %s", statusRune(r.Status), r.Name, r.Type)
		trs[i] = table.Row{name, r.CPU, r.Memory, r.Disk, r.IPv4}
	}
	return trs
}

func statusRune(status string) string {
	switch status {
	case "Running":
		return "●"
	case "Stopped":
		return "○"
	case "Frozen":
		return "◎"
	default:
		return "·"
	}
}

// Commands

func connectCmd() tea.Cmd {
	return func() tea.Msg {
		conn, err := incus.Connect()
		return connectDoneMsg{conn: conn, err: err}
	}
}

func fetchCmd(conn incusClient, prev map[string]incus.CPUSnapshot, gen int) tea.Cmd {
	return func() tea.Msg {
		rows, snaps, err := incus.FetchInstances(conn, prev)
		return fetchDoneMsg{rows: rows, snapshots: snaps, err: err, gen: gen}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func startInstanceCmd(conn incusClient, name string) tea.Cmd {
	return func() tea.Msg {
		err := incus.StartInstance(conn, name)
		return actionDoneMsg{action: "start", name: name, err: err}
	}
}

func stopInstanceCmd(conn incusClient, name string) tea.Cmd {
	return func() tea.Msg {
		err := incus.StopInstance(conn, name)
		return actionDoneMsg{action: "stop", name: name, err: err}
	}
}

func restartInstanceCmd(conn incusClient, name string) tea.Cmd {
	return func() tea.Msg {
		err := incus.RestartInstance(conn, name)
		return actionDoneMsg{action: "restart", name: name, err: err}
	}
}

func deleteInstanceCmd(conn incusClient, name string, isRunning bool) tea.Cmd {
	return func() tea.Msg {
		if isRunning {
			if err := incus.StopInstance(conn, name); err != nil {
				return actionDoneMsg{action: "delete", name: name, err: fmt.Errorf("stop before delete: %w", err)}
			}
		}
		err := incus.DeleteInstance(conn, name)
		return actionDoneMsg{action: "delete", name: name, err: err}
	}
}

func execCmd(name string) tea.Cmd {
	c := &shellCmd{cmd: exec.Command("incus", "exec", name, "--", "bash")}
	return tea.Exec(c, func(err error) tea.Msg {
		return execDoneMsg{err: err}
	})
}

func createSnapshotCmd(conn incusClient, instanceName, snapshotName string) tea.Cmd {
	return func() tea.Msg {
		err := incus.CreateSnapshot(conn, instanceName, snapshotName)
		return snapshotActionMsg{action: "create", err: err}
	}
}

func restoreSnapshotCmd(conn incusClient, instanceName, snapshotName string) tea.Cmd {
	return func() tea.Msg {
		err := incus.RestoreSnapshot(conn, instanceName, snapshotName)
		return snapshotActionMsg{action: "restore", err: err}
	}
}

func deleteSnapshotCmd(conn incusClient, instanceName, snapshotName string) tea.Cmd {
	return func() tea.Msg {
		err := incus.DeleteSnapshot(conn, instanceName, snapshotName)
		return snapshotActionMsg{action: "delete", err: err}
	}
}
