package broadcast

import (
	"context"
	"fmt"
	"sync"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/somatic-labs/meteorite/types"
)

// NodeGasCapabilities tracks whether a node accepts 0-gas transactions
type NodeGasCapabilities struct {
	NodeURL           string
	AcceptsZeroGas    bool
	MinAcceptableGas  int64
	LastChecked       time.Time
	SuccessfulTxCount int
	FailedTxCount     int
}

// GasStrategyManager manages gas optimization strategies for different nodes
type GasStrategyManager struct {
	nodeCapabilities map[string]*NodeGasCapabilities
	mutex            sync.RWMutex
}

// NewGasStrategyManager creates a new instance of the gas strategy manager
func NewGasStrategyManager() *GasStrategyManager {
	return &GasStrategyManager{
		nodeCapabilities: make(map[string]*NodeGasCapabilities),
	}
}

// GetNodeCapabilities returns the gas capabilities for a specific node
func (g *GasStrategyManager) GetNodeCapabilities(nodeURL string) *NodeGasCapabilities {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	caps, exists := g.nodeCapabilities[nodeURL]
	if !exists {
		// Return default capabilities if node hasn't been tested yet
		return &NodeGasCapabilities{
			NodeURL:          nodeURL,
			AcceptsZeroGas:   false,
			MinAcceptableGas: 0,
			LastChecked:      time.Time{},
		}
	}
	return caps
}

// UpdateNodeCapabilities updates the gas capabilities for a specific node
func (g *GasStrategyManager) UpdateNodeCapabilities(caps *NodeGasCapabilities) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.nodeCapabilities[caps.NodeURL] = caps
}

// RecordTransactionResult records the result of a transaction for a node
func (g *GasStrategyManager) RecordTransactionResult(nodeURL string, success bool, gasUsed int64) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	caps, exists := g.nodeCapabilities[nodeURL]
	if !exists {
		caps = &NodeGasCapabilities{
			NodeURL:          nodeURL,
			AcceptsZeroGas:   false,
			MinAcceptableGas: gasUsed,
			LastChecked:      time.Now(),
		}
	}

	if success {
		caps.SuccessfulTxCount++
		// If a transaction succeeded with lower gas than we thought was required,
		// update our minimum gas estimate
		if gasUsed < caps.MinAcceptableGas || caps.MinAcceptableGas == 0 {
			caps.MinAcceptableGas = gasUsed
		}

		// If a zero-gas transaction was successful, mark this node as accepting zero gas
		if gasUsed == 0 {
			caps.AcceptsZeroGas = true
			fmt.Printf("Node %s accepts zero-gas transactions! Optimizing future transactions.\n", nodeURL)
		}
	} else {
		caps.FailedTxCount++
	}

	caps.LastChecked = time.Now()
	g.nodeCapabilities[nodeURL] = caps
}

// TestZeroGasTransaction tests if a node accepts zero-gas transactions
func (g *GasStrategyManager) TestZeroGasTransaction(ctx context.Context, nodeURL string, _ types.Config) bool {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	caps, exists := g.nodeCapabilities[nodeURL]
	// If we already know this node or it was checked recently, don't test again
	if exists && time.Since(caps.LastChecked) < 1*time.Hour {
		return caps.AcceptsZeroGas
	}

	// Create a new capability entry if it doesn't exist
	if !exists {
		caps = &NodeGasCapabilities{
			NodeURL:        nodeURL,
			AcceptsZeroGas: false,
			LastChecked:    time.Now(),
		}
	}

	// We'll test a zero-gas transaction to see if the node accepts it
	fmt.Printf("Testing if node %s accepts zero-gas transactions...\n", nodeURL)

	// Connect to the node
	client, err := rpchttp.New(nodeURL, "/websocket")
	if err != nil {
		fmt.Printf("Failed to connect to node %s: %v\n", nodeURL, err)
		g.nodeCapabilities[nodeURL] = caps
		return false
	}

	// Check if the node is up first
	_, err = client.Status(ctx)
	if err != nil {
		fmt.Printf("Node %s is unreachable: %v\n", nodeURL, err)
		g.nodeCapabilities[nodeURL] = caps
		return false
	}

	// For now, we'll just set a flag to indicate that we should try a zero-gas transaction with this node
	// The actual test will happen when we send a real transaction
	caps.LastChecked = time.Now()
	g.nodeCapabilities[nodeURL] = caps

	return false // Default to false until proven otherwise
}

// DetermineOptimalGasForNode determines the optimal gas limit for a specific node
func (g *GasStrategyManager) DetermineOptimalGasForNode(
	_ context.Context,
	nodeURL string,
	baseGasLimit uint64,
	_ types.Config,
	canUseZeroGas bool,
) uint64 {
	// Get node capabilities
	caps := g.GetNodeCapabilities(nodeURL)

	// If this node accepts zero gas and we're allowed to use it, use zero gas
	if caps.AcceptsZeroGas && canUseZeroGas {
		fmt.Printf("Using zero gas for node %s which accepts zero-gas transactions\n", nodeURL)
		return 0
	}

	// If we have a known minimum gas value for this node, use it if it's lower than the base gas
	if caps.MinAcceptableGas > 0 && uint64(caps.MinAcceptableGas) < baseGasLimit {
		gasLimit := uint64(caps.MinAcceptableGas)
		fmt.Printf("Using optimized gas amount for node %s: %d (down from %d)\n",
			nodeURL, gasLimit, baseGasLimit)
		return gasLimit
	}

	// Otherwise use the base gas limit
	return baseGasLimit
}

// global instance of the gas strategy manager
var (
	globalGasManager     *GasStrategyManager
	globalGasManagerOnce sync.Once
)

// GetGasStrategyManager returns the global gas strategy manager
func GetGasStrategyManager() *GasStrategyManager {
	globalGasManagerOnce.Do(func() {
		globalGasManager = NewGasStrategyManager()
	})
	return globalGasManager
}
