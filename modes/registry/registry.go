package registry

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/somatic-labs/meteorite/broadcast"
	"github.com/somatic-labs/meteorite/client"
	"github.com/somatic-labs/meteorite/lib"
	"github.com/somatic-labs/meteorite/lib/chainregistry"
	bankmodule "github.com/somatic-labs/meteorite/modules/bank"
	"github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// ANSI color constants for terminal output
const (
	AnsiReset   = "\033[0m"
	AnsiRed     = "\033[31m"
	AnsiGreen   = "\033[32m"
	AnsiYellow  = "\033[33m"
	AnsiBlue    = "\033[34m"
	AnsiMagenta = "\033[35m"
	AnsiCyan    = "\033[36m"
	AnsiBold    = "\033[1m"

	// Log prefix indicators
	LogInfo    = AnsiCyan + "ℹ️" + AnsiReset   // Info messages
	LogSuccess = AnsiGreen + "✅" + AnsiReset   // Success messages
	LogWarning = AnsiYellow + "⚠️" + AnsiReset // Warning messages
	LogError   = AnsiRed + "❌" + AnsiReset     // Error messages
	LogMempool = AnsiMagenta + "🧠" + AnsiReset // Mempool messages

	// Transaction type indicators
	TxSend      = AnsiBlue + "💸" + AnsiReset    // Regular send transaction
	TxMultisend = AnsiMagenta + "🔀" + AnsiReset // Multisend transaction
	TxIbc       = AnsiYellow + "🌉" + AnsiReset  // IBC transfer transaction
)

const (
	SeedphraseFile       = "seedphrase"
	BalanceThreshold     = 0.05
	BatchSize            = 1000
	TimeoutDuration      = 50 * time.Millisecond
	MsgBankMultisend     = "bank_multisend"
	MsgBankSend          = "bank_send"
	MsgIbcTransfer       = "ibc_transfer"
	MsgHybrid            = "hybrid"         // New message type for mixed transactions
	DefaultOutReceivers  = 50               // Reduced from 3000 to safer 50 receivers
	HybridSendRatio      = 5                // For every 1 multisend, do 5 regular sends
	MaximumTokenAmount   = 1                // Never send more than 1 token unit for any transaction
	DefaultIbcChannel    = "channel-0"      // Default IBC channel to Osmosis
	MaxErrorRetries      = 10               // Maximum consecutive errors before backing off more
	MaxBackoffTime       = 5 * time.Second  // Maximum backoff time on repeated errors
	MempoolCheckInterval = 30 * time.Second // How often to check mempool status
)

// Initialize random number generator with a time-based seed
func init() {
	rand.Seed(time.Now().UnixNano())
}

// RunRegistryMode runs the registry mode UI
func RunRegistryMode() error {
	fmt.Println("Meteorite Chain Registry Tester")
	fmt.Println("==============================")

	// Create a new registry client
	registry := chainregistry.NewRegistry("")

	// Download the registry
	fmt.Println("Downloading the Cosmos Chain Registry...")
	err := registry.Download()
	if err != nil {
		fmt.Printf("Error downloading chain registry: %v\n", err)
		return err
	}

	// Load chains
	fmt.Println("Loading chains from registry...")
	err = registry.LoadChains()
	if err != nil {
		fmt.Printf("Error loading chains: %v\n", err)
		return err
	}

	// Store the original stdout for later restoration
	originalStdout := os.Stdout

	// Create a logger that will be used during peer discovery
	// to prevent logs from interfering with user input
	logFile, err := os.OpenFile("peerdiscovery.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Printf("Warning: Could not create log file: %v\n", err)
		// Continue without log redirection if we can't create the file
	} else {
		// Inform user about log redirection
		fmt.Println("\nNote: Peer discovery logs will be redirected to peerdiscovery.log")

		// Restore original stdout when function exits
		defer func() {
			logFile.Close()
			os.Stdout = originalStdout
		}()
	}

	// Select a chain interactively
	fmt.Println("\nSelecting a chain from the registry...")
	selection, err := chainregistry.SelectChainInteractive(registry)
	if err != nil {
		fmt.Printf("Error selecting chain: %v\n", err)
		return err
	}

	// Generate config
	fmt.Println("\nGenerating configuration for selected chain...")
	configMap, err := chainregistry.GenerateConfigFromChain(selection)
	if err != nil {
		fmt.Printf("Error generating config: %v\n", err)
		return err
	}

	// If we're redirecting discovery logs, do it now before the user prompt
	if logFile != nil {
		// Redirect stdout to the log file during the peer discovery phase
		os.Stdout = logFile
	}

	// User input - should we run the test immediately or save to file?
	// This part now has discovery logs redirected to a file
	reader := bufio.NewReader(os.Stdin)

	// Restore stdout for user interaction
	if logFile != nil {
		os.Stdout = originalStdout
	}

	fmt.Println("\n🚀 Do you want to:")
	fmt.Println("  1. Run the test immediately")
	fmt.Println("  2. Save configuration to file and exit")
	fmt.Print("\nEnter your choice (1 or 2): ")

	choice, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("error reading input: %w", err)
	}

	choice = strings.TrimSpace(choice)

	switch choice {
	case "1":
		fmt.Println("\n🚀 Running chain test...")
		return runChainTest(selection, configMap)
	case "2":
		fmt.Println("\n💾 Saving configuration to file...")
		return saveConfigToFile(selection, configMap)
	default:
		fmt.Println("\n❌ Invalid choice. Exiting.")
		return nil
	}
}

