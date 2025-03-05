package broadcast

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/somatic-labs/meteorite/lib"
	"github.com/somatic-labs/meteorite/types"
)

// NodeGasCapabilities tracks gas-related capabilities for a node
type NodeGasCapabilities struct {
	NodeURL           string
	AcceptsZeroGas    bool
	MinAcceptableGas  int64
	MinGasPrice       float64 // Minimum gas price required by this node
	LastChecked       time.Time
	SuccessfulTxCount int
	FailedTxCount     int
	GasUsageByMsgType map[string]*GasUsageStats
}

// GasUsageStats tracks gas usage statistics for a particular message type
type GasUsageStats struct {
	SuccessCount    int64
	TotalGasUsed    int64
	AverageGasUsed  int64
	RecentGasValues []int64
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
		return &NodeGasCapabilities{
			NodeURL:           nodeURL,
			AcceptsZeroGas:    false,
			MinAcceptableGas:  0,
			MinGasPrice:       0.0, // Default to 0 until updated
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
func (g *GasStrategyManager) RecordTransactionResult(nodeURL string, success bool, gasUsed int64, msgType string, txGasLimit uint64, requiredFee string) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	caps, exists := g.nodeCapabilities[nodeURL]
	if !exists {
		caps = &NodeGasCapabilities{
			NodeURL:           nodeURL,
			AcceptsZeroGas:    false,
			MinAcceptableGas:  gasUsed,
			MinGasPrice:       0.0,
			LastChecked:       time.Now(),
			GasUsageByMsgType: make(map[string]*GasUsageStats),
		}
	}

	if caps.GasUsageByMsgType == nil {
		caps.GasUsageByMsgType = make(map[string]*GasUsageStats)
	}

	stats, exists := caps.GasUsageByMsgType[msgType]
	if !exists {
		stats = &GasUsageStats{
			MinGasUsed:      gasUsed,
			MaxGasUsed:      gasUsed,
			RecentGasValues: make([]int64, 0, 10),
		}
		caps.GasUsageByMsgType[msgType] = stats
	}

	if success {
		caps.SuccessfulTxCount++
		stats.SuccessCount++
		stats.TotalGasUsed += gasUsed
		stats.AverageGasUsed = stats.TotalGasUsed / stats.SuccessCount

		if gasUsed < stats.MinGasUsed {
			stats.MinGasUsed = gasUsed
		}
		if gasUsed > stats.MaxGasUsed {
			stats.MaxGasUsed = gasUsed
		}

		if len(stats.RecentGasValues) >= 10 {
			stats.RecentGasValues = stats.RecentGasValues[1:]
		}
		stats.RecentGasValues = append(stats.RecentGasValues, gasUsed)

		if gasUsed < caps.MinAcceptableGas || caps.MinAcceptableGas == 0 {
			caps.MinAcceptableGas = gasUsed
		}

		if gasUsed == 0 {
			caps.AcceptsZeroGas = true
			fmt.Printf("Node %s accepts zero-gas transactions! Optimizing future transactions.\n", nodeURL)
		}
	} else {
		caps.FailedTxCount++
		stats.FailedCount++

		// Update MinGasPrice if the failure is due to insufficient fees
		if strings.Contains(requiredFee, "insufficient fee") {
			requiredAmount, requiredDenom, err := lib.ExtractRequiredFee(requiredFee)
			if err == nil && requiredDenom == "uatone" && txGasLimit > 0 {
				requiredGasPrice := float64(requiredAmount) / float64(txGasLimit)
				if requiredGasPrice > caps.MinGasPrice {
					caps.MinGasPrice = requiredGasPrice
					fmt.Printf("Updated MinGasPrice for node %s to %f based on required fee %d\n",
						nodeURL, caps.MinGasPrice, requiredAmount)
				}
			}
		}
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
		return 0
	}

	stats, exists := caps.GasUsageByMsgType[msgType]
	if !exists || stats.SuccessCount == 0 {
		return 0
	}

	if len(stats.RecentGasValues) > 0 {
		total := int64(0)
		weights := 0
		for i, gas := range stats.RecentGasValues {
			weight := i + 1
			total += gas * int64(weight)
			weights += weight
		}
		return total / int64(weights)
	}

	return stats.AverageGasUsed
}

// GetRecommendedGasForMsgType returns the recommended gas for a message type
func (g *GasStrategyManager) GetRecommendedGasForMsgType(nodeURL, msgType string, baseGas int64) int64 {
	avgGas := g.GetAverageGasForMsgType(nodeURL, msgType)

	if avgGas > 0 {
		safetyBuffer := float64(1.2)
		recommendedGas := int64(float64(avgGas) * safetyBuffer)

		if recommendedGas < baseGas {
			recommendedGas = baseGas
		}

		return recommendedGas
	}

	return baseGas
}

// TestZeroGasTransaction tests if a node accepts zero-gas transactions
func (g *GasStrategyManager) TestZeroGasTransaction(ctx context.Context, nodeURL string, _ types.Config) bool {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	caps, exists := g.nodeCapabilities[nodeURL]
	if exists && time.Since(caps.LastChecked) < 1*time.Hour {
		return caps.AcceptsZeroGas
	}

	if !exists {
		caps = &NodeGasCapabilities{
			NodeURL:           nodeURL,
			AcceptsZeroGas:    false,
			MinGasPrice:       0.0,
			LastChecked:       time.Now(),
			GasUsageByMsgType: make(map[string]*GasUsageStats),
		}
	}

	fmt.Printf("Testing if node %s accepts zero-gas transactions...\n", nodeURL)

	client, err := rpchttp.New(nodeURL, "/websocket")
	if err != nil {
		fmt.Printf("Failed to connect to node %s: %v\n", nodeURL, err)
		g.nodeCapabilities[nodeURL] = caps
		return false
	}

	_, err = client.Status(ctx)
	if err != nil {
		fmt.Printf("Node %s is unreachable: %v\n", nodeURL, err)
		g.nodeCapabilities[nodeURL] = caps
		return false
	}

	caps.LastChecked = time.Now()
	g.nodeCapabilities[nodeURL] = caps
	return false // Default until proven
}

// DetermineOptimalGasForNode determines the optimal gas limit for a specific node
func (g *GasStrategyManager) DetermineOptimalGasForNode(
	_ context.Context,
	nodeURL string,
	baseGasLimit uint64,
	msgType string,
	canUseZeroGas bool,
) uint64 {
	caps := g.GetNodeCapabilities(nodeURL)

	if caps.AcceptsZeroGas && canUseZeroGas {
		fmt.Printf("Using zero gas for node %s which accepts zero-gas transactions\n", nodeURL)
		return 0
	}

	recommendedGas := g.GetRecommendedGasForMsgType(nodeURL, msgType, int64(baseGasLimit))
	if recommendedGas > 0 {
		fmt.Printf("Using optimized gas amount for node %s and msg type %s: %d (based on historical data)\n",
			nodeURL, msgType, recommendedGas)
		return uint64(recommendedGas)
	}

	if caps.MinAcceptableGas > 0 && uint64(caps.MinAcceptableGas) < baseGasLimit {
		gasLimit := uint64(caps.MinAcceptableGas)
		fmt.Printf("Using optimized gas amount for node %s: %d (down from %d)\n",
			nodeURL, gasLimit, baseGasLimit)
		return gasLimit
	}

	return baseGasLimit
}

// GetMinGasPrice returns the minimum gas price for a node
func (g *GasStrategyManager) GetMinGasPrice(nodeURL string) float64 {
	caps := g.GetNodeCapabilities(nodeURL)
	return caps.MinGasPrice
}

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
