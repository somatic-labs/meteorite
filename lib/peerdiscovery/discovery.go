package peerdiscovery

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
)

const (
	// DefaultTimeout is the default timeout for RPC requests
	DefaultTimeout = 5 * time.Second

	// MaxConcurrentChecks is the maximum number of concurrent RPC checks
	MaxConcurrentChecks = 50
)

// PeerDiscovery handles the peer discovery process
type PeerDiscovery struct {
	initialEndpoints []string
	chainID          string
	visitedNodes     map[string]bool
	openRPCEndpoints []string
	visitorMutex     sync.RWMutex
	resultsMutex     sync.RWMutex
	semaphore        chan struct{}
	ctx              context.Context
	cancel           context.CancelFunc
}

// New creates a new PeerDiscovery instance
func New(initialEndpoints []string) *PeerDiscovery {
	ctx, cancel := context.WithCancel(context.Background())

	return &PeerDiscovery{
		initialEndpoints: initialEndpoints,
		visitedNodes:     make(map[string]bool),
		openRPCEndpoints: make([]string, 0),
		semaphore:        make(chan struct{}, MaxConcurrentChecks),
		ctx:              ctx,
		cancel:           cancel,
	}
}

// DiscoverPeers discovers peers with open RPCs and returns their endpoints
func (pd *PeerDiscovery) DiscoverPeers(timeout time.Duration) ([]string, error) {
	// Create a context with timeout
	ctx, cancel := context.WithTimeout(pd.ctx, timeout)
	defer cancel()

	// Start a wait group to track all goroutines
	var wg sync.WaitGroup

	// Process the initial endpoints
	for _, endpoint := range pd.initialEndpoints {
		endpoint = normalizeEndpoint(endpoint)
		if endpoint != "" {
			wg.Add(1)
			go func(ep string) {
				defer wg.Done()
				pd.checkNode(ep)
			}(endpoint)
		}
	}

	// Wait for completion or timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Wait for either completion or timeout
	select {
	case <-done:
		// All nodes processed
		fmt.Println("Peer discovery completed successfully.")
	case <-ctx.Done():
		// Timeout or cancellation
		fmt.Println("Peer discovery timed out or was canceled.")
	}

	// Return the discovered endpoints
	pd.resultsMutex.RLock()
	defer pd.resultsMutex.RUnlock()

	// Make a copy to prevent external modification
	results := make([]string, len(pd.openRPCEndpoints))
	copy(results, pd.openRPCEndpoints)

	return results, nil
}

// Cleanup releases resources
func (pd *PeerDiscovery) Cleanup() {
	pd.cancel()
}

