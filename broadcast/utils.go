package broadcast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	types "github.com/somatic-labs/meteorite/types"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// ValidateTxParams validates the transaction parameters
func ValidateTxParams(txParams *types.TxParams) error {
	if txParams == nil {
		return errors.New("transaction parameters cannot be nil")
	}

	if txParams.ChainID == "" {
		return errors.New("chain ID cannot be empty")
	}

	if txParams.MsgType == "" {
		return errors.New("message type cannot be empty")
	}

	if txParams.PrivKey == nil {
		return errors.New("private key cannot be nil")
	}

	// Check if from address is valid
	fromAddress, ok := txParams.MsgParams["from_address"].(string)
	if !ok || fromAddress == "" {
		return errors.New("from address cannot be empty or invalid")
	}

	_, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return fmt.Errorf("invalid from address: %w", err)
	}

	return nil
}

// GetClientContext returns a client context for interacting with the chain
func GetClientContext(config types.Config, nodeURL string) (client.Context, error) {
	// Create codec and registry
	ir := codectypes.NewInterfaceRegistry()

	// Register necessary interfaces
	cryptocodec.RegisterInterfaces(ir)
	authtypes.RegisterInterfaces(ir)
	banktypes.RegisterInterfaces(ir)

	localCdc := codec.NewProtoCodec(ir)

	// Create the transaction config using the auth/tx module
	txConfig := authtx.NewTxConfig(localCdc, authtx.DefaultSignModes)

	// Use the provided node URL or default to the first RPC node
	rpcEndpoint := nodeURL
	if rpcEndpoint == "" && len(config.Nodes.RPC) > 0 {
		rpcEndpoint = config.Nodes.RPC[0]
	}

	// Create an RPC client
	rpcClient, err := rpchttp.New(rpcEndpoint, "/websocket")
	if err != nil {
		return client.Context{}, fmt.Errorf("failed to create RPC client: %w", err)
	}

	clientCtx := client.Context{
		ChainID:           config.Chain,
		Codec:             localCdc,
		InterfaceRegistry: ir,
		Output:            nil, // No output writer needed for this context
		OutputFormat:      "json",
		BroadcastMode:     "block", // Use block broadcast mode
		TxConfig:          txConfig,
		AccountRetriever:  authtypes.AccountRetriever{},
		NodeURI:           rpcEndpoint,
		Client:            rpcClient,
	}

	return clientCtx, nil
}

// GetAccountInfo retrieves the account number and sequence for an address
func GetAccountInfo(ctx context.Context, clientCtx client.Context, fromAddress string) (uint64, uint64, error) {
	// Parse the address
	address, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid address format: %w", err)
	}

	// Create account retriever
	accountRetriever := authtypes.AccountRetriever{}

	// Ensure the node is reachable before proceeding
	rpcClient := clientCtx.Client
	if rpcClient == nil {
		return 0, 0, errors.New("RPC client is not initialized")
	}

	// Try to get latest block info to check connection
	_, err = rpcClient.Status(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("node connection error: %w", err)
	}

	// Get account information from the chain
	accNum, sequence, err := accountRetriever.GetAccountNumberSequence(clientCtx, address)
	if err != nil {
		return 0, 0, fmt.Errorf("error getting account info: %w", err)
	}

	return accNum, sequence, nil
}

// MakeEncodingConfig creates an encoding configuration for transactions
func MakeEncodingConfig() codec.ProtoCodecMarshaler {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	marshaler := codec.NewProtoCodec(interfaceRegistry)

	// Register common interfaces
	authtypes.RegisterInterfaces(interfaceRegistry)
	banktypes.RegisterInterfaces(interfaceRegistry)

	return marshaler
}

// NodeSelector manages the distribution of transactions across RPC nodes
type NodeSelector struct {
	nodes               []string
	nextNodeIndex       map[uint32]int  // map position -> next node index
	nodeUsage           map[string]int  // track usage count for each node
	nodeHealth          map[string]bool // track health status of each node
	mutex               sync.RWMutex
	lastHealthCheck     time.Time
	healthCheckInterval time.Duration
}

var (
	globalNodeSelector *NodeSelector
	nodeSelectorOnce   sync.Once
)

// GetNodeSelector returns the global node selector
func GetNodeSelector() *NodeSelector {
	nodeSelectorOnce.Do(func() {
		globalNodeSelector = &NodeSelector{}
	})
	return globalNodeSelector
}