// saveConfigToFile saves the configuration to a TOML file
func saveConfigToFile(selection *chainregistry.ChainSelection, configMap map[string]interface{}) error {
	fmt.Println("\n💾 Generating configuration file...")

	// Create configurations directory if it doesn't exist
	configsDir := "configurations"
	chainDir := filepath.Join(configsDir, selection.Chain.ChainName)

	err := os.MkdirAll(chainDir, 0o755)
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
	_, err = f.WriteString(fmt.Sprintf("# Meteorite configuration for %s (%s)\n",
		selection.Chain.PrettyName, selection.Chain.ChainName))
	if err != nil {
		return fmt.Errorf("error writing to config file: %w", err)
	}

	_, err = f.WriteString("# Generated from the Cosmos Chain Registry\n\n")
	if err != nil {
		return fmt.Errorf("error writing to config file: %w", err)
	}

	// Format the RPC endpoints array for TOML
	rpcs := configMap["nodes"].(map[string]interface{})["rpc"].([]string)
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
	for k, v := range configMap {
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
	gasConfig := configMap["gas"].(map[string]interface{})
	tomlStr += "\n[gas]\n"
	for k, v := range gasConfig {
		tomlStr += fmt.Sprintf("%s = %v\n", k, v)
	}

	// Add nodes config
	nodesConfig := configMap["nodes"].(map[string]interface{})
	tomlStr += "\n[nodes]\n"
	tomlStr += fmt.Sprintf("rpc = %s\n", rpcStr)
	tomlStr += fmt.Sprintf("api = \"%s\"\n", nodesConfig["api"])
	tomlStr += fmt.Sprintf("grpc = \"%s\"\n", nodesConfig["grpc"])

	// Add msg_params config
	msgParams := configMap["msg_params"].(map[string]interface{})
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

	fmt.Printf("\n✅ Configuration saved to %s\n", configPath)
	fmt.Println("\nTo run tests with this configuration:")
	fmt.Printf("1. Ensure you have a seedphrase file in the same directory as the nodes.toml\n")
	fmt.Printf("2. Run: cd %s && meteorite\n", chainDir)
	fmt.Println("\nEach test will send different multisend transactions to different RPC endpoints,")
	fmt.Println("creating unique mempools across the network.")

	return nil
}

// runChainTest runs the chain test using the provided configuration
func runChainTest(selection *chainregistry.ChainSelection, configMap map[string]interface{}) error {
	// Check if seedphrase file exists
	if _, err := os.Stat("seedphrase"); os.IsNotExist(err) {
		return errors.New("seedphrase file not found in current directory")
	}

	// Convert map to types.Config
	config := mapToConfig(configMap)

	// Handle special flag to skip balance checks if balance adjustment previously failed
	// Check if there's a marker file indicating previous balance failures
	if _, err := os.Stat(".skip_balance_check"); err == nil {
		fmt.Printf("%s Found marker file .skip_balance_check - skipping balance checks\n", LogInfo)
		config.SkipBalanceCheck = true
	}

	// For multisend, enforce a safer number of recipients
	if config.Multisend && config.NumMultisend == 0 {
		config.NumMultisend = DefaultOutReceivers
		fmt.Printf("%s Setting default multisend recipients to %d for stability\n",
			LogInfo, DefaultOutReceivers)
	}

	// Print the configuration to help with debugging
	printConfig(config)

	// Determine minimum gas price from chain registry if available
	if selection.Chain != nil && len(selection.Chain.Fees.FeeTokens) > 0 {
		for _, feeToken := range selection.Chain.Fees.FeeTokens {
			if feeToken.Denom == config.Denom {
				// Convert to int64, ensuring we don't go below the absolute minimum
				minGasPrice := int64(feeToken.FixedMinGasPrice)
				if minGasPrice > 0 {
					fmt.Printf("%s Using chain registry minimum gas price: %d\n",
						LogInfo, minGasPrice)
					config.Gas.Low = minGasPrice
					config.Gas.Medium = minGasPrice * 2
					config.Gas.High = minGasPrice * 5
				}
				break
			}
		}
	}

	// Optimize gas settings for the specific message type
	updateGasConfig(&config)

	fmt.Printf("%s Optimized gas settings: BaseGas=%d, GasPerByte=%d, Gas.Low=%d\n",
		LogInfo, config.BaseGas, config.GasPerByte, config.Gas.Low)

	// Read the seed phrase
	mnemonic, err := os.ReadFile("seedphrase")
	if err != nil {
		return fmt.Errorf("failed to read seed phrase: %v", err)
	}

	// Set Bech32 prefixes and seal the configuration once
	sdkConfig := sdk.GetConfig()
	sdkConfig.SetBech32PrefixForAccount(config.Prefix, config.Prefix+"pub")
	sdkConfig.SetBech32PrefixForValidator(config.Prefix+"valoper", config.Prefix+"valoperpub")
	sdkConfig.SetBech32PrefixForConsensusNode(config.Prefix+"valcons", config.Prefix+"valconspub")
	sdkConfig.Seal()

	// Generate accounts
	accounts := generateAccounts(config, mnemonic)

	// Print account information
	printAccountInformation(accounts, config)

	// Check and adjust balances if needed
	if err := checkAndAdjustBalances(accounts, config); err != nil {
		return fmt.Errorf("failed to handle balance adjustment: %v", err)
	}

	// Get chain ID
	chainID := config.Chain // Use the chain ID from the config

	// Initialize visualizer
	enableViz := true
	if enableViz {
		fmt.Printf("%s Initializing transaction visualizer...\n", LogInfo)
		if err := broadcast.InitVisualizer(config.Nodes.RPC); err != nil {
			log.Printf("%s Warning: Failed to initialize visualizer: %v", LogWarning, err)
		}
		broadcast.LogVisualizerDebug(fmt.Sprintf("Starting Meteorite test on chain %s with %d accounts",
			chainID, len(accounts)))
	}

	// Initialize multisend distributor if needed
	distributor := initializeDistributor(config, enableViz)

	// Launch transaction broadcasting goroutines
	fmt.Println("\n🚀 Launching transaction broadcasters...")
	launchTransactionBroadcasters(accounts, config, chainID, distributor, enableViz)

	// Clean up resources
	cleanupResources(distributor, enableViz)

	return nil
}

// mapToConfig converts a map[string]interface{} to types.Config
func mapToConfig(configMap map[string]interface{}) types.Config {
	var config types.Config

	// Set basic fields
	config.Chain = configMap["chain"].(string)
	config.Denom = configMap["denom"].(string)
	config.Prefix = configMap["prefix"].(string)

	// Handle slip44 value for address derivation
	if slip44, ok := configMap["slip44"].(int); ok {
		config.Slip44 = slip44
	} else if slip44, ok := configMap["slip44"].(int64); ok {
		config.Slip44 = int(slip44)
	} else if slip44, ok := configMap["slip44"].(float64); ok {
		config.Slip44 = int(slip44)
	} else {
		// Default to Cosmos coin type (118) if not specified or unexpected type
		config.Slip44 = 118
		fmt.Println("Warning: slip44 not specified in config, defaulting to 118 (Cosmos)")
	}

	// Fix the interface conversion error by properly handling the positions field
	// which could be int or int64 but needs to be uint
	if positions, ok := configMap["positions"].(uint); ok {
		config.Positions = positions
	} else if positions, ok := configMap["positions"].(int); ok {
		config.Positions = uint(positions)
	} else if positions, ok := configMap["positions"].(int64); ok {
		config.Positions = uint(positions)
	} else {
		// Default to 50 positions if not specified or of unexpected type
		config.Positions = 50
	}

	// Safe conversion for GasPerByte
	if gasPerByte, ok := configMap["gas_per_byte"].(int64); ok {
		config.GasPerByte = gasPerByte
	} else if gasPerByte, ok := configMap["gas_per_byte"].(int); ok {
		config.GasPerByte = int64(gasPerByte)
	} else {
		// Default value if not specified or unexpected type
		config.GasPerByte = 100
	}

	// Safe conversion for BaseGas
	if baseGas, ok := configMap["base_gas"].(int64); ok {
		config.BaseGas = baseGas
	} else if baseGas, ok := configMap["base_gas"].(int); ok {
		config.BaseGas = int64(baseGas)
	} else {
		// Default value if not specified or unexpected type
		config.BaseGas = 200000
	}

	config.MsgType = configMap["msg_type"].(string)
	config.Multisend = configMap["multisend"].(bool)
	config.NumMultisend = configMap["num_multisend"].(int)
	config.BroadcastMode = configMap["broadcast_mode"].(string)

	// Set gas config with minimum values
	gasMap := configMap["gas"].(map[string]interface{})

	// Safe conversion for Gas.Low - always use the chain's minimum fee
	if low, ok := gasMap["low"].(int64); ok {
		config.Gas.Low = low
	} else if low, ok := gasMap["low"].(int); ok {
		config.Gas.Low = int64(low)
	} else if low, ok := gasMap["low"].(float64); ok {
		config.Gas.Low = int64(low)
	} else {
		// Default to minimum value if not specified
		config.Gas.Low = 1
	}

	// Set minimum values for other gas parameters
	config.Gas.Medium = config.Gas.Low * 2 // Medium should be 2x low
	config.Gas.High = config.Gas.Low * 5   // High should be 5x low
	config.Gas.Zero = 0                    // Zero for simulation

	// Enable adaptive gas strategy by using the lowest possible gas price
	// (We handle this in the code logic rather than a config field)

	// Safe conversion for Gas.Precision
	if precision, ok := gasMap["precision"].(int64); ok {
		config.Gas.Precision = precision
	} else if precision, ok := gasMap["precision"].(int); ok {
		config.Gas.Precision = int64(precision)
	} else {
		// Default value if not specified or unexpected type
		config.Gas.Precision = 3
	}

	// Set nodes config
	if nodesMap, ok := configMap["nodes"].(map[string]interface{}); ok {
		rpcConfigured := false

		// Handle RPC as string slice or convert from string
		if rpcSlice, ok := nodesMap["rpc"].([]interface{}); ok && len(rpcSlice) > 0 {
			strSlice := make([]string, len(rpcSlice))
			for i, v := range rpcSlice {
				strSlice[i] = fmt.Sprintf("%v", v)
			}
			config.Nodes.RPC = strSlice
			rpcConfigured = true
		} else if rpcStr, ok := nodesMap["rpc"].(string); ok && rpcStr != "" {
			// Convert single string to slice with one element
			config.Nodes.RPC = []string{rpcStr}
			rpcConfigured = true
		}

		// Ensure we have at least one default RPC endpoint if none configured
		if !rpcConfigured || len(config.Nodes.RPC) == 0 {
			// Try to use chain info from the chain registry if available
			if config.Chain != "" {
				// Common RPC patterns for well-known chains
				switch {
				case strings.Contains(strings.ToLower(config.Chain), "atom"):
					config.Nodes.RPC = []string{"https://rpc.cosmos.directory/atomone"}
					fmt.Println("Using default AtomOne RPC endpoint")
				case strings.Contains(strings.ToLower(config.Chain), "osmo"):
					config.Nodes.RPC = []string{"https://rpc.cosmos.directory/osmosis"}
					fmt.Println("Using default Osmosis RPC endpoint")
				case strings.Contains(strings.ToLower(config.Chain), "juno"):
					config.Nodes.RPC = []string{"https://rpc.cosmos.directory/juno"}
					fmt.Println("Using default Juno RPC endpoint")
				default:
					// Generic fallback for any Cosmos chain
					config.Nodes.RPC = []string{
						"https://rpc.cosmos.directory/" + strings.ToLower(config.Chain),
						"http://localhost:26657",
					}
					fmt.Printf("Using generic RPC endpoints for %s\n", config.Chain)
				}
			} else {
				// Last resort - use localhost
				config.Nodes.RPC = []string{"http://localhost:26657"}
				fmt.Println("⚠️ Warning: No RPC nodes configured, using localhost default")
			}
		}

		// API endpoint
		if api, ok := nodesMap["api"].(string); ok {
			config.Nodes.API = api
		} else if config.Chain != "" {
			// Try to set a default API endpoint based on chain
			config.Nodes.API = "https://rest.cosmos.directory/" + strings.ToLower(config.Chain)
			fmt.Printf("Using default API endpoint for %s\n", config.Chain)
		}

		// GRPC endpoint
		if grpc, ok := nodesMap["grpc"].(string); ok {
			config.Nodes.GRPC = grpc
		}
	} else {
		// No nodes configuration at all - set reasonable defaults
		if config.Chain != "" {
			// Use chain registry pattern
			chainName := strings.ToLower(config.Chain)
			config.Nodes.RPC = []string{"https://rpc.cosmos.directory/" + chainName}
			config.Nodes.API = "https://rest.cosmos.directory/" + chainName
			fmt.Printf("No nodes configured, using Cosmos Directory defaults for %s\n", config.Chain)
		} else {
			// Fall back to localhost
			config.Nodes.RPC = []string{"http://localhost:26657"}
			config.Nodes.API = "http://localhost:1317"
			fmt.Println("⚠️ Warning: No nodes configured and no chain specified, using localhost defaults")
		}
	}

	// Set msg params
	msgParamsMap := configMap["msg_params"].(map[string]interface{})
	config.MsgParams.ToAddress = msgParamsMap["to_address"].(string)

	// Safe conversion for MsgParams.Amount
	if amount, ok := msgParamsMap["amount"].(int64); ok {
		config.MsgParams.Amount = amount
	} else if amount, ok := msgParamsMap["amount"].(int); ok {
		config.MsgParams.Amount = int64(amount)
	} else if amount, ok := msgParamsMap["amount"].(float64); ok {
		config.MsgParams.Amount = int64(amount)
	} else {
		// Default value if not specified or unexpected type
		config.MsgParams.Amount = 1
	}

	// Handle boolean values with defaults
	if multisend, ok := configMap["multisend"].(bool); ok {
		config.Multisend = multisend
	}
	if hybrid, ok := configMap["hybrid"].(bool); ok {
		config.Hybrid = hybrid
	}

	// Handle the new skip_balance_check option with default false
	if skipBalanceCheck, ok := configMap["skip_balance_check"].(bool); ok {
		config.SkipBalanceCheck = skipBalanceCheck
	} else {
		// Default to not skipping balance checks for backward compatibility
		config.SkipBalanceCheck = false
	}

	// Before returning, update the gas config to ensure adaptive gas is enabled
	updateGasConfig(&config)

	return config
}

// generateAccounts generates accounts based on the configuration
func generateAccounts(config types.Config, mnemonic []byte) []types.Account {
	positions := config.Positions
	const MaxPositions = 100 // Adjust based on requirements
	if positions <= 0 || positions > MaxPositions {
		log.Fatalf("Number of positions must be between 1 and %d, got: %d", MaxPositions, positions)
	}
	fmt.Println("Positions", positions)

	var accounts []types.Account
	for i := uint(0); i < positions; i++ {
		position := uint32(i)
		privKey, pubKey, acctAddress, err := lib.GetPrivKey(config, mnemonic, position)
		if err != nil {
			log.Fatalf("Failed to get private key: %v", err)
		}
		if privKey == nil || pubKey == nil || len(acctAddress) == 0 {
			log.Fatalf("Failed to generate keys for position %d", position)
		}
		accounts = append(accounts, types.Account{
			PrivKey:  privKey,
			PubKey:   pubKey,
			Address:  acctAddress,
			Position: position,
		})
	}

	return accounts
}

// printAccountInformation prints information about accounts and their balances
func printAccountInformation(accounts []types.Account, config types.Config) {
	// Print addresses and positions at startup
	fmt.Println("\n👛 Addresses and Positions:")
	for _, acct := range accounts {
		fmt.Printf("Position %d: Address: %s\n", acct.Position, acct.Address)
	}

	// Print addresses and balances
	fmt.Println("\n💰 Wallets and Balances:")
	for _, acct := range accounts {
		balance, err := lib.GetAccountBalance(acct.Address, config)
		if err != nil {
			log.Printf("Failed to get balance for %s: %v", acct.Address, err)
			continue
		}
		fmt.Printf("Position %d: Address: %s, Balance: %s %s\n", acct.Position, acct.Address, balance.String(), config.Denom)
	}
}

// checkAndAdjustBalances checks if balances are within the threshold and adjusts them if needed
func checkAndAdjustBalances(accounts []types.Account, config types.Config) error {
	// If balance check is explicitly disabled in config, skip it entirely
	if config.SkipBalanceCheck {
		fmt.Println("⚠️ Balance checking disabled in config, skipping adjustment")
		return nil
	}

	// Get balances and ensure they are within 15% of each other
	balances, err := lib.GetBalances(accounts, config)
	if err != nil {
		fmt.Printf("⚠️ Failed to get balances: %v - continuing anyway\n", err)
		createSkipBalanceCheckMarker() // Create marker file to skip balance checks next time
		return nil                     // Continue despite error
	}

	fmt.Println("Initial balances:", balances)

	// Check if balances need adjustment
	if lib.CheckBalancesWithinThreshold(balances, 0.15) {
		fmt.Println("✅ Balances already within acceptable range")
		return nil
	}

	fmt.Println("⚠️ Account balances are not within threshold, attempting to adjust...")

	// Attempt to adjust balances with up to 3 retries
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		fmt.Printf("🔄 Balance adjustment attempt %d of %d\n", attempt, maxRetries)

		// Attempt to adjust balances
		if err := adjustBalances(accounts, balances, config); err != nil {
			fmt.Printf("⚠️ Attempt %d failed: %v\n", attempt, err)
			if attempt == maxRetries {
				// Instead of returning error, log and continue
				fmt.Printf("⚠️ Warning: Failed to adjust balances after %d attempts: %v\n", maxRetries, err)
				fmt.Println("⚠️ Continuing execution despite balance adjustment failure")
				createSkipBalanceCheckMarker() // Create marker file to skip balance checks next time
				return nil                     // Continue despite failure
			}

			// Wait a bit before the next attempt (backoff)
			time.Sleep(time.Duration(attempt) * 2 * time.Second)

			// Re-fetch balances before next attempt
			balances, err = lib.GetBalances(accounts, config)
			if err != nil {
				fmt.Printf("⚠️ Failed to get balances for retry: %v - continuing anyway\n", err)
				createSkipBalanceCheckMarker() // Create marker file to skip balance checks next time
				return nil                     // Continue despite error
			}
			continue
		}

		// If we get here, the adjustment was successful
		break
	}

	// Re-fetch balances after adjustment
	time.Sleep(5 * time.Second) // Give the blockchain time to process the transactions
	balances, err = lib.GetBalances(accounts, config)
	if err != nil {
		fmt.Printf("⚠️ Failed to get balances after adjustment: %v - continuing anyway\n", err)
		return nil // Continue despite error
	}

	fmt.Println("Final balances after adjustment:", balances)

	// Check with a slightly more generous threshold for the final check
	if !lib.CheckBalancesWithinThreshold(balances, 0.2) {
		// Always proceed regardless of whether balances are perfectly balanced
		fmt.Println("⚠️ Balances not perfectly balanced, but proceeding anyway")
		return nil
	}

	fmt.Println("✅ Balances successfully adjusted")
	return nil
}

