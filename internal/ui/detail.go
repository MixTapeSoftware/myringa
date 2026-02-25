package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"myringa/internal/incus"
)

// updateDetail handles key events in the detail view.
func (m Model) updateDetail(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.view = viewTable
		return m, nil

	case "c":
		// Create snapshot — enter snapshot name prompt
		m.snapshotPromptActive = true
		m.snapshotInput.SetValue(m.defaultSnapshotName())
		cmd := m.snapshotInput.Focus()
		return m, cmd

	case "r":
		// Restore selected snapshot
		snap, ok := m.selectedSnapshot()
		if !ok {
			return m, nil
		}
		m.confirmActive = true
		m.confirmAction = "restore-snapshot"
		m.confirmName = snap.Name
		m.confirmMsg = fmt.Sprintf("Restore '%s' to snapshot '%s'?", m.detailName, snap.Name)
		return m, nil

	case "d":
		// Delete selected snapshot
		snap, ok := m.selectedSnapshot()
		if !ok {
			return m, nil
		}
		m.confirmActive = true
		m.confirmAction = "delete-snapshot"
		m.confirmName = snap.Name
		m.confirmMsg = fmt.Sprintf("Delete snapshot '%s'?", snap.Name)
		return m, nil

	default:
		var cmd tea.Cmd
		m.snapshotTable, cmd = m.snapshotTable.Update(msg)
		return m, cmd
	}
}

// viewDetail renders the instance detail screen.
func (m Model) viewDetail() string {
	var s string

	// Title
	row, _ := m.findRow(m.detailName)
	title := TitleStyle.Render("myringa")
	title += DimStyle.Render(fmt.Sprintf("  %s  %s", row.Name, row.Type))
	s += title + "\n"

	// Error banner
	if m.err != nil {
		s += ErrorStyle.Render(fmt.Sprintf("  error: %v", m.err)) + "\n"
	}

	// Instance info
	s += "\n"
	s += fmt.Sprintf("  %s %s", StatusDot(row.Status), LabelStyle.Render(row.Status))
	if row.IPv4 != "—" {
		s += DimStyle.Render("  ") + ValueStyle.Render(row.IPv4)
	}
	s += "\n"

	info := fmt.Sprintf("  CPU %s  Mem %s  Disk %s", row.CPU, row.Memory, row.Disk)
	s += DimStyle.Render(info) + "\n"
	s += "\n"

	// Snapshots header
	snapCount := len(m.detailSnapshots)
	s += LabelStyle.Render(fmt.Sprintf("  Snapshots (%d)", snapCount)) + "\n"

	if m.detailLoading {
		s += m.spinner.View() + " Loading snapshots...\n"
	} else if snapCount == 0 {
		s += DimStyle.Render("  No snapshots") + "\n"
	} else {
		s += m.snapshotTable.View() + "\n"
	}

	// Footer
	if m.confirmActive {
		s += PromptStyle.Render(fmt.Sprintf("  %s (y/n)", m.confirmMsg))
	} else if m.snapshotPromptActive {
		s += PromptStyle.Render("  Snapshot name: ") + m.snapshotInput.View()
	} else {
		s += HelpStyle.Render("  c snapshot  r restore  d delete  Esc back")
	}

	return s
}

// selectedSnapshot returns the snapshot at the cursor position.
func (m Model) selectedSnapshot() (incus.SnapshotInfo, bool) {
	idx := m.snapshotTable.Cursor()
	if idx < 0 || idx >= len(m.detailSnapshots) {
		return incus.SnapshotInfo{}, false
	}
	return m.detailSnapshots[idx], true
}

// snapshotTableColumns returns column definitions for the snapshot table.
func (m Model) snapshotTableColumns() []table.Column {
	nameW := 30
	createdW := 20
	statefulW := 10
	sizeW := m.width - nameW - createdW - statefulW
	if sizeW < 8 {
		sizeW = 8
	}
	return []table.Column{
		{Title: "NAME", Width: nameW},
		{Title: "CREATED", Width: createdW},
		{Title: "STATEFUL", Width: statefulW},
		{Title: "SIZE", Width: sizeW},
	}
}

// toSnapshotRows converts SnapshotInfo to table rows.
func toSnapshotRows(snaps []incus.SnapshotInfo) []table.Row {
	rows := make([]table.Row, len(snaps))
	for i, s := range snaps {
		stateful := "no"
		if s.Stateful {
			stateful = "yes"
		}
		size := "—"
		if s.Size > 0 {
			size = formatSize(s.Size)
		}
		rows[i] = table.Row{
			s.Name,
			s.CreatedAt.Format("2006-01-02 15:04:05"),
			stateful,
			size,
		}
	}
	return rows
}

// formatSize formats bytes to human-readable.
func formatSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// fetchSnapshotsCmd fetches snapshots for an instance.
func fetchSnapshotsCmd(conn incusClient, name string) tea.Cmd {
	return func() tea.Msg {
		snaps, err := incus.ListSnapshots(conn, name)
		return snapshotsMsg{snapshots: snaps, err: err}
	}
}
