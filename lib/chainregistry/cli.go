package chainregistry

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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
		// For API and GRPC, we'll use a derived endpoint if available
		rpcBase := selection.OpenEndpoints[0]

		// Convert RPC to API (assumed to be on port 1317)
		apiBase := strings.ReplaceAll(rpcBase, "26657", "1317")
		apiBase = strings.ReplaceAll(apiBase, "/rpc", "/rest")
		nodes["api"] = apiBase

		// Convert RPC to GRPC (assumed to be on port 9090)
		grpcBase := strings.ReplaceAll(rpcBase, "26657", "9090")
		grpcBase = strings.ReplaceAll(grpcBase, "http://", "")
		grpcBase = strings.ReplaceAll(grpcBase, "/rpc", "")
		nodes["grpc"] = grpcBase
	}

	config["nodes"] = nodes

	// Add basic message parameters
	config["msg_params"] = map[string]interface{}{
		"to_address": "",
		"amount":     1,
	}

	return config, nil
}
