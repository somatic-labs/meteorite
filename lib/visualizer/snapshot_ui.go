package visualizer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/somatic-labs/meteorite/lib/chainregistry"
	"github.com/somatic-labs/meteorite/lib/snapshotter"
)

var (
	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00FF00")).
			Bold(true).
			MarginLeft(2)

	chainStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00FFFF")).
			Bold(true)

	progressBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFF00"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF0000"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00FF00"))
)

// SnapshotUI is the main UI model
type SnapshotUI struct {
	registry    *chainregistry.Registry
	snapshotter *snapshotter.Snapshotter
	viewport    viewport.Model
	spinner     spinner.Model
	progress    map[string]*SnapshotProgress
	ready       bool
	err         error
	height      int
	width       int
	program     *tea.Program
}

// NewSnapshotUI creates a new snapshot UI
func NewSnapshotUI(registry *chainregistry.Registry, snapshotter *snapshotter.Snapshotter) *SnapshotUI {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return &SnapshotUI{
		registry:    registry,
		snapshotter: snapshotter,
		spinner:     s,
		progress:    make(map[string]*SnapshotProgress),
	}
}

// SetProgram sets the tea.Program instance
func (m *SnapshotUI) SetProgram(p *tea.Program) {
	m.program = p
}

// Init initializes the UI
func (m *SnapshotUI) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.startSnapshots,
	)
}

// startSnapshots starts the snapshot process for all chains
func (m *SnapshotUI) startSnapshots() tea.Msg {
	chains, err := m.registry.GetAllChains()
	if err != nil {
		return errMsg{err}
	}

	// Initialize progress for all chains
	for _, chain := range chains {
		m.progress[chain.ChainID] = &SnapshotProgress{
			ChainID:   chain.ChainID,
			StartTime: time.Now(),
		}
	}

	// Start snapshots in background
	go func() {
		if err := m.snapshotter.SnapshotAllChains(context.Background()); err != nil {
			m.err = err
		}
	}()

	return nil
}

// Update handles UI updates
func (m *SnapshotUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if k := msg.String(); k == "ctrl+c" || k == "q" {
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		if !m.ready {
			m.viewport = viewport.New(msg.Width-4, msg.Height-4)
			m.viewport.Style = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("62")).
				PaddingRight(2)
			m.ready = true
		} else {
			m.viewport.Width = msg.Width - 4
			m.viewport.Height = msg.Height - 4
		}
		m.height = msg.Height
		m.width = msg.Width

	case errMsg:
		m.err = msg.error
		return m, nil

	case progressMsg:
		if prog, exists := m.progress[msg.chainID]; exists {
			prog.update(msg.state, msg.err)
		}
	}

	m.viewport.SetContent(m.renderContent())
	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)

	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	if vpCmd != nil {
		cmds = append(cmds, vpCmd)
	}

	return m, tea.Batch(cmds...)
}

// View renders the UI
func (m *SnapshotUI) View() string {
	if !m.ready {
		return "\n  Initializing..."
	}

	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v\n", m.err)
	}

	return fmt.Sprintf("\n%s\n\n%s",
		titleStyle.Render("Chain Snapshots"),
		m.viewport.View())
}

// renderContent generates the content for the viewport
func (m *SnapshotUI) renderContent() string {
	var sb strings.Builder

	for chainID, prog := range m.progress {
		// Chain header
		sb.WriteString(chainStyle.Render(fmt.Sprintf("Chain: %s\n", chainID)))

		// Block height
		if prog.BlockHeight > 0 {
			sb.WriteString(fmt.Sprintf("Block Height: %d\n", prog.BlockHeight))
		} else {
			sb.WriteString(fmt.Sprintf("Block Height: %s\n", m.spinner.View()))
		}

		// Progress bar
		width := m.width - 10
		completed := 0
		if prog.AccountsComplete {
			completed++
		}
		if prog.BalancesComplete {
			completed++
		}
		if prog.StakingComplete {
			completed++
		}
		if prog.ValidatorsComplete {
			completed++
		}
		progress := float64(completed) / 4.0
		filled := int(float64(width) * progress)
		bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)

		style := progressBarStyle
		if completed == 4 {
			style = successStyle
		} else if prog.Error != nil {
			style = errorStyle
		}

		sb.WriteString(style.Render(fmt.Sprintf("[%s] %.1f%%\n", bar, progress*100)))

		// Component status
		components := []struct {
			name     string
			complete bool
		}{
			{"Accounts", prog.AccountsComplete},
			{"Balances", prog.BalancesComplete},
			{"Staking", prog.StakingComplete},
			{"Validators", prog.ValidatorsComplete},
		}

		for _, comp := range components {
			status := m.spinner.View()
			if comp.complete {
				status = "✓"
			}
			sb.WriteString(fmt.Sprintf("  %s %s\n", status, comp.name))
		}

		// Error if any
		if prog.Error != nil {
			sb.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v\n", prog.Error)))
		}

		// Duration
		duration := time.Since(prog.StartTime).Round(time.Second)
		sb.WriteString(fmt.Sprintf("Duration: %s\n", duration))

		sb.WriteString("\n")
	}

	return sb.String()
}

// Message types
type errMsg struct {
	error
}

type progressMsg struct {
	chainID string
	state   snapshotter.SnapshotState
	err     error
}

// progressCallback converts snapshot progress updates to tea.Msg
func (m *SnapshotUI) progressCallback(chainID string, state snapshotter.SnapshotState, err error) {
	// Send progress update through tea.Msg
	go func() {
		m.program.Send(progressMsg{
			chainID: chainID,
			state:   state,
			err:     err,
		})
	}()
}