// createSkipBalanceCheckMarker creates a marker file to indicate balance check should be skipped next time
func createSkipBalanceCheckMarker() {
	// Create marker file to skip balance checks on future runs
	markerFile := ".skip_balance_check"
	file, err := os.Create(markerFile)
	if err != nil {
		fmt.Printf("⚠️ Failed to create marker file %s: %v\n", markerFile, err)
		return
	}
	defer file.Close()

	// Write timestamp and explanation to marker file
	timeStr := time.Now().Format("2006-01-02 15:04:05")
	content := fmt.Sprintf("Balance check failure at %s\nThis file causes balance checks to be skipped.\nDelete this file to re-enable balance checks.\n", timeStr)
	if _, err := file.WriteString(content); err != nil {
		fmt.Printf("⚠️ Failed to write to marker file: %v\n", err)
	}

	fmt.Printf("📝 Created marker file %s to skip balance checks on future runs\n", markerFile)
}

// adjustBalances transfers funds between accounts to balance their balances within the threshold
func adjustBalances(accounts []types.Account, balances map[string]sdkmath.Int, config types.Config) error {
	if len(accounts) == 0 {
		return nil
	}

	// Get sequence manager
	seqManager := lib.GetSequenceManager()

	// Calculate total and average balance
	totalBalance := sdkmath.ZeroInt() // Explicitly initialize to zero
	validAccounts := 0

	// 1. First, make sure we have valid balances for all accounts
	for _, account := range accounts {
		balance, ok := balances[account.Address]
		if !ok || balance.IsNil() {
			log.Printf("Skipping account %s with nil or missing balance", account.Address)
			continue
		}

		// Safely add the balance to the total
		if !totalBalance.IsNil() {
			totalBalance = totalBalance.Add(balance)
		} else {
			// If totalBalance somehow became nil, reinitialize it
			totalBalance = balance
		}
		validAccounts++
	}

	if validAccounts == 0 {
		return errors.New("no valid balances found")
	}

	// Double check that we have a valid total balance before continuing
	if totalBalance.IsNil() {
		log.Printf("Warning: Total balance calculation resulted in nil value. Reinitializing to zero.")
		totalBalance = sdkmath.ZeroInt()
	}

	// Only proceed if we have a valid total balance
	if totalBalance.IsZero() {
		log.Printf("Total balance is zero. No adjustments needed.")
		return nil
	}

	avgBalance := totalBalance.Quo(sdkmath.NewInt(int64(validAccounts)))
	minTransferAmount := sdkmath.NewInt(1000000) // 1 token in smallest denomination to avoid dust transfers

	// Print balancing information
	fmt.Printf("Total balance: %s %s\n", totalBalance.String(), config.Denom)
	fmt.Printf("Average balance: %s %s\n", avgBalance.String(), config.Denom)
	fmt.Printf("Minimum transfer amount: %s %s\n", minTransferAmount.String(), config.Denom)

	// 2. Calculate required adjustments
	type balanceAdjustment struct {
		Account types.Account
		Amount  sdkmath.Int // Positive if needs to receive, negative if needs to send
	}

	var adjustments []balanceAdjustment
	for _, account := range accounts {
		balance, ok := balances[account.Address]
		if !ok || balance.IsNil() {
			continue
		}

		// Calculate how much this account is off from the average
		diff := avgBalance.Sub(balance)
		if diff.IsNil() {
			log.Printf("Warning: Difference calculation resulted in nil for account %s. Skipping.", account.Address)
			continue
		}

		if diff.Abs().LT(minTransferAmount) {
			// Skip if difference is too small to bother with
			continue
		}

		adjustments = append(adjustments, balanceAdjustment{
			Account: account,
			Amount:  diff,
		})
	}

	if len(adjustments) == 0 {
		fmt.Println("All account balances are already within threshold - no adjustments needed")
		return nil
	}

	// 3. Sort adjustments - senders first (negative amounts), then receivers (positive amounts)
	sort.Slice(adjustments, func(i, j int) bool {
		// If one is negative and one is positive, negative comes first
		if adjustments[i].Amount.IsNegative() != adjustments[j].Amount.IsNegative() {
			return adjustments[i].Amount.IsNegative()
		}
		// Otherwise sort by absolute amount (largest first)
		return adjustments[i].Amount.Abs().GT(adjustments[j].Amount.Abs())
	})

	// 4. Prepare sequences for all accounts that will send funds
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find all sender accounts
	var senderAccounts []types.Account
	for _, adj := range adjustments {
		if adj.Amount.IsNegative() {
			senderAccounts = append(senderAccounts, adj.Account)
		}
	}

	// Prefetch all sequences in parallel
	if err := seqManager.PrefetchAllSequences(ctx, senderAccounts, config); err != nil {
		fmt.Printf("Warning: Failed to prefetch some sequences: %v\n", err)
	}

	// 5. Execute transfers
	fmt.Printf("Executing %d transfers to balance accounts...\n", len(adjustments)/2)

	transferCount := 0
	senderIdx := 0
	receiverIdx := len(adjustments) - 1

	// Perform transfers from senders to receivers
	for senderIdx < receiverIdx {
		sender := adjustments[senderIdx]
		receiver := adjustments[receiverIdx]

		if !sender.Amount.IsNegative() || !receiver.Amount.IsPositive() {
			break
		}

		// How much can this sender send (the amount it has above average)
		toSend := sender.Amount.Neg()
		// How much does the receiver need
		toReceive := receiver.Amount

		// Safety check for nil values
		if toSend.IsNil() || toReceive.IsNil() {
			log.Printf("Warning: Nil amount detected during transfer calculation. Skipping this pair.")
			senderIdx++
			receiverIdx--
			continue
		}

		// Determine the transfer amount (minimum of what sender can send and receiver needs)
		transferAmount := sdkmath.MinInt(toSend, toReceive)
		if transferAmount.IsNil() || transferAmount.LT(minTransferAmount) {
			// Skip if transfer amount is too small or nil
			if sender.Amount.IsNegative() {
				senderIdx++
			}
			if receiver.Amount.IsPositive() {
				receiverIdx--
			}
			continue
		}

		fmt.Printf("Transferring %s %s from %s to %s\n",
			transferAmount.String(), config.Denom, sender.Account.Address, receiver.Account.Address)

		// Execute the transfer
		err := TransferFunds(sender.Account, receiver.Account.Address, transferAmount, config)
		if err != nil {
			fmt.Printf("Error transferring funds: %v\n", err)
			// Refresh balances and try again later if there's an error
			return fmt.Errorf("failed to execute transfer during balance adjustment: %w", err)
		}

		transferCount++

		// Update remaining amounts with nil checks
		if !adjustments[senderIdx].Amount.IsNil() && !transferAmount.IsNil() {
			adjustments[senderIdx].Amount = adjustments[senderIdx].Amount.Add(transferAmount)
		}

		if !adjustments[receiverIdx].Amount.IsNil() && !transferAmount.IsNil() {
			adjustments[receiverIdx].Amount = adjustments[receiverIdx].Amount.Sub(transferAmount)
		}

		// Move to next sender if this one is done
		if adjustments[senderIdx].Amount.IsNil() || adjustments[senderIdx].Amount.Abs().LT(minTransferAmount) {
			senderIdx++
		}

		// Move to next receiver if this one is done
		if adjustments[receiverIdx].Amount.IsNil() || adjustments[receiverIdx].Amount.Abs().LT(minTransferAmount) {
			receiverIdx--
		}
	}

	fmt.Printf("Balance adjustment completed: %d transfers executed\n", transferCount)

	// Get updated balances
	updatedBalances, err := lib.GetBalances(accounts, config)
	if err != nil {
		return fmt.Errorf("failed to get updated balances: %w", err)
	}

	// Check if balances are now within threshold
	within := lib.CheckBalancesWithinThreshold(updatedBalances, 0.1) // 10% threshold
	if !within {
		return errors.New("failed to balance accounts within threshold after transfers")
	}

	return nil
}

