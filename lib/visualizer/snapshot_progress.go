package visualizer

import (
	"time"

	"github.com/somatic-labs/meteorite/lib/snapshotter"
)

// SnapshotProgress tracks the progress of a chain snapshot
type SnapshotProgress struct {
	ChainID            string
	BlockHeight        int64
	AccountsComplete   bool
	BalancesComplete   bool
	StakingComplete    bool
	ValidatorsComplete bool
	StartTime          time.Time
	LastUpdateTime     time.Time
	Error              error
}

// update updates the progress state
func (p *SnapshotProgress) update(state snapshotter.SnapshotState, err error) {
	p.BlockHeight = state.BlockHeight
	p.AccountsComplete = state.AccountsComplete
	p.BalancesComplete = state.BalancesComplete
	p.StakingComplete = state.StakingComplete
	p.ValidatorsComplete = state.ValidatorsComplete
	p.LastUpdateTime = time.Now()
	p.Error = err
}
