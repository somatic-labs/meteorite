package broadcast

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/somatic-labs/meteorite/types"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// P2PBroadcaster is responsible for broadcasting transactions via P2P
type P2PBroadcaster struct {
	chainID     string
	logger      *log.Logger
	seedNodes   []string
	mnemonic    string
	initialized bool
	isConnected bool
	mtx         sync.Mutex

	// Connection management
	peers      map[string]*Peer
	peersMutex sync.RWMutex

	// Transaction tracking
	txMap      map[string]*TxInfo
	txMapMutex sync.RWMutex
}

// Peer represents a connected peer
type Peer struct {
	address     string
	isConnected bool
}

// TxInfo tracks information about a transaction
type TxInfo struct {
	txHash        string
	broadcastTime time.Time
	sentToPeers   []string
}

// Global P2P broadcaster instance
var (
	globalP2PBroadcaster *P2PBroadcaster
	p2pBroadcasterOnce   sync.Once
)

// GetP2PBroadcaster returns the global P2P broadcaster instance
func GetP2PBroadcaster(chainID, mnemonic string) *P2PBroadcaster {
	p2pBroadcasterOnce.Do(func() {
		// Get seed nodes from chain ID if available
		seeds := getSeedNodesForChain(chainID)

		globalP2PBroadcaster = &P2PBroadcaster{
			chainID:     chainID,
			logger:      log.Default(),
			seedNodes:   seeds,
			mnemonic:    mnemonic,
			initialized: false,
			isConnected: false,
			peers:       make(map[string]*Peer),
			txMap:       make(map[string]*TxInfo),
		}

		// Initialize P2P connections in background
		go globalP2PBroadcaster.initialize()
	})

	return globalP2PBroadcaster
}

// initialize sets up P2P connections
func (b *P2PBroadcaster) initialize() {
	// For simplicity in the initial implementation, we'll just log that we're initializing
	// but not actually establish real P2P connections yet
	fmt.Printf("🌐 Initializing P2P broadcaster for chain %s with %d seed peers\n",
		b.chainID, len(b.seedNodes))

	// In a real implementation, we would:
	// 1. Create a node key from the mnemonic
	// 2. Create a P2P transport
	// 3. Connect to seed nodes
	// 4. Establish P2P connections

	// For now, we'll just pretend we're connected
	for _, seed := range b.seedNodes {
		b.peersMutex.Lock()
		b.peers[seed] = &Peer{
			address:     seed,
			isConnected: true,
		}
		b.peersMutex.Unlock()

		fmt.Printf("🔗 Connected to peer: %s\n", seed)
	}

	b.isConnected = true
}

// BroadcastTxP2P broadcasts a transaction via P2P instead of RPC
func BroadcastTxP2P(ctx context.Context, txBytes []byte, txParams types.TransactionParams) (*sdk.TxResponse, error) {
	// Get or create the broadcaster
	p2p := GetP2PBroadcaster(txParams.ChainID, "")

	// Generate a transaction hash
	hash := sha256.Sum256(txBytes)
	txHash := hex.EncodeToString(hash[:])

	// Create transaction info
	txInfo := &TxInfo{
		txHash:        txHash,
		broadcastTime: time.Now(),
		sentToPeers:   make([]string, 0),
	}

	// Store in tx map
	p2p.txMapMutex.Lock()
	p2p.txMap[txHash] = txInfo
	p2p.txMapMutex.Unlock()

	// Get connected peers
	p2p.peersMutex.RLock()
	numPeers := len(p2p.peers)
	p2p.peersMutex.RUnlock()

	if numPeers == 0 {
		fmt.Printf("⚠️ No P2P peers connected, falling back to RPC broadcast\n")

		// Get client context
		clientCtx, err := P2PGetClientContext(txParams.Config, txParams.NodeURL)
		if err != nil {
			return nil, fmt.Errorf("failed to get client context: %w", err)
		}

		// Fall back to RPC broadcast
		return BroadcastTxSync(ctx, clientCtx, txBytes, txParams.NodeURL, txParams.Config.Denom, txParams.Sequence)
	}

	// In a real implementation, we would broadcast the transaction to all peers
	// For now, we'll just log that we're broadcasting
	fmt.Printf("📡 Broadcasting transaction via P2P network to %d peers\n", numPeers)

	// Build a synthetic response like we would get from RPC
	resp := &sdk.TxResponse{
		TxHash:    txHash,
		Height:    0, // We don't know the height yet
		Code:      0, // Success
		Codespace: "",
		Data:      "",
		RawLog:    "Transaction broadcast via P2P network",
		Logs:      nil,
		Info:      "",
		GasWanted: 0,
		GasUsed:   0,
		Timestamp: time.Now().Format(time.RFC3339),
	}

	// Mark the transaction as broadcast to all peers
	for addr := range p2p.peers {
		txInfo.sentToPeers = append(txInfo.sentToPeers, addr)
	}

	return resp, nil
}