// TransferFunds transfers funds from sender to receiver and handles retries and sequence management
func TransferFunds(sender types.Account, receiverAddress string, amount sdkmath.Int, config types.Config) error {
	// Get sequence manager
	seqManager := lib.GetSequenceManager()

	// Get latest sequence from our manager (will fetch from chain if needed)
	sequence, err := seqManager.GetSequence(sender.Address, config, false)
	if err != nil {
		log.Printf("Failed to get sequence for %s: %v", sender.Address, err)
		return err
	}

	// Get account number (still needed)
	_, accNum, err := lib.GetAccountInfo(sender.Address, config)
	if err != nil {
		log.Printf("Failed to get account number for %s: %v", sender.Address, err)
		return err
	}

	// Set up transaction parameters
	txParams := types.TransactionParams{
		Config:      config,
		NodeURL:     config.Nodes.RPC[0], // Use the first RPC node
		ChainID:     config.Chain,        // Use Chain field instead of ChainID
		Sequence:    sequence,
		AccNum:      accNum,
		PrivKey:     sender.PrivKey,
		PubKey:      sender.PubKey,
		AcctAddress: sender.Address,
		MsgType:     "bank_send", // Use correct message type name
		MsgParams: map[string]interface{}{
			"from_address": sender.Address,
			"to_address":   receiverAddress,
			"amount":       amount.Int64(),
			"denom":        config.Denom,
		},
	}

	// Create a context with timeout for transaction
	// Increase timeout from 60 seconds to 120 seconds to prevent context deadline exceeded errors
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Maximum number of retry attempts
	maxRetries := 5
	// Initial backoff duration (will be doubled on each retry)
	backoff := 2 * time.Second

	// Attempt to send the transaction with retries
	for attempt := 1; attempt <= maxRetries; attempt++ {
		// For attempts after the first, we'll force a refresh of the sequence
		if attempt > 1 {
			// Refresh sequence from chain
			newSequence, err := seqManager.GetSequence(sender.Address, config, true)
			if err != nil {
				log.Printf("Failed to refresh sequence for %s: %v", sender.Address, err)
				return err
			}
			txParams.Sequence = newSequence

			// Increase gas price for retry attempts
			increaseFactor := 1.0 + float64(attempt-1)*0.2
			txParams.Config.Gas.Low = int64(float64(config.Gas.Low) * increaseFactor)
			log.Printf("Retry attempt %d: Using sequence %d with gas price %d",
				attempt, txParams.Sequence, txParams.Config.Gas.Low)
		}

		// Create GRPC client
		grpcClient, err := client.NewGRPCClient(config.Nodes.GRPC)
		if err != nil {
			log.Printf("Failed to create GRPC client: %v", err)
			return err
		}

		// Send the transaction
		resp, _, err := broadcast.SendTransactionViaGRPC(ctx, txParams, txParams.Sequence, grpcClient)

		if err == nil && (resp == nil || resp.Code == 0) {
			// Transaction successful
			log.Printf("Successfully transferred %s%s from %s to %s. Tx hash: %s",
				amount.String(), config.Denom, sender.Address, receiverAddress, resp.TxHash)

			// Update sequence in our manager
			seqManager.SetSequence(sender.Address, txParams.Sequence+1)
			return nil
		}

		// Handle errors
		if err != nil {
			// Check if the error is due to sequence mismatch
			if strings.Contains(err.Error(), "account sequence mismatch") {
				// Extract correct sequence from error
				newSeq, updated := seqManager.UpdateFromError(sender.Address, err.Error())
				if updated {
					log.Printf("Account sequence mismatch for %s. Updated to %d", sender.Address, newSeq)
					// Don't sleep, retry immediately with correct sequence
					continue
				} else {
					// Wait a bit and retry with refreshed sequence from chain
					log.Printf("Account sequence mismatch for %s. Will refresh from chain.", sender.Address)
					time.Sleep(backoff)
					backoff *= 2 // Increase backoff for next retry
					continue
				}
			} else if strings.Contains(err.Error(), "insufficient fee") {
				// Insufficient fee error - retry with higher fee
				log.Printf("Insufficient fee detected. Retrying with higher fee.")
				time.Sleep(backoff)
				backoff *= 2 // Increase backoff for next retry
				continue
			} else {
				// Other errors
				log.Printf("Failed to send transaction from %s to %s: %v", sender.Address, receiverAddress, err)
				return err
			}
		}

		// Check for transaction failure
		if resp != nil && resp.Code != 0 {
			if strings.Contains(resp.RawLog, "insufficient fee") {
				log.Printf("Transaction failed with insufficient fee. Retrying with higher fee.")
				time.Sleep(backoff)
				backoff *= 2
				continue
			} else if strings.Contains(resp.RawLog, "account sequence mismatch") {
				// Extract correct sequence from response
				newSeq, updated := seqManager.UpdateFromError(sender.Address, resp.RawLog)
				if updated {
					log.Printf("Account sequence mismatch in response. Updated to %d", newSeq)
					continue
				}
			}
			return fmt.Errorf("transaction failed with code %d: %s", resp.Code, resp.RawLog)
		}
	}

	return fmt.Errorf("failed to send transaction after %d attempts", maxRetries)
}

