package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// Broadcaster is responsible for broadcasting transactions through the P2P network
type Broadcaster struct {
	seedPeers     []string
	client        *Client
	mnemonic      string
	chainID       string
	logger        *log.Logger
	isInitialized bool
	mtx           sync.Mutex
}

// NewP2PBroadcaster creates a new P2P broadcaster
func NewP2PBroadcaster(seedPeers []string, mnemonic, chainID string) (*Broadcaster, error) {
	logger := log.Default()

	return &Broadcaster{
		seedPeers:     seedPeers,
		mnemonic:      mnemonic,
		chainID:       chainID,
		logger:        logger,
		isInitialized: false,
	}, nil
}

// Initialize initializes the P2P client
func (b *Broadcaster) Initialize() error {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	if b.isInitialized {
		return nil
	}

	// Create P2P client
	client, err := NewClient(b.seedPeers, b.mnemonic, b.chainID)
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
func (b *Broadcaster) Shutdown() {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	if !b.isInitialized || b.client == nil {
		return
	}

	b.client.Stop()
	b.client = nil
	b.isInitialized = false
}

// BroadcastTx broadcasts a transaction through the P2P network
func (b *Broadcaster) BroadcastTx(txBytes []byte) (*BroadcastResult, error) {
	b.mtx.Lock()
	initialized := b.isInitialized
	b.mtx.Unlock()

	if !initialized {
		if err := b.Initialize(); err != nil {
			return nil, fmt.Errorf("failed to initialize P2P broadcaster: %w", err)
		}
	}

	if b.client == nil {
		return nil, errors.New("P2P client is not initialized")
	}

	// Create a hash of the transaction for tracking
	hash := sha256.Sum256(txBytes)
	txHash := hex.EncodeToString(hash[:])

	// Log the transaction broadcast
	b.logger.Printf("Broadcasting transaction with hash %s", txHash)

	// Broadcast the transaction
	err := b.client.BroadcastTx(txBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to broadcast transaction: %w", err)
	}

	// Return a synthetic success result
	return &BroadcastResult{
		Code:   0,
		Log:    "Transaction broadcast through P2P network",
		TxHash: txHash,
		Height: -1, // unknown height
	}, nil
}

// FindPeers attempts to connect to peers in the network
func (b *Broadcaster) FindPeers(ctx context.Context) error {
	b.mtx.Lock()
	initialized := b.isInitialized
	b.mtx.Unlock()

	if !initialized {
		if err := b.Initialize(); err != nil {
			return fmt.Errorf("failed to initialize P2P broadcaster: %w", err)
		}
	}

	// Use the context to potentially cancel the peer discovery process
	done := make(chan struct{})
	go func() {
		// Loop through any hardcoded seed nodes and connect to them
		for _, seedAddr := range b.seedPeers {
			// Check if context is canceled
			select {
			case <-ctx.Done():
				return
			default:
				// Continue with peer connection
			}

			// Parse the address
			addr, err := b.parseAddress(seedAddr)
			if err != nil {
				b.logger.Printf("Failed to parse seed address %s: %v", seedAddr, err)
				continue
			}

			// Connect to the peer asynchronously
			go func(addr *NetAddress) {
				b.client.connectToPeer(addr.ID, addr.IP.String(), int(addr.Port))
			}(addr)
		}
		close(done)
	}()

	// Wait for peer discovery to complete or context to be canceled
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseAddress parses a string address into a NetAddress struct
func (b *Broadcaster) parseAddress(addrStr string) (*NetAddress, error) {
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
