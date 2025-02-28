package broadcast

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
	nodes         []string
	nextNodeIndex map[uint32]int // map position -> next node index
	nodeUsage     map[string]int // track usage count for each node
	mutex         sync.RWMutex
}

var (
	globalNodeSelector *NodeSelector
	nodeSelectorOnce   sync.Once
)

// GetNodeSelector returns the global node selector
func GetNodeSelector() *NodeSelector {
	nodeSelectorOnce.Do(func() {
		globalNodeSelector = &NodeSelector{
			nodes:         []string{},
			nextNodeIndex: make(map[uint32]int),
			nodeUsage:     make(map[string]int),
		}
	})
	return globalNodeSelector
}

// SetNodes updates the list of available nodes
func (ns *NodeSelector) SetNodes(nodes []string) {
	ns.mutex.Lock()
	defer ns.mutex.Unlock()

	// Create a copy of the nodes slice
	ns.nodes = make([]string, len(nodes))
	copy(ns.nodes, nodes)

	// Reset usage statistics for nodes that no longer exist
	for node := range ns.nodeUsage {
		found := false
		for _, n := range nodes {
			if n == node {
				found = true
				break
			}
		}
		if !found {
			delete(ns.nodeUsage, node)
		}
	}

	// Initialize usage for new nodes
	for _, node := range ns.nodes {
		if _, exists := ns.nodeUsage[node]; !exists {
			ns.nodeUsage[node] = 0
		}
	}
}

// GetNextNode returns the next node for a specific position
// This ensures each position consistently uses the same sequence of nodes
func (ns *NodeSelector) GetNextNode(position uint32) string {
	ns.mutex.Lock()
	defer ns.mutex.Unlock()

	if len(ns.nodes) == 0 {
		return ""
	}

	// Get the current index for this position
	idx, exists := ns.nextNodeIndex[position]
	if !exists {
		// First time for this position, use position % nodes.length as starting point
		// This distributes positions across nodes from the beginning
		idx = int(position) % len(ns.nodes)
	}

	// Get the node
	node := ns.nodes[idx]

	// Update usage statistics
	ns.nodeUsage[node]++

	// Advance to next node for next call
	ns.nextNodeIndex[position] = (idx + 1) % len(ns.nodes)

	return node
}

// GetLeastUsedNode returns the node with the least usage count
func (ns *NodeSelector) GetLeastUsedNode() string {
	ns.mutex.RLock()
	defer ns.mutex.RUnlock()

	if len(ns.nodes) == 0 {
		return ""
	}

	var leastUsedNode string
	leastUsage := -1

	for _, node := range ns.nodes {
		usage := ns.nodeUsage[node]
		if leastUsage == -1 || usage < leastUsage {
			leastUsage = usage
			leastUsedNode = node
		}
	}

	return leastUsedNode
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
