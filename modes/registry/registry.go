package registry

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/somatic-labs/meteorite/lib/chainregistry"
)

// RunRegistryMode runs the registry mode
func RunRegistryMode() error {
	fmt.Println("Meteorite Chain Registry Tester")
	fmt.Println("==============================")

	// Create a new registry client
	registry := chainregistry.NewRegistry("")

	// Download the registry
	fmt.Println("Downloading the Cosmos Chain Registry...")
	err := registry.Download()
	if err != nil {
		return fmt.Errorf("error downloading chain registry: %v", err)
	}

	// Load chains
	fmt.Println("Loading chains from registry...")
	err = registry.LoadChains()
	if err != nil {
		return fmt.Errorf("error loading chains: %v", err)
	}

	// Select a chain interactively
	selection, err := chainregistry.SelectChainInteractive(registry)
	if err != nil {
		return fmt.Errorf("error selecting chain: %v", err)
	}

	// Generate config
	config, err := chainregistry.GenerateConfigFromChain(selection)
	if err != nil {
		return fmt.Errorf("error generating config: %v", err)
	}

	// Save config to file
	fmt.Println("\nGenerating configuration file...")

	// Create configurations directory if it doesn't exist
	configsDir := "configurations"
	chainDir := filepath.Join(configsDir, selection.Chain.ChainName)

	err = os.MkdirAll(chainDir, 0o755)
	if err != nil {
		return fmt.Errorf("error creating directories: %v", err)
	}

	configPath := filepath.Join(chainDir, "nodes.toml")

	// Check if file exists
	if _, err := os.Stat(configPath); err == nil {
		// Backup existing file
		backupFilename := configPath + ".bak"
		fmt.Printf("Backing up existing config to %s\n", backupFilename)
		err = os.Rename(configPath, backupFilename)
		if err != nil {
			return fmt.Errorf("error backing up config: %v", err)
		}
	}

	// Create file
	f, err := os.Create(configPath)
	if err != nil {
		return fmt.Errorf("error creating config file: %v", err)
	}
	defer f.Close()

	// Write header comment
	f.WriteString(fmt.Sprintf("# Meteorite configuration for %s (%s)\n",
		selection.Chain.PrettyName, selection.Chain.ChainName))
	f.WriteString("# Generated from the Cosmos Chain Registry\n\n")

	// Format the RPC endpoints array for TOML
	rpcs := config["nodes"].(map[string]interface{})["rpc"].([]string)
	rpcStr := "["
	for i, rpc := range rpcs {
		rpcStr += fmt.Sprintf(`"%s"`, rpc)
		if i < len(rpcs)-1 {
			rpcStr += ", "
		}
	}
	rpcStr += "]"

	// Format config as TOML manually to ensure proper formatting
	tomlStr := ""
	for k, v := range config {
		if k == "nodes" || k == "gas" || k == "msg_params" {
			continue // Handle these separately
		}

		switch val := v.(type) {
		case string:
			tomlStr += fmt.Sprintf("%s = \"%s\"\n", k, val)
		case bool:
			tomlStr += fmt.Sprintf("%s = %t\n", k, val)
		case int, int64, uint, uint64, float64:
			tomlStr += fmt.Sprintf("%s = %v\n", k, val)
		default:
			tomlStr += fmt.Sprintf("# Skipping %s: unknown type\n", k)
		}
	}

	// Add gas config
	gasConfig := config["gas"].(map[string]interface{})
	tomlStr += "\n[gas]\n"
	for k, v := range gasConfig {
		tomlStr += fmt.Sprintf("%s = %v\n", k, v)
	}

	// Add nodes config
	nodesConfig := config["nodes"].(map[string]interface{})
	tomlStr += "\n[nodes]\n"
	tomlStr += fmt.Sprintf("rpc = %s\n", rpcStr)
	tomlStr += fmt.Sprintf("api = \"%s\"\n", nodesConfig["api"])
	tomlStr += fmt.Sprintf("grpc = \"%s\"\n", nodesConfig["grpc"])

	// Add msg_params config
	msgParams := config["msg_params"].(map[string]interface{})
	tomlStr += "\n[msg_params]\n"
	for k, v := range msgParams {
		switch val := v.(type) {
		case string:
			tomlStr += fmt.Sprintf("%s = \"%s\"\n", k, val)
		default:
			tomlStr += fmt.Sprintf("%s = %v\n", k, val)
		}
	}

	// Write to file
	_, err = f.WriteString(tomlStr)
	if err != nil {
		return fmt.Errorf("error writing config: %v", err)
	}

	fmt.Printf("\nConfiguration saved to %s\n", configPath)
	fmt.Println("\nTo run tests with this configuration:")
	fmt.Printf("1. Ensure you have a seedphrase file in the same directory as the nodes.toml\n")
	fmt.Printf("2. Run: cd %s && meteorite\n", chainDir)
	fmt.Println("\nEach test will send different multisend transactions to different RPC endpoints,")
	fmt.Println("creating unique mempools across the network.")

	return nil
}