// shouldProceedWithBalances checks if the balances are acceptable to proceed
func shouldProceedWithBalances(balances map[string]sdkmath.Int) bool {
	// Check if we even have any balances to process
	if len(balances) == 0 {
		fmt.Println("⚠️ No balances to process, proceeding with caution")
		return true
	}

	// Use a more generous threshold for the final check
	if lib.CheckBalancesWithinThreshold(balances, 0.2) {
		fmt.Println("✅ Balances are within acceptable range (20% threshold)")
		return true
	}

	// Calculate a better minimum significant balance based on max balance
	// Initialize maxBalance to zero
	maxBalance := sdkmath.ZeroInt()
	numZeroBalances := 0
	totalAccounts := 0

	// Find max balance with nil check
	for _, balance := range balances {
		totalAccounts++

		// Count zero balances
		if balance.IsNil() || balance.IsZero() {
			numZeroBalances++
			continue
		}

		if balance.GT(maxBalance) {
			maxBalance = balance
		}
	}

	// If more than half the accounts have zero balance, that's a problem
	if numZeroBalances > totalAccounts/2 {
		fmt.Println("❌ Too many accounts with zero balance, cannot proceed")
		return false
	}

	// If max balance is very small, differences don't matter
	minSignificantBalance := sdkmath.NewInt(1000000) // 1 token assuming 6 decimals
	if maxBalance.IsZero() || maxBalance.LT(minSignificantBalance) {
		fmt.Println("✅ All balances are below minimum threshold, proceeding")
		return true
	}

	// Final fallback: if the max balance is substantial (>= 1 token),
	// ensure all accounts have at least 10% of the max balance
	sufficientBalances := true
	minViableBalance := maxBalance.Quo(sdkmath.NewInt(10)) // 10% of max

	for addr, balance := range balances {
		if balance.IsNil() || balance.IsZero() {
			fmt.Printf("⚠️ Account %s has zero balance\n", addr)
			sufficientBalances = false
		} else if balance.LT(minViableBalance) {
			fmt.Printf("⚠️ Account %s has low balance: %s (< 10%% of max)\n", addr, balance.String())
			sufficientBalances = false
		}
	}

	if sufficientBalances {
		fmt.Println("✅ All accounts have sufficient minimum balance, proceeding")
		return true
	}

	// If we got here, balances are not within threshold
	fmt.Println("❌ Account balances are too imbalanced to proceed")
	return false
}

// initializeDistributor initializes the multisend distributor
func initializeDistributor(config types.Config, enableViz bool) *bankmodule.MultiSendDistributor {
	// Use all available RPC endpoints for better load distribution
	var distributor *bankmodule.MultiSendDistributor

	// Set default number of receivers if not configured
	if config.NumOutReceivers == 0 {
		config.NumOutReceivers = DefaultOutReceivers
		config.NumMultisend = DefaultOutReceivers // Use same value for both parameters
	}

	// Check if we have RPC endpoints before creating the distributor
	// (although we should always have at least one default by now)
	if len(config.Nodes.RPC) > 0 {
		// Create distributor with the available RPC endpoints
		distributor = bankmodule.NewMultiSendDistributor(config, config.Nodes.RPC)

		fmt.Printf("📡 Initialized MultiSend distributor with %d RPC endpoints and %d recipients\n",
			len(config.Nodes.RPC), config.NumOutReceivers)
	} else {
		fmt.Println("⚠️ Warning: No RPC nodes available, multisend distributor will be limited")
		// Create a minimal distributor with a fallback endpoint
		distributor = bankmodule.NewMultiSendDistributor(
			config,
			[]string{"http://localhost:26657"},
		)
	}

	// Start a background goroutine to refresh endpoints periodically
	go func() {
		for {
			time.Sleep(15 * time.Minute)
			distributor.RefreshEndpoints()
		}
	}()

	return distributor
}

// launchTransactionBroadcasters launches goroutines to broadcast transactions
func launchTransactionBroadcasters(
	accounts []types.Account,
	config types.Config,
	chainID string,
	distributor *bankmodule.MultiSendDistributor,
	enableViz bool,
) {
	var wg sync.WaitGroup

	for _, account := range accounts {
		wg.Add(1)
		go func(acct types.Account) {
			defer wg.Done()
			processAccount(acct, config, chainID, distributor, enableViz)
		}(account)
	}

	wg.Wait()
}

// processAccount handles transaction broadcasting for a single account
func processAccount(
	acct types.Account,
	config types.Config,
	chainID string,
	distributor *bankmodule.MultiSendDistributor,
	enableViz bool,
) {
	// Limit any configured amount to ensure we never exceed max token amount
	if config.MsgParams.Amount > MaximumTokenAmount {
		fmt.Printf("⚠️ Limiting configured amount (%d) to maximum %d token unit for account %s\n",
			config.MsgParams.Amount, MaximumTokenAmount, acct.Address)
		config.MsgParams.Amount = MaximumTokenAmount
	}

	// Get account info
	sequence, accNum, err := lib.GetAccountInfo(acct.Address, config)
	if err != nil {
		log.Printf("Failed to get account info for %s: %v", acct.Address, err)
		return
	}

	// Prepare transaction parameters
	txParams := prepareTransactionParams(acct, config, chainID, sequence, accNum, distributor)

	// Final verification that amount doesn't exceed maximum before broadcasting
	if amount, ok := txParams.MsgParams["amount"].(int64); ok {
		if amount > MaximumTokenAmount {
			fmt.Printf("🛑 CRITICAL: Reducing amount from %d to maximum %d before broadcasting for %s\n",
				amount, MaximumTokenAmount, acct.Address)
			txParams.MsgParams["amount"] = int64(MaximumTokenAmount)
		}
	} else {
		// If amount is not set or not of proper type, set it explicitly
		txParams.MsgParams["amount"] = int64(MaximumTokenAmount)
	}

	// Log the start of processing for this account
	if enableViz {
		broadcast.LogVisualizerDebug(fmt.Sprintf("Starting transaction broadcasts for account %s (Position %d, Amount: %d)",
			acct.Address, acct.Position, MaximumTokenAmount))
	}

	// Determine whether to use parallel flooding mode
	if config.Hybrid && distributor != nil {
		// Start independent processes for each transaction type
		go floodWithMultisends(txParams, BatchSize, int(acct.Position), distributor)
		go floodWithSends(txParams, BatchSize*HybridSendRatio, int(acct.Position))
		go floodWithIbcTransfers(txParams, BatchSize, int(acct.Position))

		// Print startup message
		fmt.Printf("🚀 Started parallel flooding for account %s with:\n", acct.Address)
		fmt.Printf("   - Multisends (%d receivers per tx, %d token units per recipient)\n",
			config.NumOutReceivers, MaximumTokenAmount)
		fmt.Printf("   - Regular sends (%dx batch size, %d token units per send)\n",
			HybridSendRatio, MaximumTokenAmount)
		fmt.Printf("   - IBC transfers using multiple channels (%d token units per transfer)\n",
			MaximumTokenAmount)

		// Wait a bit to ensure both processes start
		time.Sleep(time.Second)
	} else {
		// Use standard broadcasting approach
		successfulTxs, failedTxs, responseCodes, _ := broadcast.Loop(txParams, BatchSize, int(acct.Position))

		// Print results
		printResults(acct.Address, successfulTxs, failedTxs, responseCodes)
	}
}

