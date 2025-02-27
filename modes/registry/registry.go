package registry

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"log"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/somatic-labs/meteorite/broadcast"
	"github.com/somatic-labs/meteorite/lib"
	"github.com/somatic-labs/meteorite/lib/chainregistry"
	bankmodule "github.com/somatic-labs/meteorite/modules/bank"
	"github.com/somatic-labs/meteorite/types"
)

const (
	BatchSize       = 100000000
	TimeoutDuration = 50 * time.Millisecond
)

// RunRegistryMode runs the registry mode
func RunRegistryMode() error {
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║     🔥 METEORITE CHAIN TESTER 🔥     ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println("Test any Cosmos chain directly from the Chain Registry")

	// Create a new registry client
	registry := chainregistry.NewRegistry("")

	// Download the registry
	fmt.Println("\n📥 Downloading the Cosmos Chain Registry...")
	err := registry.Download()
	if err != nil {
		return fmt.Errorf("error downloading chain registry: %v", err)
	}

	// Load chains
	fmt.Println("🔄 Loading chains from registry...")
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

	// Ask if the user wants to run the test immediately
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("\n🚀 Do you want to:")
	fmt.Println("  1. Run the test immediately")
	fmt.Println("  2. Save configuration to file and exit")

	for {
		fmt.Print("\nEnter your choice (1 or 2): ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("error reading input: %w", err)
		}

		input = strings.TrimSpace(input)
		switch input {
		case "1":
			// Run the test immediately
			fmt.Println("\n🚀 Running chain test...")
			return runChainTest(selection, config)
		case "2":
			// Save configuration to file
			return saveConfigToFile(selection, config)
		default:
			fmt.Println("Invalid choice. Please enter 1 or 2.")
		}
	}
}

