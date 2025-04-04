package snapshotter

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/somatic-labs/meteorite/lib/chainregistry"
)

// ProgressCallback is called when snapshot progress is updated
type ProgressCallback func(chainID string, state SnapshotState, err error)

// SnapshotState tracks the progress of a chain snapshot
type SnapshotState struct {
	ChainID            string            `json:"chain_id"`
	BlockHeight        int64             `json:"block_height"`
	AccountsComplete   bool              `json:"accounts_complete"`
	BalancesComplete   bool              `json:"balances_complete"`
	StakingComplete    bool              `json:"staking_complete"`
	ValidatorsComplete bool              `json:"validators_complete"`
	InProgressFiles    map[string]string `json:"in_progress_files"`
}

// Balance represents an account's token balance
type Balance struct {
	Address string    `json:"address"`
	Coins   sdk.Coins `json:"coins"`
}

// ChainSnapshot represents a complete snapshot of a chain
type ChainSnapshot struct {
	State      SnapshotState              `json:"state"`
	Accounts   []sdk.AccountI             `json:"accounts"`
	Balances   []Balance                  `json:"balances"`
	Validators []stakingtypes.Validator   `json:"validators"`
	Staking    []stakingtypes.DelegationI `json:"staking"`
	Params     map[string]interface{}     `json:"params"`
}

// Snapshotter manages taking snapshots of multiple chains
type Snapshotter struct {
	registry   *chainregistry.Registry
	logger     *log.Logger
	baseDir    string
	mu         sync.RWMutex
	onProgress ProgressCallback
}

// NewSnapshotter creates a new snapshotter instance
func NewSnapshotter(registry *chainregistry.Registry, baseDir string) *Snapshotter {
	return &Snapshotter{
		registry: registry,
		logger:   log.New(os.Stdout, "[SNAPSHOTTER] ", log.Ldate|log.Ltime|log.Lmicroseconds),
		baseDir:  baseDir,
	}
}

// SetProgressCallback sets the callback for progress updates
func (s *Snapshotter) SetProgressCallback(cb ProgressCallback) {
	s.onProgress = cb
}

// updateProgress updates the snapshot progress and calls the progress callback if set
func (s *Snapshotter) updateProgress(chainID string, state SnapshotState, err error) {
	if s.onProgress != nil {
		s.onProgress(chainID, state, err)
	}
}

// SnapshotAllChains takes snapshots of all chains in parallel
func (s *Snapshotter) SnapshotAllChains(ctx context.Context) error {
	chains, err := s.registry.GetAllChains()
	if err != nil {
		return fmt.Errorf("failed to get chains: %w", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(chains))

	for _, chain := range chains {
		wg.Add(1)
		go func(c chainregistry.Chain) {
			defer wg.Done()
			if err := s.SnapshotChain(ctx, c); err != nil {
				s.logger.Printf("Error snapshotting chain %s: %v", c.ChainID, err)
				errCh <- fmt.Errorf("chain %s: %w", c.ChainID, err)
				s.updateProgress(c.ChainID, SnapshotState{ChainID: c.ChainID}, err)
			}
		}(chain)
	}

	// Wait for all snapshots to complete
	wg.Wait()
	close(errCh)

	// Collect any errors
	var errors []error
	for err := range errCh {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to snapshot %d chains", len(errors))
	}

	return nil
}

// SnapshotChain takes a snapshot of a single chain
func (s *Snapshotter) SnapshotChain(ctx context.Context, chain chainregistry.Chain) error {
	s.logger.Printf("Starting snapshot of chain %s", chain.ChainID)

	// Create chain snapshot directory
	snapshotDir := filepath.Join(s.baseDir, chain.ChainID)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	// Load or initialize state
	state, err := s.loadState(snapshotDir, chain.ChainID)
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	// Get latest block height
	height, err := s.getLatestBlockHeight(chain)
	if err != nil {
		return fmt.Errorf("failed to get block height: %w", err)
	}
	state.BlockHeight = height

	// Update initial progress
	s.updateProgress(chain.ChainID, *state, nil)

	// Take snapshot components in parallel
	var wg sync.WaitGroup
	errCh := make(chan error, 4)

	// Snapshot accounts
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.snapshotAccounts(ctx, chain, state); err != nil {
			errCh <- fmt.Errorf("accounts: %w", err)
		}
		state.AccountsComplete = err == nil
		s.updateProgress(chain.ChainID, *state, err)
	}()

	// Snapshot balances
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.snapshotBalances(ctx, chain, state); err != nil {
			errCh <- fmt.Errorf("balances: %w", err)
		}
		state.BalancesComplete = err == nil
		s.updateProgress(chain.ChainID, *state, err)
	}()

	// Snapshot validators
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.snapshotValidators(ctx, chain, state); err != nil {
			errCh <- fmt.Errorf("validators: %w", err)
		}
		state.ValidatorsComplete = err == nil
		s.updateProgress(chain.ChainID, *state, err)
	}()

	// Snapshot staking
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.snapshotStaking(ctx, chain, state); err != nil {
			errCh <- fmt.Errorf("staking: %w", err)
		}
		state.StakingComplete = err == nil
		s.updateProgress(chain.ChainID, *state, err)
	}()

	// Wait for all components to complete
	wg.Wait()
	close(errCh)

	// Check for errors
	var errors []error
	for err := range errCh {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		err := fmt.Errorf("snapshot errors: %v", errors)
		s.updateProgress(chain.ChainID, *state, err)
		return err
	}

	// Save final state
	if err := s.saveState(state, true, snapshotDir); err != nil {
		return fmt.Errorf("failed to save final state: %w", err)
	}

	s.logger.Printf("Completed snapshot of chain %s at height %d", chain.ChainID, height)
	s.updateProgress(chain.ChainID, *state, nil)
	return nil
}

// loadState loads or initializes snapshot state
func (s *Snapshotter) loadState(snapshotDir, chainID string) (*SnapshotState, error) {
	stateFile := filepath.Join(snapshotDir, "state.json")

	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		return &SnapshotState{
			ChainID:         chainID,
			InProgressFiles: make(map[string]string),
		}, nil
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state SnapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}

	return &state, nil
}

// saveState saves snapshot state
func (s *Snapshotter) saveState(state *SnapshotState, final bool, snapshotDir string) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	filename := "state.json.tmp"
	if final {
		filename = "state.json"
	}

	if err := os.WriteFile(filepath.Join(snapshotDir, filename), data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// getLatestBlockHeight gets the latest block height from a chain
func (s *Snapshotter) getLatestBlockHeight(chain chainregistry.Chain) (int64, error) {
	// TODO: Implement using chain's RPC endpoint
	return 0, nil
}

// snapshotAccounts takes a snapshot of all accounts
func (s *Snapshotter) snapshotAccounts(ctx context.Context, chain chainregistry.Chain, state *SnapshotState) error {
	// TODO: Implement account snapshot logic
	return nil
}

// snapshotBalances takes a snapshot of all account balances
func (s *Snapshotter) snapshotBalances(ctx context.Context, chain chainregistry.Chain, state *SnapshotState) error {
	// TODO: Implement balance snapshot logic
	return nil
}

// snapshotValidators takes a snapshot of all validators
func (s *Snapshotter) snapshotValidators(ctx context.Context, chain chainregistry.Chain, state *SnapshotState) error {
	// TODO: Implement validator snapshot logic
	return nil
}

// snapshotStaking takes a snapshot of all staking data
func (s *Snapshotter) snapshotStaking(ctx context.Context, chain chainregistry.Chain, state *SnapshotState) error {
	// TODO: Implement staking snapshot logic
	return nil
}
