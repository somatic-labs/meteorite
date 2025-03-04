package chainregistry

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/somatic-labs/meteorite/lib/peerdiscovery"
)

// ChainSelection represents a selected chain and its open RPC endpoints
type ChainSelection struct {
	Chain         *Chain
	OpenEndpoints []string
}

// SelectChainInteractive allows the user to select a chain from the registry interactively
func SelectChainInteractive(registry *Registry) (*ChainSelection, error) {
	chains := registry.GetChains()

	if len(chains) == 0 {
		return nil, errors.New("no chains found in registry")
	}

	// Print chains with their index
	fmt.Println("Available chains:")
	fmt.Println("------------------")
	for i, chain := range chains {
		status := ""
		if chain.Status != "" {
			status = fmt.Sprintf(" [%s]", chain.Status)
		}
		fmt.Printf("%3d. %s - %s%s\n", i+1, chain.ChainName, chain.PrettyName, status)
	}

	// Ask user to select a chain
	reader := bufio.NewReader(os.Stdin)

	var selectedIndex int
	for {
		fmt.Print("\nEnter the number of the chain to test (or 'q' to quit): ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("error reading input: %w", err)
		}

		input = strings.TrimSpace(input)
		if input == "q" || input == "quit" || input == "exit" {
			return nil, errors.New("user canceled chain selection")
		}

		index, err := strconv.Atoi(input)
		if err != nil || index < 1 || index > len(chains) {
			fmt.Println("Invalid selection. Please enter a number between 1 and", len(chains))
			continue
		}

		selectedIndex = index - 1 // Convert to 0-based index
		break
	}

	selectedChain := chains[selectedIndex]
	fmt.Printf("\nSelected chain: %s (%s)\n", selectedChain.PrettyName, selectedChain.ChainName)

	// Check chain prerequisites
	if len(selectedChain.APIs.RPC) == 0 {
		return nil, errors.New("selected chain has no RPC endpoints defined")
	}

	if len(selectedChain.Fees.FeeTokens) == 0 {
		return nil, errors.New("selected chain has no fee tokens defined")
	}

	// Step 1: Find initial open RPC endpoints from the registry
	fmt.Println("\nFinding open RPC endpoints from chain registry...")
	initialEndpoints, err := registry.FindOpenRPCEndpoints(selectedChain.ChainName)
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
		fmt.Println("Continuing with peer discovery...")
	} else {
		fmt.Printf("\nFound %d open RPC endpoints in the registry for %s\n", len(initialEndpoints), selectedChain.PrettyName)
		for i, endpoint := range initialEndpoints {
			fmt.Printf("%3d. %s\n", i+1, endpoint)
		}
	}

	// Step 2: Use peer discovery to find additional RPC endpoints
	fmt.Println("\nDiscovering additional peer RPC endpoints with public IPs...")
	fmt.Println("This may take a while as we explore the network...")

	// Initialize peer discovery with any open endpoints we found
	discovery := peerdiscovery.New(initialEndpoints)

	// Discovery timeout (adjust as needed)
	discoveryTimeout := 45 * time.Second

	// Discover additional peers
	allEndpoints, err := discovery.DiscoverPeers(discoveryTimeout)
	if err != nil {
		fmt.Printf("Warning: Error during peer discovery: %v\n", err)
	}

	// Clean up discovery resources
	discovery.Cleanup()

	// If we found additional endpoints, use them
	if len(allEndpoints) > len(initialEndpoints) {
		fmt.Printf("\nDiscovered a total of %d open RPC endpoints for %s\n",
			len(allEndpoints), selectedChain.PrettyName)
		fmt.Println("Using discovered endpoints instead of registry endpoints...")

		// Show a sample of the discovered endpoints (limit to avoid overwhelming output)
		maxDisplay := 10
		if len(allEndpoints) > maxDisplay {
			fmt.Printf("Showing first %d endpoints (of %d total):\n", maxDisplay, len(allEndpoints))
			for i, endpoint := range allEndpoints[:maxDisplay] {
				fmt.Printf("%3d. %s\n", i+1, endpoint)
			}
			fmt.Printf("... and %d more endpoints\n", len(allEndpoints)-maxDisplay)
		} else {
			for i, endpoint := range allEndpoints {
				fmt.Printf("%3d. %s\n", i+1, endpoint)
			}
		}

		// Use all discovered endpoints
		return &ChainSelection{
			Chain:         selectedChain,
			OpenEndpoints: allEndpoints,
		}, nil
	}

	// Fall back to original endpoints if peer discovery didn't find anything
	return &ChainSelection{
		Chain:         selectedChain,
		OpenEndpoints: initialEndpoints,
	}, nil
}