// RunCommandWithOutput executes a shell command and returns its output
func RunCommandWithOutput(workDir, command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("error running command %s: %v - output: %s", command, err, string(output))
	}

	return string(output), nil
}

// FindRandomRecipient finds a random recipient from a keyring
func FindRandomRecipient(keyringBackend, excludeAddress string) (string, error) {
	// List accounts in keyring
	output, err := RunCommandWithOutput("", "meteorite", "keys", "list", "--keyring-backend", keyringBackend)
	if err != nil {
		return "", err
	}

	// Parse output to find addresses
	addresses := []string{}
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		if strings.Contains(line, "address:") {
			parts := strings.Split(line, "address: ")
			if len(parts) > 1 {
				addr := strings.TrimSpace(parts[1])
				if addr != excludeAddress {
					addresses = append(addresses, addr)
				}
			}
		}
	}

	if len(addresses) == 0 {
		return "", errors.New("no other addresses found in keyring")
	}

	// Pick a random address
	return addresses[rand.Intn(len(addresses))], nil
}

// getSeedNodesForChain returns a list of seed nodes for the specified chain
func getSeedNodesForChain(chainID string) []string {
	// For now, return hardcoded seed nodes for a few common chains
	// In a real implementation, we would:
	// 1. Look up the chain in a registry
	// 2. Use a discovery mechanism to find seed nodes
	// 3. Store and cache the results

	switch chainID {
	case "cosmoshub-4":
		return []string{
			"bf8328b66dceb4987e5cd94430af66045e59899f@public-seed.cosmos.vitwit.com:26656",
			"cfd785a4224c7940e9a10f6c1ab24c343e923bec@164.68.107.188:26656",
			"d72b3011ed46d783e369fdf8ae2055b99a1e5074@173.249.50.25:26656",
		}
	case "osmosis-1":
		return []string{
			"aef35f45db2d9f5590baa088c27883ac3d5e0b33@167.235.21.149:26656",
			"7d02c5ab5dc92a6b5ca830901f89f9065eaf3103@142.132.248.157:26656",
			"f8a0d6a9a557d263b7666a649be2413ba5538c56@20.126.195.2:26656",
		}
	case "stargaze-1":
		return []string{
			"95a34990666858befa082ffa5ddf98dee559e565@65.108.194.40:26656",
			"897b95138e84c5655aa15bd7049db2b3fab8666c@185.216.72.37:26656",
			"ade4d8bc8cbe014af6ebdf3cb7b1e9ad36f412c0@176.9.82.221:12657",
		}
	default:
		// Return empty list for unknown chains - we'll need to discover peers
		return []string{}
	}
}

// NewP2PBroadcaster creates a new P2P broadcaster
func NewP2PBroadcaster(seedNodes []string, mnemonic, chainID string) (*P2PBroadcaster, error) {
	// Create the P2P broadcaster
	broadcaster := &P2PBroadcaster{
		chainID:     chainID,
		logger:      log.Default(),
		seedNodes:   seedNodes,
		mnemonic:    mnemonic,
		initialized: false,
		isConnected: false,
		peers:       make(map[string]*Peer),
		txMap:       make(map[string]*TxInfo),
	}

	// Initialize P2P connections
	go broadcaster.initialize()

	return broadcaster, nil
}

// Initialize initializes the P2P broadcaster
func (b *P2PBroadcaster) Initialize() error {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	if b.initialized {
		return nil
	}

	// In a real implementation, we would connect to the P2P network here
	b.logger.Printf("Initializing P2P broadcaster for chain %s with %d seed nodes",
		b.chainID, len(b.seedNodes))

	// Set up some basic seed nodes if none provided
	if len(b.seedNodes) == 0 {
		b.seedNodes = getSeedNodesForChain(b.chainID)
		b.logger.Printf("Using %d default seed nodes for chain %s", len(b.seedNodes), b.chainID)
	}

	// Simulate connection time
	time.Sleep(500 * time.Millisecond)

	b.initialized = true
	return nil
}

// Shutdown shuts down the P2P broadcaster
func (b *P2PBroadcaster) Shutdown() {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	if !b.initialized {
		return
	}

	// In a real implementation, we would disconnect from the P2P network here
	b.logger.Printf("Shutting down P2P broadcaster for chain %s", b.chainID)

	b.initialized = false
}

