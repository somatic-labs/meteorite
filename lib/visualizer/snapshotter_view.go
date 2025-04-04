package visualizer

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/somatic-labs/meteorite/lib/snapshotter"
)

// SnapshotterView manages the snapshot progress visualization
type SnapshotterView struct {
	snapshotter *snapshotter.Snapshotter
	progress    map[string]*SnapshotProgress
	progressMu  sync.RWMutex
	lastRefresh time.Time
	refreshRate time.Duration
}

// NewSnapshotterView creates a new snapshotter view
func NewSnapshotterView(s *snapshotter.Snapshotter) *SnapshotterView {
	return &SnapshotterView{
		snapshotter: s,
		progress:    make(map[string]*SnapshotProgress),
		refreshRate: time.Second,
	}
}

// UpdateProgress updates the progress for a chain
func (v *SnapshotterView) UpdateProgress(chainID string, state snapshotter.SnapshotState, err error) {
	v.progressMu.Lock()
	defer v.progressMu.Unlock()

	prog, exists := v.progress[chainID]
	if !exists {
		prog = &SnapshotProgress{
			ChainID:   chainID,
			StartTime: time.Now(),
		}
		v.progress[chainID] = prog
	}

	prog.BlockHeight = state.BlockHeight
	prog.AccountsComplete = state.AccountsComplete
	prog.BalancesComplete = state.BalancesComplete
	prog.StakingComplete = state.StakingComplete
	prog.ValidatorsComplete = state.ValidatorsComplete
	prog.LastUpdateTime = time.Now()
	prog.Error = err
}

// Render renders the current snapshot progress
func (v *SnapshotterView) Render() string {
	v.progressMu.RLock()
	defer v.progressMu.RUnlock()

	if len(v.progress) == 0 {
		return "No snapshots in progress"
	}

	var sb strings.Builder

	// Title
	greenBold := color.New(color.FgGreen, color.Bold).SprintFunc()
	sb.WriteString(greenBold("=== CHAIN SNAPSHOTS ===\n"))

	// Progress bars
	for chainID, prog := range v.progress {
		// Calculate overall progress
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
		progress := float64(completed) / 4.0 * 100

		// Format duration
		duration := time.Since(prog.StartTime).Round(time.Second)

		// Status color
		statusColor := color.New(color.FgYellow)
		if progress == 100 {
			statusColor = color.New(color.FgGreen)
		} else if prog.Error != nil {
			statusColor = color.New(color.FgRed)
		}

		// Chain header
		sb.WriteString(fmt.Sprintf("\n%s [Height: %d] [%s]\n",
			color.New(color.FgCyan).Sprint(chainID),
			prog.BlockHeight,
			duration))

		// Progress bar
		width := 40
		filled := int(float64(width) * progress / 100)
		bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
		sb.WriteString(fmt.Sprintf("%s %s\n",
			statusColor.Sprint(bar),
			statusColor.Sprintf("%.1f%%", progress)))

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
			status := "⋯"
			if comp.complete {
				status = "✓"
			}
			sb.WriteString(fmt.Sprintf("  %s %s\n",
				status,
				comp.name))
		}

		// Error if any
		if prog.Error != nil {
			sb.WriteString(color.New(color.FgRed).Sprintf("  Error: %v\n", prog.Error))
		}
	}

	return sb.String()
}

// Clear removes completed snapshots from the view
func (v *SnapshotterView) Clear() {
	v.progressMu.Lock()
	defer v.progressMu.Unlock()

	for chainID, prog := range v.progress {
		if prog.AccountsComplete && prog.BalancesComplete && prog.StakingComplete && prog.ValidatorsComplete {
			delete(v.progress, chainID)
		}
	}
}