// SetNodes updates the list of available nodes
func (ns *NodeSelector) SetNodes(nodes []string) {
	ns.mutex.Lock()
	defer ns.mutex.Unlock()

	if len(nodes) == 0 {
		return
	}

	// Initialize health status map if needed
	if ns.nodeHealth == nil {
		ns.nodeHealth = make(map[string]bool)
	}

	// Set default health check interval if not set
	if ns.healthCheckInterval == 0 {
		ns.healthCheckInterval = 5 * time.Minute
	}

	// Filter nodes based on health if we haven't done a health check recently
	if time.Since(ns.lastHealthCheck) > ns.healthCheckInterval || len(ns.nodes) == 0 {
		fmt.Println("Performing node health check...")
		healthyNodes := FilterHealthyNodes(nodes)

		// If we found healthy nodes, use only those
		if len(healthyNodes) > 0 {
			nodes = healthyNodes
		} else {
			fmt.Println("Warning: Using all provided nodes as no healthy nodes were found")
		}

		// Update health status for all nodes
		for _, node := range nodes {
			healthy, _ := CheckNodeHealth(node)
			ns.nodeHealth[node] = healthy
		}

		ns.lastHealthCheck = time.Now()
	}

	// Store the nodes
	ns.nodes = nodes

	// Initialize or reset the node index map
	if ns.nextNodeIndex == nil {
		ns.nextNodeIndex = make(map[uint32]int)
	}

	// Initialize the usage counter if needed
	if ns.nodeUsage == nil {
		ns.nodeUsage = make(map[string]int)
	}

	// Reset counters for new nodes that don't have entries yet
	for _, node := range nodes {
		if _, exists := ns.nodeUsage[node]; !exists {
			ns.nodeUsage[node] = 0
		}
	}

	fmt.Printf("Node selector initialized with %d nodes\n", len(nodes))
}

// GetNextNode gets the next node for a specific position, preferring healthy nodes
func (ns *NodeSelector) GetNextNode(position uint32) string {
	ns.mutex.Lock()
	defer ns.mutex.Unlock()

	if len(ns.nodes) == 0 {
		return ""
	}

	// Check if we need to refresh node health status
	if time.Since(ns.lastHealthCheck) > ns.healthCheckInterval {
		// Release lock during potentially long operation
		ns.mutex.Unlock()
		ns.CheckNodeStatuses()
		ns.mutex.Lock()
	}

	// Try to find a healthy node first
	var healthyNodes []string
	for _, node := range ns.nodes {
		if healthy, exists := ns.nodeHealth[node]; exists && healthy {
			healthyNodes = append(healthyNodes, node)
		}
	}

	// If we have healthy nodes, select from those
	nodeList := ns.nodes
	if len(healthyNodes) > 0 {
		nodeList = healthyNodes
	}

	// Get current index for this position
	idx, exists := ns.nextNodeIndex[position]
	if !exists {
		// Initialize with a position-based offset to distribute initial load
		idx = int(position % uint32(len(nodeList)))
	}

	// Get the node
	node := nodeList[idx]

	// Update index for next call
	ns.nextNodeIndex[position] = (idx + 1) % len(nodeList)

	return node
}

// CheckNodeStatuses performs a health check on all nodes and updates their status
func (ns *NodeSelector) CheckNodeStatuses() {
	ns.mutex.Lock()
	defer ns.mutex.Unlock()

	if len(ns.nodes) == 0 {
		return
	}

	fmt.Println("Checking status of all nodes...")

	// Check each node's health
	var healthyCount int
	for _, node := range ns.nodes {
		healthy, err := CheckNodeHealth(node)
		ns.nodeHealth[node] = healthy

		if healthy {
			healthyCount++
			fmt.Printf("Node %s is healthy\n", node)
		} else {
			errMsg := "unknown error"
			if err != nil {
				errMsg = err.Error()
			}
			fmt.Printf("Node %s is unhealthy: %s\n", node, errMsg)
		}
	}

	fmt.Printf("Node health check complete: %d/%d nodes are healthy\n",
		healthyCount, len(ns.nodes))

	ns.lastHealthCheck = time.Now()
}