// saveConfigToFile saves the configuration to a TOML file
func saveConfigToFile(selection *chainregistry.ChainSelection, config map[string]interface{}) error {
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
		return fmt.Errorf("seedphrase file not found in current directory")
	}

	// Convert map to types.Config
	config, err := mapToConfig(configMap)
	if err != nil {
		return fmt.Errorf("error converting config: %v", err)
	}

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
		fmt.Println("\n📊 Initializing transaction visualizer...")
		if err := broadcast.InitVisualizer(config.Nodes.RPC); err != nil {
			log.Printf("Warning: Failed to initialize visualizer: %v", err)
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
func mapToConfig(configMap map[string]interface{}) (types.Config, error) {
	var config types.Config

	// Set basic fields
	config.Chain = configMap["chain"].(string)
	config.Denom = configMap["denom"].(string)
	config.Prefix = configMap["prefix"].(string)
	config.Positions = configMap["positions"].(uint)
	config.GasPerByte = configMap["gas_per_byte"].(int64)
	config.BaseGas = configMap["base_gas"].(int64)
	config.MsgType = configMap["msg_type"].(string)
	config.Multisend = configMap["multisend"].(bool)
	config.NumMultisend = configMap["num_multisend"].(int)
	config.BroadcastMode = configMap["broadcast_mode"].(string)

	// Set gas config
	gasMap := configMap["gas"].(map[string]interface{})
	config.Gas.Low = gasMap["low"].(int64)
	config.Gas.Precision = gasMap["precision"].(int64)

	// Set nodes config
	nodesMap := configMap["nodes"].(map[string]interface{})
	rpcSlice := nodesMap["rpc"].([]string)
	config.Nodes.RPC = rpcSlice
	config.Nodes.API = nodesMap["api"].(string)
	config.Nodes.GRPC = nodesMap["grpc"].(string)

	// Set msg params
	msgParamsMap := configMap["msg_params"].(map[string]interface{})
	config.MsgParams.ToAddress = msgParamsMap["to_address"].(string)
	config.MsgParams.Amount = msgParamsMap["amount"].(int64)

	return config, nil
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
	// Get balances and ensure they are within 10% of each other
	balances, err := lib.GetBalances(accounts, config)
	if err != nil {
		return fmt.Errorf("failed to get balances: %v", err)
	}

	fmt.Println("balances", balances)

	if !lib.CheckBalancesWithinThreshold(balances, 0.10) {
		fmt.Println("⚠️ Account balances are not within 10% of each other. Adjusting balances...")
		if err := handleBalanceAdjustment(accounts, balances, config); err != nil {
			return err
		}
	}

	return nil
}

// handleBalanceAdjustment handles the balance adjustment between accounts
func handleBalanceAdjustment(accounts []types.Account, balances map[string]sdkmath.Int, config types.Config) error {
	if err := adjustBalances(accounts, balances, config); err != nil {
		return fmt.Errorf("failed to adjust balances: %v", err)
	}

	balances, err := lib.GetBalances(accounts, config)
	if err != nil {
		return fmt.Errorf("failed to get balances after adjustment: %v", err)
	}

	if !shouldProceedWithBalances(balances) {
		return fmt.Errorf("account balances are still not within threshold after adjustment")
	}

	return nil
}

// adjustBalances transfers funds between accounts to balance their balances within the threshold
func adjustBalances(accounts []types.Account, balances map[string]sdkmath.Int, config types.Config) error {
	// Implementation from main.go
	// This function would need to be copied from main.go
	return nil
}

// shouldProceedWithBalances checks if the balances are acceptable to proceed
func shouldProceedWithBalances(balances map[string]sdkmath.Int) bool {
	if lib.CheckBalancesWithinThreshold(balances, 0.15) {
		fmt.Println("✅ Balances successfully adjusted within acceptable range")
		return true
	}

	var maxBalance sdkmath.Int
	for _, balance := range balances {
		if balance.GT(maxBalance) {
			maxBalance = balance
		}
	}

	minSignificantBalance := sdkmath.NewInt(1000000)
	if maxBalance.LT(minSignificantBalance) {
		fmt.Println("✅ Remaining balance differences are below minimum threshold, proceeding")
		return true
	}

	return false
}

// initializeDistributor initializes the MultiSendDistributor if needed
func initializeDistributor(config types.Config, enableViz bool) *bankmodule.MultiSendDistributor {
	var distributor *bankmodule.MultiSendDistributor

	// Create a multisend distributor if needed
	if config.MsgType == "bank_multisend" && config.Multisend {
		// Initialize the distributor with RPC endpoints from config
		distributor = bankmodule.NewMultiSendDistributor(config, config.Nodes.RPC)
		fmt.Printf("📡 Initialized MultiSendDistributor with %d RPC endpoints\n", len(config.Nodes.RPC))

		if enableViz {
			broadcast.LogVisualizerDebug(fmt.Sprintf("Initialized MultiSendDistributor with %d RPC endpoints",
				len(config.Nodes.RPC)))
		}

		// Start a background goroutine to refresh endpoints periodically
		go func() {
			for {
				time.Sleep(15 * time.Minute)
				distributor.RefreshEndpoints()
			}
		}()
	}

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
	// Get account info
	sequence, accNum, err := lib.GetAccountInfo(acct.Address, config)
	if err != nil {
		log.Printf("Failed to get account info for %s: %v", acct.Address, err)
		return
	}

	// Prepare transaction parameters
	txParams := prepareTransactionParams(acct, config, chainID, sequence, accNum, distributor)

	// Log the start of processing for this account
	if enableViz {
		broadcast.LogVisualizerDebug(fmt.Sprintf("Starting transaction broadcasts for account %s (Position %d)",
			acct.Address, acct.Position))
	}

	// Broadcast transactions
	successfulTxs, failedTxs, responseCodes, _ := broadcast.Loop(txParams, BatchSize, int(acct.Position))

	// Print results
	printResults(acct.Address, successfulTxs, failedTxs, responseCodes)
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
	// Use the distributor to get the next RPC endpoint if available
	var nodeURL string
	var txMsgType string // Determine the message type based on availability of distributor

	if distributor != nil {
		nodeURL = distributor.GetNextRPC()
		if nodeURL == "" {
			nodeURL = config.Nodes.RPC[0] // Fallback
		}

		// Use bank_multisend when distributor is available and multisend is enabled
		if config.MsgType == "bank_send" && config.Multisend {
			txMsgType = "bank_multisend" // Use our special distributed multisend
		} else {
			txMsgType = config.MsgType
		}
	} else {
		nodeURL = config.Nodes.RPC[0] // Default to first RPC
		txMsgType = config.MsgType
	}

	return types.TransactionParams{
		Config:      config,
		NodeURL:     nodeURL,
		ChainID:     chainID,
		Sequence:    sequence,
		AccNum:      accNum,
		PrivKey:     acct.PrivKey,
		PubKey:      acct.PubKey,
		AcctAddress: acct.Address,
		MsgType:     txMsgType,
		MsgParams:   config.MsgParams,
		Distributor: distributor, // Pass distributor for multisend operations
	}
}

// printResults prints the results of transaction broadcasting
func printResults(address string, successfulTxs, failedTxs int, responseCodes map[uint32]int) {
	fmt.Printf("Account %s: Successful transactions: %d, Failed transactions: %d\n",
		address, successfulTxs, failedTxs)

	fmt.Println("Response code breakdown:")
	for code, count := range responseCodes {
		percentage := float64(count) / float64(successfulTxs+failedTxs) * 100
		fmt.Printf("Code %d: %d (%.2f%%)\n", code, count, percentage)
	}
}

// cleanupResources cleans up resources used by the program
func cleanupResources(distributor *bankmodule.MultiSendDistributor, enableViz bool) {
	fmt.Println("✅ All transactions completed. Cleaning up resources...")
	if distributor != nil {
		distributor.Cleanup()
	}

	// Stop the visualizer
	if enableViz {
		broadcast.StopVisualizer()
	}
}