// GenerateConfigFromChain generates a configuration for a selected chain
func GenerateConfigFromChain(selection *ChainSelection) (map[string]interface{}, error) {
	chain := selection.Chain

	// Find the first fee token to use as the default
	var feeDenom string
	var fixedMinGasPrice float64
	if len(chain.Fees.FeeTokens) > 0 {
		feeDenom = chain.Fees.FeeTokens[0].Denom
		fixedMinGasPrice = chain.Fees.FeeTokens[0].FixedMinGasPrice
	}

	// Determine slip44 value based on chain information
	slip44 := determineSlip44(chain.ChainName, chain.Bech32Prefix)
	fmt.Printf("Using slip44 value %d for chain %s (prefix: %s)\n", slip44, chain.ChainName, chain.Bech32Prefix)

	// Create config
	config := map[string]interface{}{
		"chain":          chain.ChainID,
		"denom":          feeDenom,
		"prefix":         chain.Bech32Prefix,
		"gas_per_byte":   100,
		"base_gas":       200000,
		"msg_type":       "bank_send",
		"multisend":      true,
		"num_multisend":  10,
		"broadcast_mode": "grpc",
		"positions":      50,
		"slip44":         slip44, // Add slip44 value for correct address derivation
	}

	// Add gas configuration
	config["gas"] = map[string]interface{}{
		"low":       int64(fixedMinGasPrice),
		"precision": 3,
	}

	// Add nodes configuration with open endpoints
	nodes := map[string]interface{}{
		"rpc": selection.OpenEndpoints,
	}

	if len(selection.OpenEndpoints) > 0 {
		return configureNodeEndpoints(selection, chain)
	}

	// For GRPC, try to use endpoints from the registry first
	var grpcBase string
	if len(chain.APIs.GRPC) > 0 {
		grpcBase = chain.APIs.GRPC[0].Address
		// Strip protocol if present, as it's not used in the GRPC URL
		grpcBase = strings.ReplaceAll(grpcBase, "http://", "")
		grpcBase = strings.ReplaceAll(grpcBase, "https://", "")
	} else {
		// Fallback to derived GRPC from RPC
		rpcBase := selection.OpenEndpoints[0] // Use first endpoint for GRPC
		grpcBase = strings.ReplaceAll(rpcBase, "26657", "9090")
		grpcBase = strings.ReplaceAll(grpcBase, "http://", "")
		grpcBase = strings.ReplaceAll(grpcBase, "https://", "")
		grpcBase = strings.ReplaceAll(grpcBase, "/rpc", "")
	}
	nodes["grpc"] = grpcBase

	config["nodes"] = nodes

	// Add basic message parameters
	config["msg_params"] = map[string]interface{}{
		"to_address": "",
		"amount":     1,
	}

	return config, nil
}

// verificationCache stores API endpoint verification results
var (
	verificationCache = make(map[string]bool)
	cacheMutex        = &sync.RWMutex{}
)

