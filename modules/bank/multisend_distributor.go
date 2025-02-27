package bank

import (
	"fmt"
	"sync"
	"time"

	"github.com/somatic-labs/meteorite/lib"
	"github.com/somatic-labs/meteorite/lib/peerdiscovery"
	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// MultiSendDistributor manages sending different multisend transactions to different RPC endpoints
type MultiSendDistributor struct {
	config          types.Config
	rpcs            []string
	rpcIndex        int
	mutex           sync.Mutex
	seedCounter     int64
	peerDiscovery   *peerdiscovery.PeerDiscovery
	lastRefreshTime time.Time
}

// NewMultiSendDistributor creates a new MultiSendDistributor
func NewMultiSendDistributor(config types.Config, rpcs []string) *MultiSendDistributor {
	// Initialize with the peer discovery module
	peerDiscovery := peerdiscovery.New(rpcs)

	return &MultiSendDistributor{
		config:          config,
		rpcs:            rpcs,
		rpcIndex:        0,
		seedCounter:     0,
		peerDiscovery:   peerDiscovery,
		lastRefreshTime: time.Now(),
	}
}

// GetNextRPC returns the next RPC endpoint in a round-robin fashion
func (m *MultiSendDistributor) GetNextRPC() string {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if len(m.rpcs) == 0 {
		return ""
	}

	rpc := m.rpcs[m.rpcIndex]
	m.rpcIndex = (m.rpcIndex + 1) % len(m.rpcs)

	// If we've gone through all nodes once, consider refreshing the list
	if m.rpcIndex == 0 {
		// Refresh endpoints every 30 minutes or if our endpoint list is small
		if time.Since(m.lastRefreshTime) > 30*time.Minute || len(m.rpcs) < 5 {
			go m.RefreshEndpoints()
		}
	}

	return rpc
}

// RefreshEndpoints discovers new RPC endpoints and updates the distributor's list
func (m *MultiSendDistributor) RefreshEndpoints() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	fmt.Println("Refreshing RPC endpoints through peer discovery...")

	// Use a reasonable timeout for discovery
	discoveryTimeout := 30 * time.Second

	// Discover new peers
	newEndpoints, err := m.peerDiscovery.DiscoverPeers(discoveryTimeout)
	if err != nil {
		fmt.Printf("Warning: Failed to discover new peers: %v\n", err)
		return
	}

	// If we found new endpoints, update our list
	if len(newEndpoints) > len(m.rpcs) {
		fmt.Printf("Updated RPC endpoint list: %d → %d endpoints\n",
			len(m.rpcs), len(newEndpoints))
		m.rpcs = newEndpoints
		m.lastRefreshTime = time.Now()
	}
}

// GetNextSeed returns the next seed value for randomization
func (m *MultiSendDistributor) GetNextSeed() int64 {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	seed := m.seedCounter
	m.seedCounter++
	return seed
}

// CreateDistributedMultiSendMsg creates a multisend message with a unique set of recipients
// based on the RPC endpoint it will be sent to. This ensures different mempools across nodes.
func (m *MultiSendDistributor) CreateDistributedMultiSendMsg(
	fromAddress string,
	msgParams types.MsgParams,
	seed int64,
) (sdk.Msg, string, error) {
	fromAccAddress, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return nil, "", fmt.Errorf("invalid from address: %w", err)
	}

	// Calculate the total amount to send (amount per recipient * number of recipients)
	numRecipients := m.config.NumMultisend
	if numRecipients <= 0 {
		numRecipients = 1 // Default to 1 if not properly configured
	}

	amountPerRecipient := sdkmath.NewInt(msgParams.Amount)
	totalAmount := amountPerRecipient.MulRaw(int64(numRecipients))

	// Create the input for the multisend (from the sender)
	input := banktypes.Input{
		Address: fromAccAddress.String(),
		Coins:   sdk.NewCoins(sdk.NewCoin(m.config.Denom, totalAmount)),
	}

	// Create outputs for each recipient based on the seed
	outputs := make([]banktypes.Output, 0, numRecipients)

	for i := 0; i < numRecipients; i++ {
		// Generate a deterministic seed for this recipient
		randomSeed := fmt.Sprintf("%d-%d", seed, i)

		var toAccAddress sdk.AccAddress
		var err error

		// Try to use the specified recipient if provided
		if msgParams.ToAddress != "" {
			toAccAddress, err = sdk.AccAddressFromBech32(msgParams.ToAddress)
			// If address is invalid, fall back to deterministic generation
			if err != nil {
				toAccAddress, err = lib.GenerateDeterministicAccount(randomSeed)
				if err != nil {
					return nil, "", fmt.Errorf("error generating deterministic account: %w", err)
				}
			}
		} else {
			// No address specified, generate a deterministic one
			toAccAddress, err = lib.GenerateDeterministicAccount(randomSeed)
			if err != nil {
				return nil, "", fmt.Errorf("error generating deterministic account: %w", err)
			}
		}

		// Add this recipient to the outputs
		outputs = append(outputs, banktypes.Output{
			Address: toAccAddress.String(),
			Coins:   sdk.NewCoins(sdk.NewCoin(m.config.Denom, amountPerRecipient)),
		})
	}

	// Create the multisend message
	msg := &banktypes.MsgMultiSend{
		Inputs:  []banktypes.Input{input},
		Outputs: outputs,
	}

	// Generate a random memo
	memo, err := lib.GenerateRandomStringOfLength(256)
	if err != nil {
		return nil, "", fmt.Errorf("error generating random memo: %w", err)
	}

	return msg, memo, nil
}

// Cleanup releases resources used by the distributor
func (m *MultiSendDistributor) Cleanup() {
	if m.peerDiscovery != nil {
		m.peerDiscovery.Cleanup()
	}
}