// GetLeastUsedNode returns the node with the least usage count, preferring healthy nodes
func (ns *NodeSelector) GetLeastUsedNode() string {
	ns.mutex.Lock()
	defer ns.mutex.Unlock()

	if len(ns.nodes) == 0 {
		return ""
	}

	// Check if we need to refresh node health
	if time.Since(ns.lastHealthCheck) > ns.healthCheckInterval {
		// Release lock during potentially long operation
		ns.mutex.Unlock()
		ns.CheckNodeStatuses()
		ns.mutex.Lock()
	}

	var candidate string
	var minUsage int = -1

	// First try to find the least used healthy node
	for _, node := range ns.nodes {
		usage := ns.nodeUsage[node]
		isHealthy, _ := ns.nodeHealth[node]

		if isHealthy && (minUsage == -1 || usage < minUsage) {
			minUsage = usage
			candidate = node
		}
	}

	// If no healthy node found, fall back to any node
	if candidate == "" {
		for _, node := range ns.nodes {
			usage := ns.nodeUsage[node]
			if minUsage == -1 || usage < minUsage {
				minUsage = usage
				candidate = node
			}
		}
	}

	return candidate
}

// GetNodeUsage returns statistics about node usage
func (ns *NodeSelector) GetNodeUsage() map[string]int {
	ns.mutex.RLock()
	defer ns.mutex.RUnlock()

	// Create a copy of the usage statistics
	usage := make(map[string]int, len(ns.nodeUsage))
	for node, count := range ns.nodeUsage {
		usage[node] = count
	}

	return usage
}

// RecordUsage increments the usage count for a specific node
func (ns *NodeSelector) RecordUsage(nodeURL string) {
	ns.mutex.Lock()
	defer ns.mutex.Unlock()

	if _, exists := ns.nodeUsage[nodeURL]; exists {
		ns.nodeUsage[nodeURL]++
	}
}

// CheckNodeHealth tests if a node is responsive and ready to accept transactions
func CheckNodeHealth(nodeURL string) (bool, error) {
	// First check if node status endpoint is responsive (RPC test)
	statusURL := fmt.Sprintf("%s/status", nodeURL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create status request: %w", err)
	}

	// Set headers for better compatibility
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Meteorite/1.0")

	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     60 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   false,
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to get status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("status endpoint returned %d", resp.StatusCode)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read status response: %w", err)
	}

	// Parse response to verify it's valid
	var statusResp map[string]interface{}
	if err := json.Unmarshal(body, &statusResp); err != nil {
		return false, fmt.Errorf("failed to parse status response: %w", err)
	}

	// Check if the node is not catching up
	result, hasResult := statusResp["result"].(map[string]interface{})
	if hasResult {
		syncInfo, hasSyncInfo := result["sync_info"].(map[string]interface{})
		if hasSyncInfo {
			catchingUp, ok := syncInfo["catching_up"].(bool)
			if ok && catchingUp {
				return false, fmt.Errorf("node is still syncing")
			}

			// Check if node has a reasonable latest block height
			latestBlockHeight, ok := syncInfo["latest_block_height"].(string)
			if ok {
				height, err := strconv.ParseInt(latestBlockHeight, 10, 64)
				if err == nil && height < 1 {
					return false, fmt.Errorf("node has zero or invalid block height")
				}
			}
		}
	}

	// Additional check: Make a small request to the mempool to see if that's responding too
	mempoolURL := fmt.Sprintf("%s/unconfirmed_txs?limit=1", nodeURL)
	mempoolReq, err := http.NewRequestWithContext(ctx, http.MethodGet, mempoolURL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create mempool request: %w", err)
	}
	mempoolReq.Header.Set("Accept", "application/json")

	mempoolResp, err := client.Do(mempoolReq)
	if err != nil {
		return false, fmt.Errorf("mempool endpoint not responding: %w", err)
	}
	defer mempoolResp.Body.Close()

	if mempoolResp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("mempool endpoint returned %d", mempoolResp.StatusCode)
	}

	// Node is healthy if we've made it here
	return true, nil
}

// FilterHealthyNodes takes a list of RPC nodes and returns only those that are healthy
func FilterHealthyNodes(nodes []string) []string {
	if len(nodes) == 0 {
		return nil
	}

	var healthyNodes []string
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Check all nodes concurrently
	for _, node := range nodes {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()

			healthy, err := CheckNodeHealth(url)
			if healthy && err == nil {
				mu.Lock()
				healthyNodes = append(healthyNodes, url)
				fmt.Printf("Node %s is healthy and ready\n", url)
				mu.Unlock()
			} else {
				fmt.Printf("Node %s is unhealthy: %v\n", url, err)
			}
		}(node)
	}

	wg.Wait()

	if len(healthyNodes) == 0 {
		fmt.Println("Warning: No healthy nodes found! Using all provided nodes as fallback.")
		return nodes // Fallback to all nodes if none are healthy
	}

	fmt.Printf("Found %d healthy nodes out of %d total nodes\n", len(healthyNodes), len(nodes))
	return healthyNodes
}