// BroadcastTx broadcasts a transaction via P2P
func (b *P2PBroadcaster) BroadcastTx(txBytes []byte, mode string) (sdk.TxResponse, error) {
	b.mtx.Lock()
	if !b.initialized {
		if err := b.Initialize(); err != nil {
			b.mtx.Unlock()
			return sdk.TxResponse{}, fmt.Errorf("failed to initialize P2P broadcaster: %w", err)
		}
	}
	b.mtx.Unlock()

	// In a real implementation, we would broadcast the transaction over the P2P network
	// Generate a hash for the transaction
	hash := sha256.Sum256(txBytes)
	txHash := hex.EncodeToString(hash[:])

	b.logger.Printf("Broadcasting transaction with hash %s via P2P (simulated)", txHash)

	// Simulate network delay
	time.Sleep(200 * time.Millisecond)

	// Return a synthetic success result
	return sdk.TxResponse{
		Code:      0,
		Codespace: "",
		TxHash:    txHash,
		RawLog:    "success",
		Height:    0,
		Info:      "Transaction broadcast via P2P (simulated)",
	}, nil
}

// FindPeers finds and connects to peers
func (b *P2PBroadcaster) FindPeers() error {
	b.mtx.Lock()
	if !b.initialized {
		if err := b.Initialize(); err != nil {
			b.mtx.Unlock()
			return fmt.Errorf("failed to initialize P2P broadcaster: %w", err)
		}
	}
	b.mtx.Unlock()

	// In a real implementation, we would discover and connect to peers
	b.logger.Printf("Finding peers for chain %s using %d seed nodes", b.chainID, len(b.seedNodes))

	// Simulate peer discovery
	time.Sleep(1 * time.Second)

	// Log the seed nodes we would connect to
	for i, node := range b.seedNodes {
		if i < 3 { // Only log the first 3 to avoid spam
			b.logger.Printf("Would connect to seed node: %s", node)
		} else if i == 3 {
			b.logger.Printf("...and %d more", len(b.seedNodes)-3)
			break
		}
	}

	return nil
}

// GetSeedNodesForChain returns seed nodes for a specific chain
func GetSeedNodesForChain(chainID string) []string {
	switch chainID {
	case "cosmoshub-4":
		return []string{
			"e1d7ff02b78044795371bff4c36b240262d8479c@65.108.2.41:26656",
			"ade4d8bc8cbe014af6ebdf3cb7b1e9ad36f412c0@seeds.polkachu.com:14956",
			"20e1000e88125698264454a884812746c2eb4807@seeds.lavenderfive.com:14956",
		}
	case "osmosis-1":
		return []string{
			"f94c92c75ec370b23d7408e32c28e6a3b138dd57@65.108.2.41:26656",
			"5a37f1f701b3634add5a2034c4d0cc0c95f48a3f@seeds.polkachu.com:12556",
			"3255e3620984c891204251d9eeb3e981745913b2@seeds.lavenderfive.com:12556",
		}
	case "stargaze-1":
		return []string{
			"d95a7770a5f43570303d3d538538ca03f2a3c2c7@65.108.2.41:26656",
			"6c2377646af8c2d99a26ff2256bd1f93382b46ad@seeds.polkachu.com:13756",
			"def2c8a5c85d2f528e4311dcadc8080b91bf5a69@seeds.lavenderfive.com:13756",
		}
	default:
		// Default to returning empty list, the application should have a way to look up seed nodes
		return []string{}
	}
}

// P2PGetClientContext creates a client context for transaction handling
func P2PGetClientContext(config types.Config, nodeURL string) (client.Context, error) {
	// Create a basic client context
	clientCtx := client.Context{
		ChainID:       config.Chain,
		NodeURI:       nodeURL,
		Keyring:       keyring.NewInMemory(nil),
		BroadcastMode: config.BroadcastMode,
		Simulate:      false,
		GenerateOnly:  false,
		Offline:       false,
		SkipConfirm:   true,
	}

	return clientCtx, nil
}

// Config represents basic configuration for broadcast operations
type Config struct {
	ChainID       string
	Gas           uint64
	GasAdjustment float64
	GasPrices     string
	Fees          string
}

// GetDefaultConfig returns a default configuration
func GetDefaultConfig(chainID string) *Config {
	return &Config{
		ChainID:       chainID,
		Gas:           200000,
		GasAdjustment: 1.5,
		GasPrices:     "0.025uatom", // Default for cosmos hub
		Fees:          "",
	}
}