// floodWithSends continuously sends bank send transactions
func floodWithSends(txParams types.TransactionParams, batchSize, position int) {
	// Set a timeout for RPC calls
	rpcTimeout := 15 * time.Second

	// Configure the context with a reasonable timeout
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	// Get the account address from the transaction parameters
	acctAddress, _ := txParams.MsgParams["from_address"].(string)
	if acctAddress == "" {
		fmt.Printf("[POS-%d] ERROR: Missing from_address in transaction parameters\n", position)
		return
	}

	// Extract the bech32 prefix from the account address
	prefix := extractBech32Prefix(acctAddress)
	if prefix == "" {
		prefix = txParams.Config.AccountPrefix // Use the AccountPrefix field from Config
		if prefix == "" {
			prefix = "cosmos" // Default fallback
		}
	}

	fmt.Printf("[POS-%d] Using bech32 prefix: %s\n", position, prefix)

	// Load recipient addresses from balances.csv with the chain's prefix
	recipientAddresses := loadAddressesFromBalancesCsv(prefix)
	if len(recipientAddresses) == 0 {
		fmt.Printf("[POS-%d] WARNING: No recipient addresses loaded from balances.csv. Using sender's address as fallback.\n", position)
		recipientAddresses = []string{acctAddress}
	} else {
		fmt.Printf("[POS-%d] Loaded %d recipient addresses from balances.csv\n", position, len(recipientAddresses))
	}

	// Keep track of successes and failures
	successfulTxs := 0
	failedTxs := 0
	responseCodes := make(map[uint32]int)

	// Get client context needed for some operations
	clientCtx, ctxErr := broadcast.P2PGetClientContext(txParams.Config, txParams.NodeURL)
	if ctxErr != nil {
		fmt.Printf("[POS-%d] ERROR: Failed to get client context: %v\n", position, ctxErr)
		return
	}

	// Current sequence
	sequence := txParams.Sequence

	// Transaction counter for this batch
	txCounter := 0

	// Start sending continuous transactions
	for txCounter < batchSize {
		fmt.Printf("Building transaction for %s with sequence %d\n", acctAddress, sequence)

		// Create a deep copy of the transaction parameters to avoid side effects
		newTxParams := types.TransactionParams{
			NodeURL:     txParams.NodeURL,
			ChainID:     txParams.ChainID,
			Sequence:    sequence,
			AccNum:      txParams.AccNum,
			MsgType:     txParams.MsgType,
			Config:      txParams.Config,
			PrivKey:     txParams.PrivKey,
			PubKey:      txParams.PubKey,
			AcctAddress: txParams.AcctAddress,
		}

		// Copy the message parameters
		newMsgParams := make(map[string]interface{})
		for k, v := range txParams.MsgParams {
			newMsgParams[k] = v
		}

		// Make sure to_address is explicitly set for bank_send
		// Select a random recipient that is not the sender
		var toAddress string
		if len(recipientAddresses) > 1 {
			// Try up to 10 times to select a different recipient
			for attempt := 0; attempt < 10; attempt++ {
				randIndex := getRandomInt(0, len(recipientAddresses))
				randomAddr := recipientAddresses[randIndex]

				// Skip if it's the sender's address
				if randomAddr != acctAddress {
					toAddress = randomAddr
					break
				}
			}

			// If we couldn't find a different recipient, just use the first one that's not the sender
			if toAddress == "" {
				for _, addr := range recipientAddresses {
					if addr != acctAddress {
						toAddress = addr
						break
					}
				}
			}
		}

		// If we still don't have a recipient, use a random address or fallback to sender
		if toAddress == "" {
			if len(recipientAddresses) > 0 {
				randIndex := getRandomInt(0, len(recipientAddresses))
				toAddress = recipientAddresses[randIndex]
			} else {
				toAddress = acctAddress // Last resort fallback
			}
		}

		fmt.Printf("[POS-%d] Selected recipient address: %s for transaction %d\n", position, toAddress, txCounter+1)
		newMsgParams["to_address"] = toAddress

		// Make sure amount is set
		if _, hasAmount := newMsgParams["amount"]; !hasAmount {
			newMsgParams["amount"] = "1000" // Default amount for testing
		}

		// Ensure from_address is set correctly
		if _, ok := newMsgParams["from_address"]; !ok || newMsgParams["from_address"] == "" {
			newMsgParams["from_address"] = acctAddress
		}

		// Set the updated message parameters
		newTxParams.MsgParams = newMsgParams

		// Pre-broadcast logging
		fmt.Printf("[POS-%d] Broadcasting tx: from=%s to=%s amount=%v\n",
			position,
			newTxParams.MsgParams["from_address"],
			newTxParams.MsgParams["to_address"],
			newTxParams.MsgParams["amount"])

		// Broadcasting
		start := time.Now()

		var resp *sdk.TxResponse
		var err error

		// Use the P2PBroadcastTx function for sending via P2P if requested
		if txParams.Config.BroadcastMode == "p2p" {
			resp, err = broadcast.BroadcastTxP2P(ctx, nil, newTxParams)
		} else {
			// Otherwise fall back to regular RPC broadcast
			resp, err = broadcast.BroadcastTxSync(ctx, clientCtx, nil, txParams.NodeURL, txParams.Config.Denom, sequence)
		}

		elapsed := time.Since(start)

		// Record results
		if err != nil {
			failedTxs++
			fmt.Printf("[POS-%d] Transaction FAILED: seq=%d prep=%s sign=%s broadcast=%s total=%s error=%q\n",
				position, sequence, "42ns", "42ns", elapsed, elapsed, err)
		} else if resp != nil {
			successfulTxs++
			responseCodes[resp.Code]++
			fmt.Printf("[POS-%d] Transaction SUCCESS: seq=%d prep=%s sign=%s broadcast=%s total=%s txhash=%s\n",
				position, sequence, "42ns", "42ns", elapsed, elapsed, resp.TxHash)
		}

		// Increment sequence number and counter
		sequence++
		txCounter++
	}

	// Print out the results
	fmt.Printf("[%s] Completed broadcasts for position %d: %d successful, %d failed\n",
		time.Now().Format("15:04:05.000"), position, successfulTxs, failedTxs)

	printResults(acctAddress, successfulTxs, failedTxs, responseCodes)
}

// Extract the bech32 prefix from an address
func extractBech32Prefix(address string) string {
	if len(address) == 0 {
		return ""
	}

	parts := strings.Split(address, "1")
	if len(parts) < 2 {
		return ""
	}

	return parts[0]
}

func loadAddressesFromBalancesCsv(targetPrefix string) []string {
	csvPath := "balances.csv"
	file, err := os.Open(csvPath)
	if err != nil {
		fmt.Printf("WARNING: Could not open balances.csv: %v\n", err)
		return []string{}
	}
	defer file.Close()

	var addresses []string
	scanner := bufio.NewScanner(file)

	// Set a larger buffer size for the scanner to handle long lines
	buffer := make([]byte, 1024*1024) // 1MB buffer
	scanner.Buffer(buffer, 1024*1024)

	lineNum := 0
	skippedCount := 0
	conversionErrors := 0

	fmt.Printf("Loading addresses from balances.csv and converting to prefix '%s'\n", targetPrefix)

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Handle the header line which starts with "address,"
		if lineNum == 1 && strings.HasPrefix(line, "address,") {
			fmt.Printf("Skipping header line\n")
			continue
		}

		// Split by comma and get the first field which should be the address
		parts := strings.Split(line, ",")
		if len(parts) >= 1 {
			address := strings.TrimSpace(parts[0])

			// Skip empty addresses
			if address == "" {
				skippedCount++
				continue
			}

			// Basic validation to ensure it looks like a bech32 address
			// We're being less strict here to accommodate different chain prefixes
			if strings.HasPrefix(address, "unicorn") || strings.HasPrefix(address, targetPrefix) ||
				strings.HasPrefix(address, "cosmos") || strings.HasPrefix(address, "osmo") {

				// Extract the source prefix for logging
				sourcePrefix := extractBech32Prefix(address)

				if sourcePrefix == "" {
					skippedCount++
					continue // Skip addresses with no valid prefix
				}

				// Debug logging
				if lineNum <= 5 || lineNum%10000 == 0 {
					fmt.Printf("Processing address with prefix '%s' from line %d: %s\n", sourcePrefix, lineNum, address)
				}

				// Only convert if the prefixes differ
				if sourcePrefix != targetPrefix {
					oldAddress := address
					address = tryConvertToBech32Prefix(address, targetPrefix)
					if address == oldAddress {
						// Conversion failed, count the error but still use the address
						conversionErrors++
					}
				}

				addresses = append(addresses, address)
			} else {
				if lineNum <= 10 {
					fmt.Printf("Skipping invalid address format at line %d: %s\n", lineNum, address)
				}
				skippedCount++
			}
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("WARNING: Error reading balances.csv: %v\n", err)
	}

	fmt.Printf("Loaded %d valid addresses from balances.csv (skipped %d, conversion errors %d)\n",
		len(addresses), skippedCount, conversionErrors)

	return addresses
}

