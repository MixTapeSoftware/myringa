package ui

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lxc/incus/v6/shared/api"

	"myringa/internal/incus"
)

// --- Mock Client ---

type mockClient struct {
	instances  []incus.InstanceRow
	snapshots  map[string]incus.CPUSnapshot
	fetchErr   error
	actionErr  error
	snaps      []incus.SnapshotInfo
	snapsErr   error
	calls      []string // records method calls
}

func (m *mockClient) record(name string) { m.calls = append(m.calls, name) }

func (m *mockClient) FetchInstances(_ context.Context, prev map[string]incus.CPUSnapshot) ([]incus.InstanceRow, map[string]incus.CPUSnapshot, error) {
	m.record("FetchInstances")
	if m.fetchErr != nil {
		return nil, nil, m.fetchErr
	}
	snaps := m.snapshots
	if snaps == nil {
		snaps = make(map[string]incus.CPUSnapshot)
	}
	return m.instances, snaps, nil
}

func (m *mockClient) StartInstance(_ context.Context, name string) error {
	m.record("StartInstance:" + name)
	return m.actionErr
}

func (m *mockClient) StopInstance(_ context.Context, name string) error {
	m.record("StopInstance:" + name)
	return m.actionErr
}

func (m *mockClient) RestartInstance(_ context.Context, name string) error {
	m.record("RestartInstance:" + name)
	return m.actionErr
}

func (m *mockClient) DeleteInstance(_ context.Context, name string) error {
	m.record("DeleteInstance:" + name)
	return m.actionErr
}

func (m *mockClient) CreateSnapshot(_ context.Context, inst, snap string) error {
	m.record("CreateSnapshot:" + inst + "/" + snap)
	return m.actionErr
}

func (m *mockClient) RestoreSnapshot(_ context.Context, inst, snap string) error {
	m.record("RestoreSnapshot:" + inst + "/" + snap)
	return m.actionErr
}

func (m *mockClient) DeleteSnapshot(_ context.Context, inst, snap string) error {
	m.record("DeleteSnapshot:" + inst + "/" + snap)
	return m.actionErr
}

func (m *mockClient) ListSnapshots(_ context.Context, inst string) ([]incus.SnapshotInfo, error) {
	m.record("ListSnapshots:" + inst)
	return m.snaps, m.snapsErr
}

func (m *mockClient) ListImages(_ context.Context) ([]incus.ImageInfo, error) {
	m.record("ListImages")
	return nil, nil
}

func (m *mockClient) ListProfiles(_ context.Context) ([]incus.ProfileInfo, error) {
	m.record("ListProfiles")
	return nil, nil
}

func (m *mockClient) CreateInstance(_ context.Context, name, image, profile string) error {
	m.record("CreateInstance:" + name)
	return m.actionErr
}

func (m *mockClient) ProfileExists(_ context.Context, name string) (bool, error) {
	return false, nil
}

