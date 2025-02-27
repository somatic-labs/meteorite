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
	sequences     map[string]uint64 // Map of address -> current sequence
	mutex         sync.RWMutex
	refreshTimes  map[string]time.Time
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
			sequences:     make(map[string]uint64),
			refreshTimes:  make(map[string]time.Time),
			refreshWindow: 30 * time.Second, // Refresh from chain every 30 seconds
		}
	})
	return globalSequenceManager
}

// GetSequence gets the current sequence for an address, refreshing from chain if needed
func (sm *SequenceManager) GetSequence(address string, config types.Config, forceRefresh bool) (uint64, error) {
	sm.mutex.RLock()
	seq, exists := sm.sequences[address]
	lastRefresh := sm.refreshTimes[address]
	sm.mutex.RUnlock()

	// Determine if we need to refresh from chain
	needsRefresh := forceRefresh || !exists || time.Since(lastRefresh) > sm.refreshWindow

	if needsRefresh {
		// Get the latest sequence from the chain
		sequence, _, err := GetAccountInfo(address, config)
		if err != nil {
			return 0, fmt.Errorf("failed to get sequence for %s: %w", address, err)
		}

		// Update our locally tracked sequence
		sm.mutex.Lock()
		sm.sequences[address] = sequence
		sm.refreshTimes[address] = time.Now()
		sm.mutex.Unlock()

		return sequence, nil
	}

	return seq, nil
}

// IncrementSequence increments the sequence for an address after successful transaction
func (sm *SequenceManager) IncrementSequence(address string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if seq, exists := sm.sequences[address]; exists {
		sm.sequences[address] = seq + 1
	}
}

// SetSequence explicitly sets a sequence for an address
func (sm *SequenceManager) SetSequence(address string, sequence uint64) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	sm.sequences[address] = sequence
	sm.refreshTimes[address] = time.Now()
}

// UpdateFromError updates the sequence based on a sequence mismatch error
// It parses the error message to extract the expected sequence
func (sm *SequenceManager) UpdateFromError(address, errMsg string) (uint64, bool) {
	expectedSeq, err := ExtractExpectedSequence(errMsg)
	if err != nil {
		return 0, false
	}

	sm.SetSequence(address, expectedSeq)
	return expectedSeq, true
}

// BatchReservation reserves a batch of sequential sequence numbers for an address
// This is useful when you know you'll be sending multiple sequential transactions
func (sm *SequenceManager) BatchReservation(address string, count int, config types.Config) (uint64, error) {
	// Get latest sequence from chain to ensure accuracy
	sequence, _, err := GetAccountInfo(address, config)
	if err != nil {
		return 0, fmt.Errorf("failed to get sequence for batch reservation: %w", err)
	}

	// Reserve the batch by updating our tracked sequence
	sm.mutex.Lock()
	startSeq := sequence
	sm.sequences[address] = sequence + uint64(count)
	sm.refreshTimes[address] = time.Now()
	sm.mutex.Unlock()

	return startSeq, nil
}

// PrefetchAllSequences preloads sequences for a list of accounts
func (sm *SequenceManager) PrefetchAllSequences(ctx context.Context, accounts []types.Account, config types.Config) error {
	// Use a wait group to fetch all sequences concurrently
	var wg sync.WaitGroup
	var errMutex sync.Mutex
	var firstErr error

	for _, account := range accounts {
		wg.Add(1)
		go func(addr string) {
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

			seq, _, err := GetAccountInfo(addr, config)
			if err != nil {
				errMutex.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("failed to prefetch sequence for %s: %w", addr, err)
				}
				errMutex.Unlock()
				return
			}

			sm.mutex.Lock()
			sm.sequences[addr] = seq
			sm.refreshTimes[addr] = time.Now()
			sm.mutex.Unlock()
		}(account.Address)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	return firstErr
}
