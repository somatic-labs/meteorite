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
	addressManager  *lib.AddressManager
}

// NewMultiSendDistributor creates a new MultiSendDistributor
func NewMultiSendDistributor(config types.Config, rpcs []string) *MultiSendDistributor {
	// Initialize with the peer discovery module
	peerDiscovery := peerdiscovery.New(rpcs)

	// Get the address manager for CSV addresses
	addressManager := lib.GetAddressManager()
	// Attempt to load addresses from CSV
	_ = addressManager.LoadAddressesFromCSV()

	return &MultiSendDistributor{
		config:          config,
		rpcs:            rpcs,
		rpcIndex:        0,
		seedCounter:     0,
		peerDiscovery:   peerDiscovery,
		lastRefreshTime: time.Now(),
		addressManager:  addressManager,
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
	// We'll check more frequently now to find more non-registry nodes
	if m.rpcIndex == 0 || m.rpcIndex%10 == 0 {
		// Refresh endpoints more frequently
		if time.Since(m.lastRefreshTime) > 10*time.Minute || len(m.rpcs) < 10 {
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

	// Use a longer timeout for more thorough discovery
	discoveryTimeout := 45 * time.Second

	// Discover new peers
	_, err := m.peerDiscovery.DiscoverPeers(discoveryTimeout)
	if err != nil {
		fmt.Printf("Warning: Failed to discover new peers: %v\n", err)
		return
	}

	// Get the prioritized list (non-registry nodes first)
	prioritizedEndpoints := m.peerDiscovery.GetPrioritizedEndpoints()

	// If we found any endpoints, update our list
	if len(prioritizedEndpoints) > 0 {
		nonRegistryCount := len(m.peerDiscovery.GetNonRegistryNodes())
		fmt.Printf("Updated RPC endpoint list: %d endpoints (%d non-registry nodes)\n",
			len(prioritizedEndpoints), nonRegistryCount)
		m.rpcs = prioritizedEndpoints
		m.lastRefreshTime = time.Now()

		// Print a few non-registry nodes if we have them
		if nonRegistryCount > 0 {
			nonRegistryNodes := m.peerDiscovery.GetNonRegistryNodes()
			maxDisplay := 5
			if nonRegistryCount < maxDisplay {
				maxDisplay = nonRegistryCount
			}
			fmt.Printf("Sample non-registry nodes: %v\n", nonRegistryNodes[:maxDisplay])
		}
	}
}

// GetNextSeed returns the next seed value for randomization
func (m *MultiSendDistributor) GetNextSeed() int64 {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.seedCounter++
	return m.seedCounter
}

// GetPeerDiscovery returns the peer discovery instance
func (m *MultiSendDistributor) GetPeerDiscovery() *peerdiscovery.PeerDiscovery {
	return m.peerDiscovery
}

// Helper function to get or create a recipient address
func getOrCreateToAddress(specifiedAddress, randomSeed string) (sdk.AccAddress, error) {
	if specifiedAddress != "" {
		toAccAddress, err := sdk.AccAddressFromBech32(specifiedAddress)
		// If address is valid, use it
		if err == nil {
			return toAccAddress, nil
		}
		// Otherwise fall through to deterministic generation
	}

	// Generate a deterministic address from the seed
	toAccAddress, _, err := lib.GenerateDeterministicAccount(randomSeed)
	if err != nil {
		return nil, fmt.Errorf("error generating deterministic account: %w", err)
	}

	return toAccAddress, nil
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

	// First, try to use addresses from CSV if the slip44 is 118 (ATOM)
	useCSVAddresses := m.addressManager != nil && m.config.Slip44 == 118

	for i := 0; i < numRecipients; i++ {
		var toAccAddress sdk.AccAddress
		var addressFound bool

		// Attempt to get address from CSV file if applicable
		if useCSVAddresses {
			// Get an address from the CSV with the proper prefix conversion
			csvAddress, csvErr := m.addressManager.GetRandomAddressWithPrefix(m.config.Prefix)
			if csvErr == nil && csvAddress != "" {
				// Convert the address if needed
				csvAccAddress, addrErr := sdk.AccAddressFromBech32(csvAddress)
				if addrErr == nil {
					// Successfully got an address from CSV
					fmt.Printf("Using CSV address with prefix %s: %s\n", m.config.Prefix, csvAddress)
					toAccAddress = csvAccAddress
					addressFound = true
				}
			}
		}

		// If we couldn't get a CSV address, fall back to the standard method
		if !addressFound {
			// Generate a deterministic seed for this recipient
			randomSeed := fmt.Sprintf("%d-%d", seed, i)

			// Get or create recipient address
			var err error
			toAccAddress, err = getOrCreateToAddress(msgParams.ToAddress, randomSeed)
			if err != nil {
				return nil, "", err
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