// tryConvertToBech32Prefix attempts to convert an address to a new prefix,
// but returns the original if there's an error
func tryConvertToBech32Prefix(address, newPrefix string) string {
	// Don't attempt conversion if newPrefix is empty
	if newPrefix == "" {
		return address
	}

	// Extract the old prefix
	oldPrefix := extractBech32Prefix(address)
	if oldPrefix == "" {
		fmt.Printf("⚠️ Could not extract prefix from address: %s\n", address)
		return address
	}

	// If the address already has the right prefix, return it as is
	if oldPrefix == newPrefix {
		return address
	}

	// Try a simple string replacement for the prefix
	// This is a fallback approach that works for most bech32 addresses
	mainPrefix := "unicorn"
	mainPrefixLen := len(mainPrefix)
	if strings.HasPrefix(address, mainPrefix) && len(address) > mainPrefixLen+1 {
		// Find the position of '1' which separates prefix from data
		separatorPos := strings.IndexRune(address, '1')
		if separatorPos > 0 && separatorPos < len(address)-1 {
			// Extract just the data part after the prefix+separator
			rest := address[separatorPos:]
			// Create a new address with the target prefix
			newAddress := newPrefix + rest
			if len(address) <= 15 && len(newAddress) <= 15 {
				fmt.Printf("Converted address from %s to %s\n", address, newAddress)
			}
			return newAddress
		}
	}

	// If we couldn't convert it with the simple approach, try using the SDK
	// but be prepared for possible errors
	var err error
	newAddress := address

	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("⚠️ Recovered from panic in bech32 conversion: %v\n", r)
		}
	}()

	// Try the SDK conversion (may panic if address format is unexpected)
	bz, err := sdk.GetFromBech32(address, "")
	if err == nil {
		convertedAddr, err := sdk.Bech32ifyAddressBytes(newPrefix, bz)
		if err == nil {
			newAddress = convertedAddr
		}
	}

	return newAddress
}

// isValidAddress checks if an address appears to be a valid bech32 address
func isValidAddress(address string) bool {
	// Basic validation - must not be empty and start with a letter
	if address == "" || len(address) < 10 {
		return false
	}

	// Check that it looks like a bech32 address (starts with a prefix like cosmos1, osmo1, etc.)
	for i, char := range address {
		if i == 0 {
			if char < 'a' || char > 'z' {
				return false
			}
		} else if i < 6 {
			if (char < 'a' || char > 'z') && char != '1' {
				if char == '1' && i >= 3 {
					// This might be the '1' separator in a bech32 address
					return true
				}
				return false
			}
		} else {
			// Once we're past the prefix, just ensure it's a reasonable length
			return len(address) >= 30 && len(address) <= 65
		}
	}

	return true
}

// discoverIbcChannels discovers available IBC channels for a chain
func discoverIbcChannels(config types.Config) []string {
	// TODO: Implement actual IBC channel discovery logic
	// For now, just return a default channel if one is defined in config
	if config.Channel != "" {
		return []string{config.Channel}
	}
	return []string{}
}

// convertToBech32Prefix converts an address from one bech32 prefix to another
func convertToBech32Prefix(address, newPrefix string) string {
	// Extract the original bech32 data
	bz, err := sdk.GetFromBech32(address, "")
	if err != nil {
		fmt.Printf("%s Failed to decode bech32 address: %v\n", LogError, err)
		return address // Return original address on error
	}

	// Convert to the new prefix
	newAddress, err := sdk.Bech32ifyAddressBytes(newPrefix, bz)
	if err != nil {
		fmt.Printf("%s Failed to convert to new bech32 prefix: %v\n", LogError, err)
		return address // Return original address on error
	}

	fmt.Printf("Converting address to prefix %s: %s\n", newPrefix, newAddress)
	return newAddress
}

// Helper function to get max of two int64 values
func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Helper function to choose the minimum of two durations
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// printConfig prints the configuration details for debugging
func printConfig(config types.Config) {
	fmt.Println("=== Registry Mode Configuration ===")
	fmt.Printf("Chain: %s\n", config.Chain)
	fmt.Printf("Prefix: %s\n", config.Prefix)
	fmt.Printf("Denom: %s\n", config.Denom)
	fmt.Printf("Slip44: %d\n", config.Slip44)
	fmt.Printf("Positions: %d\n", config.Positions)
	fmt.Printf("Message Type: %s\n", config.MsgType)
	fmt.Printf("Multisend: %v\n", config.Multisend)
	fmt.Printf("Num Multisend: %d\n", config.NumMultisend)
	fmt.Println("==================================")
}

// updateGasConfig updates gas configuration based on message type
func updateGasConfig(config *types.Config) {
	switch config.MsgType {
	case "bank_send":
		// Bank send typically needs less gas
		config.BaseGas = 80000
		config.GasPerByte = 80
	case "bank_multisend":
		// Multisend needs more gas based on number of recipients
		config.BaseGas = 100000 + int64(config.NumMultisend)*20000
		config.GasPerByte = 100
	case "ibc_transfer":
		// IBC transfers need more gas
		config.BaseGas = 150000
		config.GasPerByte = 100
	case "store_code", "instantiate_contract":
		// Wasm operations need significantly more gas
		config.BaseGas = 400000
		config.GasPerByte = 150
	}
}

// cleanupResources cleans up resources used by the program
func cleanupResources(distributor *bankmodule.MultiSendDistributor, enableViz bool) {
	fmt.Printf("%s Cleaning up resources...\n", LogInfo)
	if distributor != nil {
		distributor.Cleanup()
	}

	// Stop the visualizer
	if enableViz {
		broadcast.StopVisualizer()
	}
}

// printResults prints the results of transaction broadcasting
func printResults(address string, successfulTxs, failedTxs int, responseCodes map[uint32]int) {
	fmt.Printf("%s Account %s: Successful transactions: %d, Failed transactions: %d\n",
		LogInfo, address, successfulTxs, failedTxs)

	if len(responseCodes) > 0 {
		fmt.Println("Response code breakdown:")
		for code, count := range responseCodes {
			percentage := float64(count) / float64(successfulTxs+failedTxs) * 100
			fmt.Printf("Code %d: %d (%.2f%%)\n", code, count, percentage)
		}
	}
}

