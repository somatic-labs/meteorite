package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
)

// NetAddress represents a network address
type NetAddress struct {
	IP   net.IP
	Port uint16
	ID   string
}

// NewNetAddress creates a new NetAddress
func NewNetAddress(ip net.IP, port uint16) *NetAddress {
	return &NetAddress{
		IP:   ip,
		Port: port,
	}
}

// BroadcastResult represents the result of a transaction broadcast
type BroadcastResult struct {
	Code      uint32
	Log       string
	TxHash    string
	Codespace string
	Height    int64
}

// P2PBroadcaster is responsible for broadcasting transactions through the P2P network
type P2PBroadcaster struct {
	seedPeers     []string
	client        *P2PClient
	mnemonic      string
	chainID       string
	logger        *log.Logger
	isInitialized bool
	mtx           sync.Mutex
}

// NewP2PBroadcaster creates a new P2P broadcaster
func NewP2PBroadcaster(seedPeers []string, mnemonic, chainID string) (*P2PBroadcaster, error) {
	logger := log.Default()

	return &P2PBroadcaster{
		seedPeers:     seedPeers,
		mnemonic:      mnemonic,
		chainID:       chainID,
		logger:        logger,
		isInitialized: false,
	}, nil
}

// Initialize initializes the P2P client
func (b *P2PBroadcaster) Initialize() error {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	if b.isInitialized {
		return nil
	}

	// Create P2P client
	client, err := NewP2PClient(b.seedPeers, b.mnemonic, b.chainID)
	if err != nil {
		return fmt.Errorf("failed to create P2P client: %w", err)
	}

	// Start the client
	if err := client.Start(); err != nil {
		return fmt.Errorf("failed to start P2P client: %w", err)
	}

	b.client = client
	b.isInitialized = true
	return nil
}

// Shutdown shuts down the P2P client
func (b *P2PBroadcaster) Shutdown() {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	if b.client != nil {
		b.client.Stop()
		b.client = nil
	}

	b.isInitialized = false
}

// BroadcastTx broadcasts a transaction through the P2P network
func (b *P2PBroadcaster) BroadcastTx(txBytes []byte) (*BroadcastResult, error) {
	b.mtx.Lock()
	if !b.isInitialized {
		if err := b.Initialize(); err != nil {
			b.mtx.Unlock()
			return nil, fmt.Errorf("failed to initialize P2P client: %w", err)
		}
	}
	b.mtx.Unlock()

	// Generate a hash for tracking this tx
	hash := sha256.Sum256(txBytes)
	txHash := hex.EncodeToString(hash[:])

	// Broadcast the transaction
	if err := b.client.BroadcastTx(txBytes); err != nil {
		return nil, fmt.Errorf("failed to broadcast transaction: %w", err)
	}

	// Since we're not really getting a response from the network in this simplified version,
	// we'll just return a synthetic success result
	b.logger.Printf("Transaction broadcast successful, hash: %s", txHash)
	return &BroadcastResult{
		Code:   0,
		Log:    "success",
		TxHash: txHash,
		Height: 0,
	}, nil
}

// FindPeers finds and connects to peers
func (b *P2PBroadcaster) FindPeers(ctx context.Context) error {
	b.mtx.Lock()
	if !b.isInitialized {
		if err := b.Initialize(); err != nil {
			b.mtx.Unlock()
			return fmt.Errorf("failed to initialize P2P client: %w", err)
		}
	}
	b.mtx.Unlock()

	// Log that we're starting peer discovery
	b.logger.Printf("Starting peer discovery with %d seed nodes", len(b.seedPeers))

	// In a real implementation, we would use the seed nodes to discover more peers
	// For now, we'll just connect to the seed nodes directly
	for _, seedAddr := range b.seedPeers {
		// Parse the address
		addr, err := b.parseAddress(seedAddr)
		if err != nil {
			b.logger.Printf("Failed to parse seed address %s: %v", seedAddr, err)
			continue
		}

		// Connect to the peer asynchronously
		go func(addr *NetAddress) {
			err := b.client.connectToPeer(addr.ID, addr.IP.String(), int(addr.Port))
			if err != nil {
				b.logger.Printf("Failed to connect to peer %s: %v", addr.ID, err)
			}
		}(addr)
	}

	return nil
}

// parseAddress parses a string address into a NetAddress
func (b *P2PBroadcaster) parseAddress(addrStr string) (*NetAddress, error) {
	// Format expected: "node_id@ip:port"
	parts := strings.Split(addrStr, "@")
	if len(parts) != 2 {
		// If no node_id, try to parse as "ip:port"
		hostPort := addrStr
		addrParts := strings.Split(hostPort, ":")
		if len(addrParts) != 2 {
			return nil, fmt.Errorf("invalid address format: %s", addrStr)
		}

		ip := net.ParseIP(addrParts[0])
		if ip == nil {
			return nil, fmt.Errorf("invalid IP address: %s", addrParts[0])
		}

		port, err := strconv.ParseUint(addrParts[1], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid port: %s", addrParts[1])
		}

		return &NetAddress{
			ID:   "",
			IP:   ip,
			Port: uint16(port),
		}, nil
	}

	nodeID := parts[0]
	hostPort := parts[1]

	addrParts := strings.Split(hostPort, ":")
	if len(addrParts) != 2 {
		return nil, fmt.Errorf("invalid address format: %s", addrStr)
	}

	ip := net.ParseIP(addrParts[0])
	if ip == nil {
		return nil, fmt.Errorf("invalid IP address: %s", addrParts[0])
	}

	port, err := strconv.ParseUint(addrParts[1], 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid port: %s", addrParts[1])
	}

	return &NetAddress{
		ID:   nodeID,
		IP:   ip,
		Port: uint16(port),
	}, nil
}