// checkNode checks if a node has an open RPC endpoint and discovers its peers
func (pd *PeerDiscovery) checkNode(nodeAddr string) {
	// Acquire semaphore to limit concurrency
	pd.semaphore <- struct{}{}
	defer func() { <-pd.semaphore }()

	// Check if we've already visited this node
	pd.visitorMutex.RLock()
	visited := pd.visitedNodes[nodeAddr]
	pd.visitorMutex.RUnlock()

	if visited {
		return
	}

	// Mark as visited
	pd.visitorMutex.Lock()
	pd.visitedNodes[nodeAddr] = true
	pd.visitorMutex.Unlock()

	// Skip if not a public IP (unless it's an initial endpoint)
	isInitial := false
	for _, ep := range pd.initialEndpoints {
		if normalizeEndpoint(ep) == nodeAddr {
			isInitial = true
			break
		}
	}

	if !isInitial {
		host := strings.Split(nodeAddr, ":")[0]
		host = strings.TrimPrefix(host, "http://")
		host = strings.TrimPrefix(host, "https://")

		// Skip localhost and private IP addresses
		if host == "localhost" || isPrivateIP(host) {
			return
		}
	}

	// Create a client with timeout
	client, err := http.NewWithTimeout(nodeAddr, "websocket", uint(DefaultTimeout.Milliseconds()))
	if err != nil {
		fmt.Printf("Failed to create client for %s: %v\n", nodeAddr, err)
		return
	}

	// Verify this is a working RPC endpoint
	status, err := client.Status(pd.ctx)
	if err != nil {
		fmt.Printf("Failed to get status from %s: %v\n", nodeAddr, err)
		return
	}

	// Set chainID from first successful node if not already set
	if pd.chainID == "" {
		pd.chainID = status.NodeInfo.Network
	} else if status.NodeInfo.Network != pd.chainID {
		// Skip nodes with different chain IDs
		fmt.Printf("Node %s is on a different chain: %s (expected %s)\n",
			nodeAddr, status.NodeInfo.Network, pd.chainID)
		return
	}

	// Add to open RPC endpoints
	pd.resultsMutex.Lock()
	pd.openRPCEndpoints = append(pd.openRPCEndpoints, nodeAddr)
	pd.resultsMutex.Unlock()

	fmt.Printf("Found open RPC endpoint: %s (Chain ID: %s)\n", nodeAddr, pd.chainID)

	// Discover peers through the net_info endpoint
	netInfo, err := client.NetInfo(pd.ctx)
	if err != nil {
		fmt.Printf("Failed to get net_info from %s: %v\n", nodeAddr, err)
		return
	}

	// Process each peer
	for _, peer := range netInfo.Peers {
		peerAddr := buildRPCAddress(peer)
		peerAddr = normalizeAddressWithRemoteIP(peerAddr, peer.RemoteIP)

		// Process each peer asynchronously
		if peerAddr != "" {
			go pd.checkNode("http://" + peerAddr)
		}
	}
}

// buildRPCAddress builds an RPC address from a peer
func buildRPCAddress(peer coretypes.Peer) string {
	rpcAddr := peer.NodeInfo.Other.RPCAddress
	rpcAddr = strings.TrimPrefix(rpcAddr, "tcp://")

	// If the node advertises a loopback address, replace with the actual IP
	if strings.HasPrefix(rpcAddr, "0.0.0.0:") || strings.HasPrefix(rpcAddr, "127.0.0.1:") {
		rpcAddr = peer.RemoteIP + rpcAddr[8:]
	}

	return rpcAddr
}

// normalizeAddressWithRemoteIP replaces loopback addresses with the remote IP
func normalizeAddressWithRemoteIP(nodeAddr, remoteIP string) string {
	nodeAddr = strings.ReplaceAll(nodeAddr, "0.0.0.0", remoteIP)
	nodeAddr = strings.ReplaceAll(nodeAddr, "127.0.0.1", remoteIP)
	return nodeAddr
}

// normalizeEndpoint ensures the endpoint has the correct format
func normalizeEndpoint(endpoint string) string {
	// Trim whitespace
	endpoint = strings.TrimSpace(endpoint)

	// Ensure it has http:// prefix
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}

	// Remove trailing slashes
	endpoint = strings.TrimRight(endpoint, "/")

	return endpoint
}

// isPrivateIP checks if an IP address is private
func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false // Not a valid IP address
	}

	// Check if the IP is in private IP ranges
	privateRanges := []struct {
		start net.IP
		end   net.IP
	}{
		{net.ParseIP("10.0.0.0"), net.ParseIP("10.255.255.255")},     // 10.0.0.0/8
		{net.ParseIP("172.16.0.0"), net.ParseIP("172.31.255.255")},   // 172.16.0.0/12
		{net.ParseIP("192.168.0.0"), net.ParseIP("192.168.255.255")}, // 192.168.0.0/16
	}

	for _, r := range privateRanges {
		if bytes4ToUint32(ip) >= bytes4ToUint32(r.start) && bytes4ToUint32(ip) <= bytes4ToUint32(r.end) {
			return true
		}
	}

	return false
}

// bytes4ToUint32 converts a 4-byte IP address to uint32 for comparison
func bytes4ToUint32(ip net.IP) uint32 {
	if len(ip) == 16 {
		ip = ip[12:16] // Use the last 4 bytes if it's an IPv6 address
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}