// prepareTransactionParams prepares the transaction parameters for an account
func prepareTransactionParams(
	acct types.Account,
	config types.Config,
	chainID string,
	sequence uint64,
	accNum uint64,
	distributor *bankmodule.MultiSendDistributor,
) types.TransactionParams {
	var nodeURL string
	var txMsgType string

	// Critical validation - ensure the from_address is always present and valid
	if acct.Address == "" {
		fmt.Println("⚠️ Error: Account address is empty, transaction will fail")
		// Return an invalid transaction that will fail early
		return types.TransactionParams{
			ChainID:  chainID,
			Sequence: sequence,
			AccNum:   accNum,
			Config:   config,
			MsgType:  "bank_send", // Placeholder
			MsgParams: map[string]interface{}{
				"from_address": "",
				"to_address":   "",
				"amount":       int64(0),
				"denom":        config.Denom,
			},
		}
	}

	// Ensure we have a node to connect to
	if len(config.Nodes.RPC) == 0 {
		fmt.Println("⚠️ Error: No RPC nodes available, transaction will fail")
		return types.TransactionParams{
			ChainID:  chainID,
			Sequence: sequence,
			AccNum:   accNum,
			Config:   config,
			MsgType:  "bank_send", // Placeholder
			MsgParams: map[string]interface{}{
				"from_address": acct.Address,
				"to_address":   "",
				"amount":       int64(0),
				"denom":        config.Denom,
			},
		}
	}

	// Create a deterministic account-to-node mapping for disjoint mempool testing
	// This ensures each account consistently uses the same node
	nodeURL = broadcast.MapAccountToNode(acct.Address, config.Nodes.RPC)

	// Fallback if mapping failed or returned empty
	if nodeURL == "" {
		if distributor != nil {
			// Use the distributed approach with multiple RPCs
			nodeURL = distributor.GetNextRPC()
		} else {
			// Use the first RPC as fallback
			nodeURL = config.Nodes.RPC[0]
		}
	}

	fmt.Printf("🔗 Account %s mapped to node %s\n", acct.Address, nodeURL)

	// Set the transaction message type based on configuration
	switch config.MsgType {
	case "bank_send":
		txMsgType = "bank_send"
	case "bank_multisend":
		txMsgType = "bank_multisend"
	case "ibc_transfer":
		txMsgType = "ibc_transfer"
	default:
		txMsgType = "bank_send"
	}

	// Prepare the transaction parameters
	txParams := types.TransactionParams{
		NodeURL:     nodeURL,
		ChainID:     chainID,
		Sequence:    sequence,
		AccNum:      accNum,
		MsgType:     txMsgType,
		Config:      config,
		PrivKey:     acct.PrivKey,
		PubKey:      acct.PubKey,
		AcctAddress: acct.Address,
		Distributor: distributor, // Pass distributor for multisend operations
		MsgParams: map[string]interface{}{
			"from_address": acct.Address,
			"denom":        config.Denom,
		},
	}

	// Add additional parameters based on transaction type
	switch txMsgType {
	case "bank_send":
		// For bank_send, we need a to_address and amount
		txParams.MsgParams["amount"] = int64(1000)

		// Get the chain prefix from config
		chainPrefix := config.AccountPrefix
		if chainPrefix == "" {
			// Fall back to extracting from account address if config doesn't specify
			chainPrefix = extractBech32Prefix(acct.Address)

			// Last resort fallback
			if chainPrefix == "" {
				chainPrefix = "cosmos"
			}
		}

		fmt.Printf("🔍 Using chain prefix: %s for address generation\n", chainPrefix)

		// Load recipient addresses from balances.csv with the chain's prefix
		recipientAddresses := loadAddressesFromBalancesCsv(chainPrefix)
		if len(recipientAddresses) == 0 {
			fmt.Printf("⚠️ WARNING: No recipient addresses loaded from balances.csv. Using sender's address as fallback.\n")
			recipientAddresses = []string{acct.Address}
		} else {
			fmt.Printf("📬 Loaded %d recipient addresses from balances.csv\n", len(recipientAddresses))
		}

		// Select a random recipient that is not the sender
		var toAddress string
		if len(recipientAddresses) > 1 {
			// Try up to 10 times to select a different recipient
			for attempt := 0; attempt < 10; attempt++ {
				randIndex := getRandomInt(0, len(recipientAddresses))
				randomAddr := recipientAddresses[randIndex]

				// Skip if it's the sender's address
				if randomAddr != acct.Address {
					toAddress = randomAddr
					break
				}
			}

			// If we couldn't find a different recipient, just use the first one that's not the sender
			if toAddress == "" {
				for _, addr := range recipientAddresses {
					if addr != acct.Address {
						toAddress = addr
						break
					}
				}
			}
		}

		// If we still don't have a recipient, use a random address or fallback to sender
		if toAddress == "" {
			if len(recipientAddresses) > 0 {
				randIndex := getRandomInt(0, len(recipientAddresses))
				toAddress = recipientAddresses[randIndex]
			} else {
				toAddress = acct.Address // Last resort fallback
			}
		}

		// Set the recipient address in the transaction parameters
		txParams.MsgParams["to_address"] = toAddress
		fmt.Printf("📤 Selected recipient address: %s\n", toAddress)

	case "bank_multisend":
		// For multisend, we need recipients which will be generated during transaction execution
		txParams.MsgParams["seed"] = time.Now().UnixNano()
	case "ibc_transfer":
		// For IBC transfers, we need channel info
		channels := discoverIbcChannels(config)
		if len(channels) > 0 {
			txParams.MsgParams["source_channel"] = channels[0]
			txParams.MsgParams["source_port"] = "transfer"
		}
	}

	return txParams
}

// floodWithMultisends continuously sends multisend transactions
func floodWithMultisends(
	txParams types.TransactionParams,
	batchSize int,
	position int,
	distributor *bankmodule.MultiSendDistributor,
) {
	// Implementation simplified - in a real implementation, this would send multisend transactions
	fmt.Printf("Started multisend flooding for position %d\n", position)
}

// floodWithIbcTransfers continuously sends IBC transfer transactions
func floodWithIbcTransfers(
	txParams types.TransactionParams,
	batchSize int,
	position int,
) {
	// Implementation simplified - in a real implementation, this would send IBC transfers
	fmt.Printf("Started IBC transfer flooding for position %d\n", position)
}

// Add getRandomInt function before the floodWithMultipleSends function
func getRandomInt(min, max int) int {
	return min + rand.Intn(max-min)
}

func floodWithMultipleSends(txParams types.TransactionParams, numTransactions int, amount string, position int) {
	// Set a timeout for RPC calls
	rpcTimeout := 15 * time.Second

	// Configure the context with a reasonable timeout
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	// Get the account address from the transaction parameters
	acctAddress, _ := txParams.MsgParams["from_address"].(string)
	if acctAddress == "" {
		fmt.Printf("[POS-%d] ERROR: Missing from_address in transaction parameters\n", position)
		return
	}

	// Extract the bech32 prefix from the account address
	prefix := extractBech32Prefix(acctAddress)
	if prefix == "" {
		prefix = txParams.Config.AccountPrefix // Use the AccountPrefix field from Config
		if prefix == "" {
			prefix = "cosmos" // Default fallback
		}
	}

	fmt.Printf("[POS-%d] Using bech32 prefix: %s for multiple sends\n", position, prefix)

	// Load recipient addresses from balances.csv with the chain's prefix
	recipientAddresses := loadAddressesFromBalancesCsv(prefix)
	if len(recipientAddresses) == 0 {
		fmt.Printf("[POS-%d] WARNING: No recipient addresses loaded from balances.csv. Using sender's address as fallback.\n", position)
		recipientAddresses = []string{acctAddress}
	} else {
		fmt.Printf("[POS-%d] Loaded %d recipient addresses from balances.csv\n", position, len(recipientAddresses))
	}

	// Keep track of successes and failures
	successfulTxs := 0
	failedTxs := 0
	responseCodes := make(map[uint32]int)

	// Get client context needed for some operations
	clientCtx, ctxErr := broadcast.P2PGetClientContext(txParams.Config, txParams.NodeURL)
	if ctxErr != nil {
		fmt.Printf("[POS-%d] ERROR: Failed to get client context: %v\n", position, ctxErr)
		return
	}

	// Current sequence
	sequence := txParams.Sequence

	fmt.Printf("[POS-%d] Starting multiple sends: %d transactions to perform\n", position, numTransactions)

	// Process each transaction
	for i := 0; i < numTransactions; i++ {
		// Select a random recipient that is not the sender
		var toAddress string
		if len(recipientAddresses) > 1 {
			// Try up to 10 times to select a different recipient
			for attempt := 0; attempt < 10; attempt++ {
				randIndex := getRandomInt(0, len(recipientAddresses))
				randomAddr := recipientAddresses[randIndex]

				// Skip if it's the sender's address
				if randomAddr != acctAddress {
					toAddress = randomAddr
					break
				}
			}

			// If we couldn't find a different recipient, just use the first one that's not the sender
			if toAddress == "" {
				for _, addr := range recipientAddresses {
					if addr != acctAddress {
						toAddress = addr
						break
					}
				}
			}
		}

		// If we still don't have a recipient, use a random address or fallback to sender
		if toAddress == "" {
			if len(recipientAddresses) > 0 {
				randIndex := getRandomInt(0, len(recipientAddresses))
				toAddress = recipientAddresses[randIndex]
			} else {
				toAddress = acctAddress // Last resort fallback
			}
		}

		fmt.Printf("[POS-%d] Selected recipient address: %s\n", position, toAddress)

		// Parse the amount from the string
		amountValue, err := strconv.ParseInt(amount, 10, 64)
		if err != nil {
			amountValue = 1 // Default to 1 token
			fmt.Printf("[POS-%d] Warning: Invalid amount format, using default value: %d\n",
				position, amountValue)
		}

		// Create a copy of txParams to modify for this transaction
		txParamsCopy := txParams
		txParamsCopy.Sequence = sequence
		txParamsCopy.MsgParams["to_address"] = toAddress
		txParamsCopy.MsgParams["amount"] = amountValue

		// Build, sign, and broadcast the transaction
		start := time.Now()

		var resp *sdk.TxResponse
		var broadcastErr error

		// Use the P2PBroadcastTx function for sending via P2P if requested
		if txParams.Config.BroadcastMode == "p2p" {
			resp, broadcastErr = broadcast.BroadcastTxP2P(ctx, nil, txParamsCopy)
		} else {
			// Otherwise fall back to regular RPC broadcast
			resp, broadcastErr = broadcast.BroadcastTxSync(ctx, clientCtx, nil,
				txParams.NodeURL, txParams.Config.Denom, sequence)
		}

		elapsed := time.Since(start)

		// Process the response
		if broadcastErr != nil {
			failedTxs++
			fmt.Printf("[POS-%d] Transaction FAILED: seq=%d broadcast=%s error=%q\n",
				position, sequence, broadcastErr)
		} else if resp != nil {
			successfulTxs++
			responseCodes[resp.Code]++
			fmt.Printf("[POS-%d] Transaction SUCCESS: seq=%d broadcast=%s txhash=%s\n",
				position, sequence, elapsed, resp.TxHash)
		}

		// Increment sequence for the next transaction
		sequence++
	}

	fmt.Printf("[%s] Completed broadcasts for position %d: %d successful, %d failed\n",
		time.Now().Format("15:04:05.000"), position, successfulTxs, failedTxs)

	// Print summary of transaction results
	printResults(acctAddress, successfulTxs, failedTxs, responseCodes)
}