func (m *mockClient) ImageAliasExists(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func (m *mockClient) DeleteImageAlias(_ context.Context, _ string) error {
	return nil
}

func (m *mockClient) LaunchBuilder(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (m *mockClient) ExecStream(_ context.Context, _ string, _ []string, _, _ io.Writer) error {
	return nil
}

func (m *mockClient) PublishInstance(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockClient) CreateProfile(_ context.Context, name string, _ string) error {
	return nil
}

func (m *mockClient) CreateInstanceFull(_ context.Context, _ api.InstancesPost) error {
	return nil
}

func (m *mockClient) UpdateInstanceConfig(_ context.Context, _ string, _ map[string]string) error {
	return nil
}

func (m *mockClient) AddDevice(_ context.Context, _, _ string, _ map[string]string) error {
	return nil
}

func (m *mockClient) ExecInstance(_ context.Context, _ string, _ []string) ([]byte, error) {
	return nil, nil
}

// --- Test Helpers ---

func testModel(mc *mockClient) Model {
	m := NewModel()
	m.client = mc
	m.loading = false
	m.width = 120
	m.height = 40
	m.table.SetColumns(m.tableColumns())
	m.table.SetHeight(30)
	return m
}

func testModelWithRows(mc *mockClient, rows []incus.InstanceRow) Model {
	m := testModel(mc)
	m.rows = rows
	m.table.SetRows(toTableRows(rows))
	return m
}

func key(k string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func ctrlKey(k string) tea.KeyMsg {
	switch k {
	case "c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

// Resolve a tea.Cmd and return the resulting tea.Msg
func resolveCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// --- Behavior Tests: Polling & Connection ---

func TestConnectDoneMsg_Success(t *testing.T) {
	mc := &mockClient{
		instances: []incus.InstanceRow{{Name: "test-1", Status: "Running"}},
	}
	m := NewModel()
	m.width = 120
	m.height = 40
	m.table.SetColumns(m.tableColumns())
	m.table.SetHeight(30)

	// Simulate successful connection
	result, cmd := m.Update(connectDoneMsg{client: mc, err: nil})
	m = result.(Model)

	if m.client == nil {
		t.Fatal("client should be set after connectDoneMsg")
	}
	if m.err != nil {
		t.Errorf("err should be nil, got %v", m.err)
	}

	// The returned cmd should be a fetchCmd — resolve it
	msg := resolveCmd(cmd)
	if msg == nil {
		t.Fatal("expected fetchCmd to return a msg")
	}
	if _, ok := msg.(fetchDoneMsg); !ok {
		t.Fatalf("expected fetchDoneMsg, got %T", msg)
	}
}

func TestConnectDoneMsg_Error(t *testing.T) {
	m := NewModel()

	result, cmd := m.Update(connectDoneMsg{err: errors.New("socket not found")})
	m = result.(Model)

	if m.err == nil {
		t.Fatal("err should be set on connect error")
	}
	if m.loading {
		t.Error("loading should be false after connect error")
	}
	if m.errCountdown != 3 {
		t.Errorf("errCountdown = %d, want 3", m.errCountdown)
	}

	// Should return tickCmd for retry
	if cmd == nil {
		t.Fatal("expected tickCmd for retry")
	}
}

func TestFetchDoneMsg_Success_UpdatesRows(t *testing.T) {
	mc := &mockClient{}
	m := testModel(mc)

	rows := []incus.InstanceRow{
		{Name: "alpha", Status: "Running", CPU: "5.0%", Memory: "128M", Disk: "1G", IPv4: "10.0.0.1"},
		{Name: "bravo", Status: "Stopped", CPU: "—", Memory: "—", Disk: "—", IPv4: "—"},
	}
	snaps := map[string]incus.CPUSnapshot{
		"alpha": {UsageNS: 100, Timestamp: time.Now()},
	}

	result, _ := m.Update(fetchDoneMsg{rows: rows, snapshots: snaps, gen: m.fetchGen})
	m = result.(Model)

	if len(m.rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(m.rows))
	}
	if m.rows[0].Name != "alpha" {
		t.Errorf("first row = %q, want alpha", m.rows[0].Name)
	}
	if m.loading {
		t.Error("loading should be false")
	}
	if m.lastUpdated.IsZero() {
		t.Error("lastUpdated should be set")
	}
}

func TestFetchDoneMsg_Error_NilsClient(t *testing.T) {
	mc := &mockClient{}
	m := testModel(mc)

	result, _ := m.Update(fetchDoneMsg{err: errors.New("network error"), gen: m.fetchGen})
	m = result.(Model)

	if m.client != nil {
		t.Error("client should be nil after fetch error")
	}
	if m.err == nil {
		t.Error("err should be set")
	}
	if m.errCountdown != 3 {
		t.Errorf("errCountdown = %d, want 3", m.errCountdown)
	}
}

func TestFetchDoneMsg_StaleGeneration_NoTick(t *testing.T) {
	mc := &mockClient{}
	m := testModel(mc)
	m.fetchGen = 5

	// fetchDoneMsg with old generation
	result, cmd := m.Update(fetchDoneMsg{
		rows: []incus.InstanceRow{{Name: "test"}},
		gen:  3, // stale
	})
	m = result.(Model)

	// Rows should still update (data is valid)
	if len(m.rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(m.rows))
	}

	// But should NOT chain to next tick
	if cmd != nil {
		t.Error("stale generation should not chain tickCmd")
	}
}

func TestFetchDoneMsg_CurrentGeneration_ChainsTick(t *testing.T) {
	mc := &mockClient{}
	m := testModel(mc)
	m.fetchGen = 5

	result, cmd := m.Update(fetchDoneMsg{
		rows: []incus.InstanceRow{{Name: "test"}},
		gen:  5, // current
	})
	_ = result.(Model)

	if cmd == nil {
		t.Fatal("current generation should chain tickCmd")
	}
}

// --- Flash Error Auto-Clear ---

func TestFlashError_ClearsAfterSuccessfulFetches(t *testing.T) {
	mc := &mockClient{}
	m := testModel(mc)
	m.err = errors.New("previous error")
	m.errCountdown = 2 // 2 successful fetches to clear

	// First successful fetch — countdown decrements
	result, _ := m.Update(fetchDoneMsg{rows: []incus.InstanceRow{}, gen: m.fetchGen})
	m = result.(Model)
	if m.err == nil {
		t.Fatal("err should persist after first fetch (countdown=1)")
	}
	if m.errCountdown != 1 {
		t.Errorf("errCountdown = %d, want 1", m.errCountdown)
	}

	// Second successful fetch — error cleared
	result, _ = m.Update(fetchDoneMsg{rows: []incus.InstanceRow{}, gen: m.fetchGen})
	m = result.(Model)
	if m.err != nil {
		t.Errorf("err should be cleared after countdown, got %v", m.err)
	}
}

// --- Generation Counter: Prevents Duplicate Ticker Chains ---

func TestActionDoneMsg_BumpsGeneration(t *testing.T) {
	mc := &mockClient{
		instances: []incus.InstanceRow{{Name: "test", Status: "Running"}},
	}
	m := testModel(mc)
	initialGen := m.fetchGen

	result, cmd := m.Update(actionDoneMsg{action: "restart", name: "test"})
	m = result.(Model)

	if m.fetchGen != initialGen+1 {
		t.Errorf("fetchGen = %d, want %d", m.fetchGen, initialGen+1)
	}
	if m.actionPending != "" {
		t.Errorf("actionPending should be cleared, got %q", m.actionPending)
	}
	if cmd == nil {
		t.Fatal("expected fetchCmd after action")
	}
}

func TestActionDoneMsg_Error_SetsErr(t *testing.T) {
	mc := &mockClient{}
	m := testModel(mc)

	result, _ := m.Update(actionDoneMsg{action: "start", name: "foo", err: errors.New("failed")})
	m = result.(Model)

	if m.err == nil {
		t.Fatal("err should be set on action error")
	}
	if m.errCountdown != 3 {
		t.Errorf("errCountdown = %d, want 3", m.errCountdown)
	}
}

// --- Key Handlers: Table View ---

func TestKeyS_StopRunningInstance(t *testing.T) {
	mc := &mockClient{}
	rows := []incus.InstanceRow{{Name: "web-1", Status: "Running"}}
	m := testModelWithRows(mc, rows)

	m, cmd := m.updateTableKeys(key("s"))

	if m.actionPending != "Stopping web-1…" {
		t.Errorf("actionPending = %q", m.actionPending)
	}

	// Resolve the command — should call StopInstance
	msg := resolveCmd(cmd)
	if msg == nil {
		t.Fatal("expected action cmd")
	}
	if _, ok := msg.(actionDoneMsg); !ok {
		t.Fatalf("expected actionDoneMsg, got %T", msg)
	}
	if len(mc.calls) == 0 || mc.calls[0] != "StopInstance:web-1" {
		t.Errorf("calls = %v, want [StopInstance:web-1]", mc.calls)
	}
}

func TestKeyS_StartStoppedInstance(t *testing.T) {
	mc := &mockClient{}
	rows := []incus.InstanceRow{{Name: "db-1", Status: "Stopped"}}
	m := testModelWithRows(mc, rows)

	m, cmd := m.updateTableKeys(key("s"))

	if m.actionPending != "Starting db-1…" {
		t.Errorf("actionPending = %q", m.actionPending)
	}

	msg := resolveCmd(cmd)
	if _, ok := msg.(actionDoneMsg); !ok {
		t.Fatalf("expected actionDoneMsg, got %T", msg)
	}
	if mc.calls[0] != "StartInstance:db-1" {
		t.Errorf("calls = %v", mc.calls)
	}
}

func TestKeyR_RestartRunningInstance(t *testing.T) {
	mc := &mockClient{}
	rows := []incus.InstanceRow{{Name: "app-1", Status: "Running"}}
	m := testModelWithRows(mc, rows)

	m, cmd := m.updateTableKeys(key("r"))

	if m.actionPending != "Restarting app-1…" {
		t.Errorf("actionPending = %q", m.actionPending)
	}

	msg := resolveCmd(cmd)
	if _, ok := msg.(actionDoneMsg); !ok {
		t.Fatalf("expected actionDoneMsg, got %T", msg)
	}
	if mc.calls[0] != "RestartInstance:app-1" {
		t.Errorf("calls = %v", mc.calls)
	}
}

func TestKeyR_IgnoresStoppedInstance(t *testing.T) {
	mc := &mockClient{}
	rows := []incus.InstanceRow{{Name: "db-1", Status: "Stopped"}}
	m := testModelWithRows(mc, rows)

	_, cmd := m.updateTableKeys(key("r"))

	if cmd != nil {
		t.Error("restart on stopped instance should do nothing")
	}
}

func TestKeyS_NoClient(t *testing.T) {
	m := NewModel()
	m.width = 120
	m.height = 40
	m.table.SetColumns(m.tableColumns())
	m.table.SetHeight(30)
	m.rows = []incus.InstanceRow{{Name: "test", Status: "Running"}}
	m.table.SetRows(toTableRows(m.rows))
	// client is nil

	_, cmd := m.updateTableKeys(key("s"))
	if cmd != nil {
		t.Error("should do nothing when client is nil")
	}
}

// --- Overlay Tests: Confirm ---

func TestConfirmOverlay_Accept(t *testing.T) {
	mc := &mockClient{}
	rows := []incus.InstanceRow{{Name: "doomed", Status: "Stopped"}}
	m := testModelWithRows(mc, rows)

	// Press x to trigger delete confirm
	m, _ = m.updateTableKeys(key("x"))
	if !m.confirmActive {
		t.Fatal("confirmActive should be true after x")
	}
	if m.confirmAction != "delete-instance" {
		t.Errorf("confirmAction = %q", m.confirmAction)
	}
	if m.confirmName != "doomed" {
		t.Errorf("confirmName = %q", m.confirmName)
	}

	// Press y to confirm
	m, cmd := m.updateConfirm(key("y"))
	if m.confirmActive {
		t.Error("confirmActive should be cleared after y")
	}
	if m.actionPending != "Deleting doomed…" {
		t.Errorf("actionPending = %q", m.actionPending)
	}

	// Resolve the delete command
	msg := resolveCmd(cmd)
	if _, ok := msg.(actionDoneMsg); !ok {
		t.Fatalf("expected actionDoneMsg, got %T", msg)
	}
	if mc.calls[0] != "DeleteInstance:doomed" {
		t.Errorf("calls = %v", mc.calls)
	}
}

func TestConfirmOverlay_Cancel_N(t *testing.T) {
	mc := &mockClient{}
	m := testModelWithRows(mc, []incus.InstanceRow{{Name: "safe", Status: "Running"}})
	m.confirmActive = true
	m.confirmAction = "delete-instance"
	m.confirmName = "safe"

	m, cmd := m.updateConfirm(key("n"))
	if m.confirmActive {
		t.Error("confirmActive should be cleared")
	}
	if m.confirmAction != "" {
		t.Errorf("confirmAction should be empty, got %q", m.confirmAction)
	}
	if cmd != nil {
		t.Error("cancel should not dispatch a command")
	}
}

func TestConfirmOverlay_Cancel_Esc(t *testing.T) {
	mc := &mockClient{}
	m := testModelWithRows(mc, []incus.InstanceRow{{Name: "safe", Status: "Running"}})
	m.confirmActive = true
	m.confirmAction = "delete-instance"

	m, _ = m.updateConfirm(tea.KeyMsg{Type: tea.KeyEscape})
	if m.confirmActive {
		t.Error("confirmActive should be cleared on esc")
	}
}

// --- Overlay Tests: Snapshot Prompt ---

func TestSnapshotPrompt_Submit(t *testing.T) {
	mc := &mockClient{}
	m := testModelWithRows(mc, []incus.InstanceRow{{Name: "web-1", Status: "Running"}})
	m.snapshotPromptActive = true
	m.confirmName = "web-1"
	m.snapshotInput.SetValue("my-snapshot")

	m, cmd := m.updateSnapshotPrompt(tea.KeyMsg{Type: tea.KeyEnter})
	if m.snapshotPromptActive {
		t.Error("prompt should be deactivated")
	}

	msg := resolveCmd(cmd)
	if _, ok := msg.(snapshotActionMsg); !ok {
		t.Fatalf("expected snapshotActionMsg, got %T", msg)
	}
	if mc.calls[0] != "CreateSnapshot:web-1/my-snapshot" {
		t.Errorf("calls = %v", mc.calls)
	}
}

func TestSnapshotPrompt_Cancel(t *testing.T) {
	mc := &mockClient{}
	m := testModelWithRows(mc, []incus.InstanceRow{{Name: "web-1"}})
	m.snapshotPromptActive = true

	m, cmd := m.updateSnapshotPrompt(tea.KeyMsg{Type: tea.KeyEscape})
	if m.snapshotPromptActive {
		t.Error("prompt should be deactivated on esc")
	}
	if cmd != nil {
		t.Error("cancel should not dispatch a command")
	}
}

func TestSnapshotPrompt_EmptyName(t *testing.T) {
	mc := &mockClient{}
	m := testModelWithRows(mc, []incus.InstanceRow{{Name: "web-1"}})
	m.snapshotPromptActive = true
	m.confirmName = "web-1"
	m.snapshotInput.SetValue("")

	m, cmd := m.updateSnapshotPrompt(tea.KeyMsg{Type: tea.KeyEnter})
	if m.snapshotPromptActive {
		t.Error("prompt should be deactivated")
	}
	if cmd != nil {
		t.Error("empty name should not dispatch a command")
	}
}

// --- Cursor Preservation ---

func TestCursorPreservation_AcrossRefresh(t *testing.T) {
	mc := &mockClient{}
	rows := []incus.InstanceRow{
		{Name: "alpha", Status: "Running"},
		{Name: "bravo", Status: "Running"},
		{Name: "charlie", Status: "Stopped"},
	}
	m := testModelWithRows(mc, rows)
	m.table.SetCursor(1) // select "bravo"

	// Simulate a fetch that returns the same rows (sorted)
	newRows := []incus.InstanceRow{
		{Name: "alpha", Status: "Running"},
		{Name: "bravo", Status: "Running"},
		{Name: "charlie", Status: "Stopped"},
	}

	result, _ := m.Update(fetchDoneMsg{rows: newRows, gen: m.fetchGen})
	m = result.(Model)

	if m.table.Cursor() != 1 {
		t.Errorf("cursor = %d, want 1 (bravo)", m.table.Cursor())
	}
}

func TestCursorPreservation_AfterRowRemoved(t *testing.T) {
	mc := &mockClient{}
	rows := []incus.InstanceRow{
		{Name: "alpha", Status: "Running"},
		{Name: "bravo", Status: "Running"},
		{Name: "charlie", Status: "Stopped"},
	}
	m := testModelWithRows(mc, rows)
	m.table.SetCursor(2) // select "charlie"

	// charlie gets deleted
	newRows := []incus.InstanceRow{
		{Name: "alpha", Status: "Running"},
		{Name: "bravo", Status: "Running"},
	}

	result, _ := m.Update(fetchDoneMsg{rows: newRows, gen: m.fetchGen})
	m = result.(Model)

	// charlie gone — cursor should stay within bounds (table handles clamping)
	cursor := m.table.Cursor()
	if cursor >= len(m.rows) {
		t.Errorf("cursor = %d, out of bounds (len=%d)", cursor, len(m.rows))
	}
}

// --- Tick → Reconnect When Client is Nil ---

func TestTickMsg_ReconnectsWhenClientNil(t *testing.T) {
	m := NewModel()
	m.client = nil

	result, cmd := m.Update(tickMsg(time.Now()))
	_ = result.(Model)

	// Should return connectCmd
	if cmd == nil {
		t.Fatal("expected connectCmd when client is nil")
	}
	msg := resolveCmd(cmd)
	if _, ok := msg.(connectDoneMsg); !ok {
		t.Fatalf("expected connectDoneMsg, got %T", msg)
	}
}

// --- Global Quit ---

func TestCtrlC_Quits(t *testing.T) {
	m := NewModel()

	result, cmd := m.Update(ctrlKey("c"))
	_ = result.(Model)

	// tea.Quit returns a special msg
	if cmd == nil {
		t.Fatal("expected quit command")
	}
}

// --- Delete Running Instance: Stop then Delete ---

func TestDeleteRunningInstance_StopsThenDeletes(t *testing.T) {
	mc := &mockClient{}
	rows := []incus.InstanceRow{{Name: "live-one", Status: "Running"}}
	m := testModelWithRows(mc, rows)

	// Trigger delete confirm
	m, _ = m.updateTableKeys(key("x"))
	m, cmd := m.updateConfirm(key("y"))

	// Resolve
	resolveCmd(cmd)
	if len(mc.calls) < 2 {
		t.Fatalf("expected 2 calls, got %v", mc.calls)
	}
	if mc.calls[0] != "StopInstance:live-one" {
		t.Errorf("first call = %q, want StopInstance:live-one", mc.calls[0])
	}
	if mc.calls[1] != "DeleteInstance:live-one" {
		t.Errorf("second call = %q, want DeleteInstance:live-one", mc.calls[1])
	}
}