// Helper function to verify an API endpoint works
func verifyAPIEndpoint(apiURL string) bool {
	// Check cache first
	cacheMutex.RLock()
	cachedResult, found := verificationCache[apiURL]
	cacheMutex.RUnlock()

	if found {
		if cachedResult {
			fmt.Printf("✅ Using cached verification for: %s\n", apiURL)
		}
		return cachedResult
	}

	// Try to make a simple request to verify the API is working
	testURL := apiURL + "/cosmos/base/tendermint/v1beta1/node_info"
	fmt.Printf("Testing API endpoint: %s\n", testURL)

	// Use a shorter timeout to fail faster
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		fmt.Printf("⚠️ Error creating request to verify API endpoint %s: %v\n", apiURL, err)
		cacheResult(apiURL, false)
		return false
	}

	// Add user agent and content type headers
	req.Header.Set("User-Agent", "Meteorite/1.0")
	req.Header.Set("Accept", "application/json")

	// Create a client with improved connection settings
	client := &http.Client{
		Timeout: 6 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
				MinVersion:         tls.VersionTLS12, // Minimum TLS version 1.2
			},
			MaxIdleConns:    10,
			IdleConnTimeout: 30 * time.Second,
		},
	}

	// Track request timing for debugging
	startTime := time.Now()
	resp, err := client.Do(req)
	requestDuration := time.Since(startTime)

	if err != nil {
		fmt.Printf("⚠️ API endpoint verification failed for %s after %.2fs: %v\n",
			apiURL, requestDuration.Seconds(), err)
		cacheResult(apiURL, false)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Printf("⚠️ API endpoint returned status %d for %s (%.2fs)\n",
			resp.StatusCode, testURL, requestDuration.Seconds())
		cacheResult(apiURL, false)
		return false
	}

	// Read a small amount of the body to verify it's valid JSON
	body := make([]byte, 512) // 512 bytes is enough to verify JSON structure
	n, err := io.ReadAtLeast(resp.Body, body, 1)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		fmt.Printf("⚠️ Error reading response body from %s: %v\n", apiURL, err)
		cacheResult(apiURL, false)
		return false
	}

	// Trim body to actual size read
	body = body[:n]

	// Check if body appears to be JSON
	if n > 0 && (body[0] == '{' || body[0] == '[') {
		// Faster validation - just check if it's valid JSON without full parsing
		if json.Valid(body) {
			fmt.Printf("✅ API endpoint verified: %s (%.2fs)\n", apiURL, requestDuration.Seconds())
			cacheResult(apiURL, true)
			return true
		}

		fmt.Printf("⚠️ API endpoint response is not valid JSON: %s\n", apiURL)
		fmt.Printf("Response first 100 bytes: %s\n", string(body[:minInt(100, n)]))
		cacheResult(apiURL, false)
		return false
	}

	fmt.Printf("⚠️ API endpoint response doesn't appear to be JSON: %s\n", apiURL)
	fmt.Printf("Response first 100 bytes: %s\n", string(body[:minInt(100, n)]))
	cacheResult(apiURL, false)
	return false
}

// Helper function to cache verification results
func cacheResult(apiURL string, result bool) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	verificationCache[apiURL] = result
}

// minInt returns the minimum of two integers
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// determineSlip44 returns the appropriate slip44/coin type for a given chain
func determineSlip44(chainName, prefix string) int {
	// Map common prefixes to their coin types
	prefixMap := map[string]int{
		"cosmos":    118, // Cosmos Hub
		"osmo":      118, // Osmosis (uses Cosmos coin type)
		"juno":      118, // Juno
		"evmos":     60,  // Evmos (uses Ethereum coin type)
		"injective": 60,  // Injective (uses Ethereum coin type)
		"atone":     118, // AtomOne
		"sei":       118, // Sei
		"akash":     118, // Akash
		"regen":     118, // Regen
		"secret":    529, // Secret Network
		"stargaze":  118, // Stargaze
		"umee":      118, // Umee
		"kujira":    118, // Kujira
		"neutron":   118, // Neutron
		"dydx":      118, // dYdX
		"mantra":    118, // Mantra
	}

	// Check for direct prefix match
	if coinType, ok := prefixMap[prefix]; ok {
		return coinType
	}

	// Check if chain name contains a known prefix using a switch pattern
	switch {
	case strings.Contains(chainName, "cosmos"):
		return 118
	case strings.Contains(chainName, "osmo"):
		return 118
	case strings.Contains(chainName, "juno"):
		return 118
	case strings.Contains(chainName, "evmos"):
		return 60
	case strings.Contains(chainName, "injective"):
		return 60
	case strings.Contains(chainName, "secret"):
		return 529
	// Chains using standard Cosmos coin type 118
	case strings.Contains(chainName, "atone"),
		strings.Contains(chainName, "sei"),
		strings.Contains(chainName, "akash"),
		strings.Contains(chainName, "regen"),
		strings.Contains(chainName, "stargaze"),
		strings.Contains(chainName, "umee"),
		strings.Contains(chainName, "kujira"),
		strings.Contains(chainName, "neutron"),
		strings.Contains(chainName, "dydx"),
		strings.Contains(chainName, "mantra"):
		return 118
	}

	// Default to Cosmos coin type (118) for unknown chains
	return 118
}

