package registry

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
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

const (
	SeedphraseFile   = "seedphrase"
	BalanceThreshold = 0.05
	BatchSize        = 1000
	TimeoutDuration  = 50 * time.Millisecond
	MsgBankMultisend = "bank_multisend"
)

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

	// For multisend, always enforce 3000 recipients for optimal performance
	if config.Multisend {
		config.NumMultisend = 3000
		fmt.Println("Enforcing 3000 recipients per multisend transaction for optimal performance")
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
					fmt.Printf("Using chain registry minimum gas price: %d\n", minGasPrice)
					config.Gas.Low = minGasPrice
					config.Gas.Medium = minGasPrice * 2
					config.Gas.High = minGasPrice * 5
				}
				break
			}
		}
	}

	// Optimize gas settings for the specific message type
	switch config.MsgType {
	case "bank_send":
		// Bank send typically needs less gas
		config.BaseGas = 80000
		config.GasPerByte = 80
	case MsgBankMultisend:
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

	fmt.Printf("🔥 Optimized gas settings: BaseGas=%d, GasPerByte=%d, Gas.Low=%d\n",
		config.BaseGas, config.GasPerByte, config.Gas.Low)

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
	nodesMap := configMap["nodes"].(map[string]interface{})
	rpcSlice := nodesMap["rpc"].([]string)
	config.Nodes.RPC = rpcSlice
	config.Nodes.API = nodesMap["api"].(string)
	config.Nodes.GRPC = nodesMap["grpc"].(string)

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
	// Get balances and ensure they are within 10% of each other
	balances, err := lib.GetBalances(accounts, config)
	if err != nil {
		return fmt.Errorf("failed to get balances: %v", err)
	}

	fmt.Println("balances", balances)

	// Check if balances need adjustment
	if lib.CheckBalancesWithinThreshold(balances, 0.15) {
		fmt.Println("✅ Balances already within acceptable range")
		return nil
	}

	fmt.Println("⚠️ Account balances are not within threshold, attempting to adjust...")

	// Attempt to adjust balances
	if err := adjustBalances(accounts, balances, config); err != nil {
		return fmt.Errorf("failed to adjust balances: %v", err)
	}

	// Re-fetch balances after adjustment
	balances, err = lib.GetBalances(accounts, config)
	if err != nil {
		return fmt.Errorf("failed to get balances after adjustment: %v", err)
	}

	if !shouldProceedWithBalances(balances) {
		return errors.New("account balances are still not within threshold after adjustment")
	}

	return nil
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

	if lib.CheckBalancesWithinThreshold(balances, 0.15) {
		fmt.Println("✅ Balances successfully adjusted within acceptable range")
		return true
	}

	// Initialize maxBalance to zero
	maxBalance := sdkmath.ZeroInt()

	// Find max balance with nil check
	for _, balance := range balances {
		// Skip nil balances
		if balance.IsNil() {
			continue
		}

		if balance.GT(maxBalance) {
			maxBalance = balance
		}
	}

	minSignificantBalance := sdkmath.NewInt(1000000)
	// Handle the case where maxBalance might still be zero
	if maxBalance.IsZero() || maxBalance.LT(minSignificantBalance) {
		fmt.Println("✅ Remaining balance differences are below minimum threshold, proceeding")
		return true
	}

	return false
}

// initializeDistributor initializes the MultiSendDistributor if needed
func initializeDistributor(config types.Config, enableViz bool) *bankmodule.MultiSendDistributor {
	var distributor *bankmodule.MultiSendDistributor

	// Create a multisend distributor if multisend is enabled, regardless of initial message type
	// This allows the prepareTransactionParams function to switch to multisend mode
	if config.Multisend {
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

		// Use MsgBankMultisend when distributor is available and multisend is enabled
		if config.MsgType == "bank_send" && config.Multisend {
			txMsgType = MsgBankMultisend // Use our special distributed multisend
		} else {
			txMsgType = config.MsgType
		}
	} else {
		nodeURL = config.Nodes.RPC[0] // Default to first RPC
		txMsgType = config.MsgType
	}

	// Convert MsgParams struct to map
	msgParamsMap := types.ConvertMsgParamsToMap(config.MsgParams)

	// Explicitly set the from_address to the account's address
	// This ensures it's always set correctly even if not present in config.MsgParams
	msgParamsMap["from_address"] = acct.Address

	// Add distributor to msgParams for multisend operations
	if distributor != nil && txMsgType == MsgBankMultisend {
		msgParamsMap["distributor"] = distributor
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
		MsgParams:   msgParamsMap,
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

// Update the GasConfig when loading from a config map to ensure adaptive gas is enabled
func updateGasConfig(config *types.Config) {
	// Enable adaptive gas by default
	// This ensures we're always using the most efficient gas settings
	if config.Gas.Medium == 0 {
		config.Gas.Medium = config.Gas.Low * 2 // Medium should be 2x low
	}
	if config.Gas.High == 0 {
		config.Gas.High = config.Gas.Low * 5 // High should be 5x low
	}
	if config.Gas.Zero == 0 {
		config.Gas.Zero = 0 // Zero for simulation
	}

	// Set gas price denom if not already set
	if config.Gas.Denom == "" {
		config.Gas.Denom = config.Denom // Use the same denom as the main config
	}

	// Set default gas price if not already set
	if config.Gas.Price == "" {
		// Convert to string with precision
		precision := config.Gas.Precision
		if precision == 0 {
			precision = 6 // Default precision
		}

		divisor := float64(1)
		for i := int64(0); i < precision; i++ {
			divisor *= 10
		}

		priceValue := float64(config.Gas.Low) / divisor
		config.Gas.Price = fmt.Sprintf("%g", priceValue)
	}

	// Enable adaptive gas by default
	config.Gas.AdaptiveGas = true

	fmt.Printf("Gas optimization enabled: Using adaptive gas strategy with base price %s%s\n",
		config.Gas.Price, config.Gas.Denom)
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
