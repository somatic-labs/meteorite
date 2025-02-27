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
	// Tracking gas usage per message type
	GasUsageByMsgType map[string]*GasUsageStats
}

// GasUsageStats tracks gas usage statistics for a particular message type
type GasUsageStats struct {
	SuccessCount    int64
	TotalGasUsed    int64
	AverageGasUsed  int64
	RecentGasValues []int64 // Store recent gas values for better averaging
	MaxGasUsed      int64
	MinGasUsed      int64
	FailedCount     int64
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
			NodeURL:           nodeURL,
			AcceptsZeroGas:    false,
			MinAcceptableGas:  0,
			LastChecked:       time.Time{},
			GasUsageByMsgType: make(map[string]*GasUsageStats),
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
func (g *GasStrategyManager) RecordTransactionResult(nodeURL string, success bool, gasUsed int64, msgType string) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	caps, exists := g.nodeCapabilities[nodeURL]
	if !exists {
		caps = &NodeGasCapabilities{
			NodeURL:           nodeURL,
			AcceptsZeroGas:    false,
			MinAcceptableGas:  gasUsed,
			LastChecked:       time.Now(),
			GasUsageByMsgType: make(map[string]*GasUsageStats),
		}
	}

	// Ensure GasUsageByMsgType is initialized
	if caps.GasUsageByMsgType == nil {
		caps.GasUsageByMsgType = make(map[string]*GasUsageStats)
	}

	// Get or create stats for this message type
	stats, exists := caps.GasUsageByMsgType[msgType]
	if !exists {
		stats = &GasUsageStats{
			MinGasUsed:      gasUsed,
			MaxGasUsed:      gasUsed,
			RecentGasValues: make([]int64, 0, 10), // Keep last 10 gas values
		}
		caps.GasUsageByMsgType[msgType] = stats
	}

	if success {
		caps.SuccessfulTxCount++

		// Update gas usage statistics for this message type
		stats.SuccessCount++
		stats.TotalGasUsed += gasUsed
		stats.AverageGasUsed = stats.TotalGasUsed / stats.SuccessCount

		// Update min/max gas used
		if gasUsed < stats.MinGasUsed {
			stats.MinGasUsed = gasUsed
		}
		if gasUsed > stats.MaxGasUsed {
			stats.MaxGasUsed = gasUsed
		}

		// Store recent gas values for better averaging (keep last 10)
		if len(stats.RecentGasValues) >= 10 {
			stats.RecentGasValues = stats.RecentGasValues[1:] // Remove oldest
		}
		stats.RecentGasValues = append(stats.RecentGasValues, gasUsed)

		// If successful with lower gas than we thought was required for this node,
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
		stats.FailedCount++
	}

	caps.LastChecked = time.Now()
	g.nodeCapabilities[nodeURL] = caps
}

// GetAverageGasForMsgType returns the average gas used for a specific message type
func (g *GasStrategyManager) GetAverageGasForMsgType(nodeURL, msgType string) int64 {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	caps, exists := g.nodeCapabilities[nodeURL]
	if !exists || caps.GasUsageByMsgType == nil {
		return 0 // No data available
	}

	stats, exists := caps.GasUsageByMsgType[msgType]
	if !exists || stats.SuccessCount == 0 {
		return 0 // No data for this message type
	}

	// If we have recent values, calculate a weighted average
	if len(stats.RecentGasValues) > 0 {
		// Calculate weighted average with more weight to recent values
		total := int64(0)
		weights := 0
		for i, gas := range stats.RecentGasValues {
			weight := i + 1 // More weight to more recent values
			total += gas * int64(weight)
			weights += weight
		}
		return total / int64(weights)
	}

	return stats.AverageGasUsed
}

// GetRecommendedGasForMsgType returns the recommended gas for a message type
// It takes into account average usage, success rate, and applies a safety buffer
func (g *GasStrategyManager) GetRecommendedGasForMsgType(nodeURL, msgType string, baseGas int64) int64 {
	avgGas := g.GetAverageGasForMsgType(nodeURL, msgType)

	// If we have historical data, use it with a safety buffer
	if avgGas > 0 {
		// Apply a 20% safety buffer to the average
		safetyBuffer := float64(1.2)
		recommendedGas := int64(float64(avgGas) * safetyBuffer)

		// Always use at least the base gas
		if recommendedGas < baseGas {
			recommendedGas = baseGas
		}

		return recommendedGas
	}

	// If no historical data, return the base gas
	return baseGas
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
			NodeURL:           nodeURL,
			AcceptsZeroGas:    false,
			LastChecked:       time.Now(),
			GasUsageByMsgType: make(map[string]*GasUsageStats),
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
	msgType string,
	canUseZeroGas bool,
) uint64 {
	// Get node capabilities
	caps := g.GetNodeCapabilities(nodeURL)

	// If this node accepts zero gas and we're allowed to use it, use zero gas
	if caps.AcceptsZeroGas && canUseZeroGas {
		fmt.Printf("Using zero gas for node %s which accepts zero-gas transactions\n", nodeURL)
		return 0
	}

	// Try to get a recommended gas amount based on historical data
	recommendedGas := g.GetRecommendedGasForMsgType(nodeURL, msgType, int64(baseGasLimit))
	if recommendedGas > 0 {
		fmt.Printf("Using optimized gas amount for node %s and msg type %s: %d (based on historical data)\n",
			nodeURL, msgType, recommendedGas)
		return uint64(recommendedGas)
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
