package visualizer

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/somatic-labs/meteorite/lib/chainregistry"
	"github.com/somatic-labs/meteorite/lib/snapshotter"
)

// StartSnapshotUI starts the snapshot UI in a new window
func (v *Visualizer) StartSnapshotUI(registry *chainregistry.Registry, snapshotter *snapshotter.Snapshotter) error {
	ui := NewSnapshotUI(registry, snapshotter)
	p := tea.NewProgram(ui)
	ui.SetProgram(p)

	go func() {
		if err := p.Start(); err != nil {
			v.AddDebugLog(fmt.Sprintf("Error starting snapshot UI: %v", err))
		}
	}()

	return nil
}
