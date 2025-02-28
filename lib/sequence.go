package lib

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/somatic-labs/meteorite/types"
)

// SequenceManager provides centralized management of account sequences
// to avoid sequence mismatch errors when broadcasting multiple transactions
// from the same account concurrently.
type SequenceManager struct {
	// Map of address -> node -> current sequence
	sequences     map[string]map[string]uint64
	mutex         sync.RWMutex
	refreshTimes  map[string]map[string]time.Time
	refreshWindow time.Duration // How often to force refresh from chain
}

// Global sequence manager instance
var (
	globalSequenceManager *SequenceManager
	once                  sync.Once
)

// GetSequenceManager returns the global sequence manager instance
func GetSequenceManager() *SequenceManager {
	once.Do(func() {
		globalSequenceManager = &SequenceManager{
			sequences:     make(map[string]map[string]uint64),
			refreshTimes:  make(map[string]map[string]time.Time),
			refreshWindow: 30 * time.Second, // Refresh from chain every 30 seconds
		}
	})
	return globalSequenceManager
}

// createAddressEntryIfNotExists ensures that the address has an entry in the maps
func (sm *SequenceManager) createAddressEntryIfNotExists(address string) {
	if _, exists := sm.sequences[address]; !exists {
		sm.sequences[address] = make(map[string]uint64)
		sm.refreshTimes[address] = make(map[string]time.Time)
	}
}

// GetSequence gets the current sequence for an address and node, refreshing from chain if needed
func (sm *SequenceManager) GetSequence(address, nodeURL string, config types.Config, forceRefresh bool) (uint64, error) {
	sm.mutex.RLock()
	// Check if we have an entry for this address
	addressMap, addressExists := sm.sequences[address]

	// Initialize sequence and lastRefresh
	var seq uint64
	var exists bool
	var lastRefresh time.Time

	if addressExists {
		seq, exists = addressMap[nodeURL]
		if timeMap, ok := sm.refreshTimes[address]; ok {
			lastRefresh = timeMap[nodeURL]
		}
	}
	sm.mutex.RUnlock()

	// Determine if we need to refresh from chain
	needsRefresh := forceRefresh || !addressExists || !exists || time.Since(lastRefresh) > sm.refreshWindow

	if needsRefresh {
		// Get the latest sequence from the chain using this specific node
		nodeConfig := config
		nodeConfig.Nodes.RPC = []string{nodeURL} // Override the node URL to use the specific node

		sequence, _, err := GetAccountInfo(address, nodeConfig)
		if err != nil {
			return 0, fmt.Errorf("failed to get sequence for %s from node %s: %w", address, nodeURL, err)
		}

		// Update our locally tracked sequence
		sm.mutex.Lock()
		// Ensure address entry exists
		sm.createAddressEntryIfNotExists(address)

		sm.sequences[address][nodeURL] = sequence
		sm.refreshTimes[address][nodeURL] = time.Now()
		sm.mutex.Unlock()

		return sequence, nil
	}

	return seq, nil
}

// IncrementSequence increments the sequence for an address after successful transaction
func (sm *SequenceManager) IncrementSequence(address, nodeURL string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// Ensure address entry exists
	sm.createAddressEntryIfNotExists(address)

	if seq, exists := sm.sequences[address][nodeURL]; exists {
		sm.sequences[address][nodeURL] = seq + 1
	}
}

// SetSequence explicitly sets a sequence for an address and node
func (sm *SequenceManager) SetSequence(address, nodeURL string, sequence uint64) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// Ensure address entry exists
	sm.createAddressEntryIfNotExists(address)

	sm.sequences[address][nodeURL] = sequence
	sm.refreshTimes[address][nodeURL] = time.Now()
}

// UpdateFromError updates the sequence based on a sequence mismatch error
// It parses the error message to extract the expected sequence
func (sm *SequenceManager) UpdateFromError(address, nodeURL, errMsg string) (uint64, bool) {
	expectedSeq, err := ExtractExpectedSequence(errMsg)
	if err != nil {
		return 0, false
	}

	sm.SetSequence(address, nodeURL, expectedSeq)
	return expectedSeq, true
}

// BatchReservation reserves a batch of sequential sequence numbers for an address and node
// This is useful when you know you'll be sending multiple sequential transactions
func (sm *SequenceManager) BatchReservation(address, nodeURL string, count int, config types.Config) (uint64, error) {
	// Get latest sequence from chain to ensure accuracy
	// Use the specific node for this request
	nodeConfig := config
	nodeConfig.Nodes.RPC = []string{nodeURL} // Override the node URL

	sequence, _, err := GetAccountInfo(address, nodeConfig)
	if err != nil {
		return 0, fmt.Errorf("failed to get sequence for batch reservation from node %s: %w", nodeURL, err)
	}

	// Reserve the batch by updating our tracked sequence
	sm.mutex.Lock()
	// Ensure address entry exists
	sm.createAddressEntryIfNotExists(address)

	startSeq := sequence
	sm.sequences[address][nodeURL] = sequence + uint64(count)
	sm.refreshTimes[address][nodeURL] = time.Now()
	sm.mutex.Unlock()

	return startSeq, nil
}

// PrefetchAllSequences preloads sequences for a list of accounts from all nodes
func (sm *SequenceManager) PrefetchAllSequences(ctx context.Context, accounts []types.Account, config types.Config) error {
	// Use a wait group to fetch all sequences concurrently
	var wg sync.WaitGroup
	var errMutex sync.Mutex
	var firstErr error

	// For each account, fetch sequences from all RPC nodes
	for _, account := range accounts {
		for _, nodeURL := range config.Nodes.RPC {
			wg.Add(1)
			go func(addr, node string) {
				defer wg.Done()

				// Check for context cancellation
				select {
				case <-ctx.Done():
					errMutex.Lock()
					if firstErr == nil {
						firstErr = ctx.Err()
					}
					errMutex.Unlock()
					return
				default:
					// Continue processing
				}

				// Use a node-specific config
				nodeConfig := config
				nodeConfig.Nodes.RPC = []string{node}

				seq, _, err := GetAccountInfo(addr, nodeConfig)
				if err != nil {
					errMutex.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("failed to prefetch sequence for %s from node %s: %w", addr, node, err)
					}
					errMutex.Unlock()
					return
				}

				sm.mutex.Lock()
				// Ensure address entry exists
				sm.createAddressEntryIfNotExists(addr)

				sm.sequences[addr][node] = seq
				sm.refreshTimes[addr][node] = time.Now()
				sm.mutex.Unlock()
			}(account.Address, nodeURL)
		}
	}

	// Wait for all goroutines to complete
	wg.Wait()

	return firstErr
}