// configureNodeEndpoints configures API, GRPC and other endpoints using the available open endpoints
func configureNodeEndpoints(selection *ChainSelection, chain *Chain) (map[string]interface{}, error) {
	// For API, use REST endpoints from the chain registry directly
	var apiBase string
	var verified bool

	// Create a new function to check all endpoints concurrently
	verifyAPIEndpointsConcurrently := func(endpoints []APIEndpoint) string {
		fmt.Printf("Testing %d endpoints concurrently...\n", len(endpoints))

		// Create a channel for results
		results := make(chan string, len(endpoints))
		var wg sync.WaitGroup

		// Test each endpoint concurrently
		for _, endpoint := range endpoints {
			wg.Add(1)
			go func(e APIEndpoint) {
				defer wg.Done()
				endpointURL := strings.TrimSuffix(e.Address, "/")
				if verifyAPIEndpoint(endpointURL) {
					results <- endpointURL
				}
			}(endpoint)
		}

		// Wait for all endpoint checks to complete
		wg.Wait()
		close(results)

		// Return the first verified endpoint, if any
		for r := range results {
			return r
		}
		return ""
	}

	// Try to verify REST endpoints from registry
	if len(chain.APIs.Rest) > 0 {
		fmt.Println("Verifying REST endpoints from chain registry...")
		apiBase = verifyAPIEndpointsConcurrently(chain.APIs.Rest)
		verified = apiBase != ""
	}

	// If REST endpoints failed, try API endpoints
	if !verified && len(chain.APIs.API) > 0 {
		fmt.Println("Verifying API endpoints from chain registry...")
		apiBase = verifyAPIEndpointsConcurrently(chain.APIs.API)
		verified = apiBase != ""
	}

	// If both failed, derive an endpoint from the RPC nodes
	if !verified {
		fmt.Println("⚠️ Warning: Could not verify any API endpoint. Using first REST endpoint from registry (if available) but balance queries may fail.")
		// Use a fallback endpoint from available options
		switch {
		case len(chain.APIs.Rest) > 0:
			apiBase = strings.TrimSuffix(chain.APIs.Rest[0].Address, "/")
		case len(chain.APIs.API) > 0:
			apiBase = strings.TrimSuffix(chain.APIs.API[0].Address, "/")
		case len(selection.OpenEndpoints) > 0:
			// Fallback to derived endpoint from RPC
			rpcBase := selection.OpenEndpoints[0]
			apiBase = strings.ReplaceAll(rpcBase, "26657", "1317")
			apiBase = strings.ReplaceAll(apiBase, "/rpc", "/rest")
			apiBase = strings.TrimSuffix(apiBase, "/")
		}
	}

	// Configure nodes
	nodes := map[string]interface{}{
		"rpc": selection.OpenEndpoints,
		"api": apiBase,
	}

	// For GRPC, try to use endpoints from the registry first
	var grpcBase string
	if len(chain.APIs.GRPC) > 0 {
		grpcBase = strings.TrimSuffix(chain.APIs.GRPC[0].Address, "/")
	} else {
		// Fallback to derived endpoint from RPC
		grpcBase = strings.ReplaceAll(selection.OpenEndpoints[0], "26657", "9090")
		grpcBase = strings.ReplaceAll(grpcBase, "http://", "")
		grpcBase = strings.TrimSuffix(grpcBase, "/")
	}
	nodes["grpc"] = grpcBase

	// Build configuration
	config := map[string]interface{}{
		"chain":     chain.ChainName,
		"prefix":    chain.Bech32Prefix,
		"denom":     chain.Fees.FeeTokens[0].Denom,
		"slip44":    determineSlip44(chain.ChainName, chain.Bech32Prefix),
		"positions": 10, // A reasonable default number of accounts
		"msg_type":  "bank_send",
		"nodes":     nodes,
	}

	// Add gas configuration based on minimum gas price from chain
	fixedMinGasPrice := int64(1) // Default fallback
	if len(chain.Fees.FeeTokens) > 0 && chain.Fees.FeeTokens[0].FixedMinGasPrice > 0 {
		fixedMinGasPrice = int64(chain.Fees.FeeTokens[0].FixedMinGasPrice)
	}

	config["gas"] = map[string]interface{}{
		"low":       fixedMinGasPrice,
		"precision": 3,
	}

	return config, nil
}
