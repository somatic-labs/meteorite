package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/somatic-labs/meteorite/broadcast"
	"github.com/somatic-labs/meteorite/client"
	"github.com/somatic-labs/meteorite/lib"
	"github.com/somatic-labs/meteorite/modes/registry"
	bankmodule "github.com/somatic-labs/meteorite/modules/bank"
	"github.com/somatic-labs/meteorite/types"
	"github.com/spf13/viper"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	BatchSize                  = 100000000
	TimeoutDuration            = 50 * time.Millisecond
	DefaultMultisendRecipients = 3000 // Always use 3000 recipients per multisend
)

func main() {
	log.Println("Welcome to Meteorite - Transaction Scaling Framework for Cosmos SDK chains")

	// Initialize configuration
	viper.SetConfigFile("config.toml")
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Config file not found
			log.Println("No config file found. Using default configuration.")
		} else {
			// Config file was found but another error was produced
			log.Fatalf("Fatal error reading config file: %s", err)
		}
	}

	// Log gas optimization details
	log.Println("Gas optimization enabled - Using minimum required gas for transactions")

	// Parse command-line flags
	flags := parseCommandLineFlags()

	// Determine if we're using a config file or the registry
	if flags.useConfigFile {
		// Config file mode - load config from file
		runConfigFileMode(flags)
	} else {
		// Registry mode (default) - run the chain registry UI
		if err := registry.RunRegistryMode(); err != nil {
			log.Fatalf("Error in registry mode: %v", err)
		}
	}
}

// Flags holds the parsed command-line flags
type Flags struct {
	useConfigFile bool
	configFile    string
	enableViz     bool
}

// parseCommandLineFlags parses the command-line flags and returns a Flags struct
func parseCommandLineFlags() Flags {
	useConfigFile := flag.Bool("config", false, "Use a configuration file instead of the chain registry")
	configFile := flag.String("f", "nodes.toml", "Path to the configuration file (only used with -config)")
	enableViz := flag.Bool("viz", true, "Enable the transaction visualizer")

	// Parse flags
	flag.Parse()

	return Flags{
		useConfigFile: *useConfigFile,
		configFile:    *configFile,
		enableViz:     *enableViz,
	}
}

// runConfigFileMode runs the application in config file mode
func runConfigFileMode(flags Flags) {
	// Load configuration and setup environment
	config, accounts := setupEnvironment(flags.configFile)

	// Print account information
	printAccountInformation(accounts, config)

	// Check and adjust balances if needed
	if err := checkAndAdjustBalances(accounts, config); err != nil {
		log.Fatalf("Failed to handle balance adjustment: %v", err)
	}

	// Get chain ID
	nodeURL := config.Nodes.RPC[0] // Use the first node
	chainID, err := lib.GetChainID(nodeURL)
	if err != nil {
		log.Fatalf("Failed to get chain ID: %v", err)
	}

	// Initialize visualizer if enabled
	if flags.enableViz {
		initializeVisualizer(config, chainID, len(accounts))
	}

	// Initialize multisend distributor if needed
	distributor := initializeDistributor(config, flags.enableViz)

	// Launch transaction broadcasting goroutines
	launchTransactionBroadcasters(accounts, config, chainID, distributor, flags.enableViz)

	// Clean up resources
	cleanupResources(distributor, flags.enableViz)
}

// setupEnvironment loads the configuration and sets up the environment
func setupEnvironment(configFile string) (types.Config, []types.Account) {
	// Load config from file
	config := types.Config{}
	if _, err := toml.DecodeFile(configFile, &config); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Always enforce 3000 recipients per multisend when multisend is enabled
	if config.Multisend {
		if config.NumMultisend != DefaultMultisendRecipients {
			log.Printf("⚠️  Overriding NumMultisend from %d to %d for optimal performance",
				config.NumMultisend, DefaultMultisendRecipients)
			config.NumMultisend = DefaultMultisendRecipients
		}
	}

	mnemonic, err := os.ReadFile("seedphrase")
	if err != nil {
		log.Fatalf("Failed to read seed phrase: %v", err)
	}

	// Set Bech32 prefixes and seal the configuration once
	sdkConfig := sdk.GetConfig()
	sdkConfig.SetBech32PrefixForAccount(config.Prefix, config.Prefix+"pub")
	sdkConfig.SetBech32PrefixForValidator(config.Prefix+"valoper", config.Prefix+"valoperpub")
	sdkConfig.SetBech32PrefixForConsensusNode(config.Prefix+"valcons", config.Prefix+"valconspub")
	sdkConfig.Seal()

	// Validate and set positions
	accounts := generateAccounts(config, mnemonic)

	return config, accounts
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
	fmt.Println("Addresses and Positions:")
	for _, acct := range accounts {
		fmt.Printf("Position %d: Address: %s\n", acct.Position, acct.Address)
	}

	// Print addresses and balances
	fmt.Println("Wallets and Balances:")
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
		fmt.Println("Account balances are not within 10% of each other. Adjusting balances...")
		if err := handleBalanceAdjustment(accounts, balances, config); err != nil {
			return err
		}
	}

	return nil
}

// initializeVisualizer initializes the transaction visualizer
func initializeVisualizer(config types.Config, chainID string, numAccounts int) {
	fmt.Println("Initializing transaction visualizer...")
	if err := broadcast.InitVisualizer(config.Nodes.RPC); err != nil {
		log.Printf("Warning: Failed to initialize visualizer: %v", err)
	}
	broadcast.LogVisualizerDebug(fmt.Sprintf("Starting Meteorite test on chain %s with %d accounts",
		chainID, numAccounts))
}

// initializeDistributor initializes the MultiSendDistributor if needed
func initializeDistributor(config types.Config, enableViz bool) *bankmodule.MultiSendDistributor {
	var distributor *bankmodule.MultiSendDistributor

	// Create a multisend distributor if needed
	if config.MsgType == "bank_multisend" && config.Multisend {
		// Initialize the distributor with RPC endpoints from config
		distributor = bankmodule.NewMultiSendDistributor(config, config.Nodes.RPC)
		fmt.Printf("Initialized MultiSendDistributor with %d RPC endpoints\n", len(config.Nodes.RPC))

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
	var nodeURL string
	var txMsgType string

	if len(config.Nodes.RPC) > 0 {
		if distributor != nil {
			// Use the distributed approach with multiple RPCs
			nodeURL = distributor.GetNextRPC()
		} else {
			// Use the simple approach with the first RPC
			nodeURL = config.Nodes.RPC[0]
		}
	}

	// If no node URL is available, use a default
	if nodeURL == "" {
		nodeURL = "http://localhost:26657"
	}

	// Get message type - either from config or default to bank_send
	if config.MsgType != "" {
		txMsgType = config.MsgType
	} else {
		txMsgType = "bank_send"
	}

	// Convert MsgParams struct to map
	msgParamsMap := types.ConvertMsgParamsToMap(config.MsgParams)

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
	fmt.Println("All transactions completed. Cleaning up resources...")
	if distributor != nil {
		distributor.Cleanup()
	}

	// Stop the visualizer
	if enableViz {
		broadcast.StopVisualizer()
	}
}

// adjustBalances transfers funds between accounts to balance their balances within the threshold
func adjustBalances(accounts []types.Account, balances map[string]sdkmath.Int, config types.Config) error {
	if len(accounts) == 0 {
		return errors.New("no accounts provided for balance adjustment")
	}

	// Calculate the total balance
	totalBalance := sdkmath.ZeroInt()
	for _, balance := range balances {
		totalBalance = totalBalance.Add(balance)
	}
	fmt.Printf("Total Balance across all accounts: %s %s\n", totalBalance.String(), config.Denom)

	if totalBalance.IsZero() {
		return errors.New("total balance is zero, nothing to adjust")
	}

	numAccounts := sdkmath.NewInt(int64(len(accounts)))
	averageBalance := totalBalance.Quo(numAccounts)
	fmt.Printf("Number of Accounts: %d, Average Balance per account: %s %s\n", numAccounts.Int64(), averageBalance.String(), config.Denom)

	// Define minimum transfer amount to avoid dust transfers
	minTransfer := sdkmath.NewInt(1000000) // Adjust based on your token's decimal places
	fmt.Printf("Minimum Transfer Amount to avoid dust: %s %s\n", minTransfer.String(), config.Denom)

	// Create a slice to track balances that need to send or receive funds
	type balanceAdjustment struct {
		Account types.Account
		Amount  sdkmath.Int // Positive if needs to receive, negative if needs to send
	}
	var adjustments []balanceAdjustment

	threshold := averageBalance.MulRaw(10).QuoRaw(100) // threshold = averageBalance * 10 / 100
	fmt.Printf("Balance Threshold for adjustments (10%% of average balance): %s %s\n", threshold.String(), config.Denom)

	for _, acct := range accounts {
		currentBalance := balances[acct.Address]
		difference := averageBalance.Sub(currentBalance)

		fmt.Printf("Account %s - Current Balance: %s %s, Difference from average: %s %s\n",
			acct.Address, currentBalance.String(), config.Denom, difference.String(), config.Denom)

		// Only consider adjustments exceeding the threshold and minimum transfer amount
		if difference.Abs().GT(threshold) && difference.Abs().GT(minTransfer) {
			adjustments = append(adjustments, balanceAdjustment{
				Account: acct,
				Amount:  difference,
			})
			fmt.Printf("-> Account %s requires adjustment of %s %s\n", acct.Address, difference.String(), config.Denom)
		} else {
			fmt.Printf("-> Account %s is within balance threshold, no adjustment needed\n", acct.Address)
		}
	}

	// Separate adjustments into senders (negative amounts) and receivers (positive amounts)
	var senders, receivers []balanceAdjustment
	for _, adj := range adjustments {
		if adj.Amount.IsNegative() {
			// Check if the account has enough balance to send
			accountBalance := balances[adj.Account.Address]
			fmt.Printf("Sender Account %s - Balance: %s %s, Surplus: %s %s\n",
				adj.Account.Address, accountBalance.String(), config.Denom, adj.Amount.Abs().String(), config.Denom)

			if accountBalance.GT(sdkmath.ZeroInt()) {
				senders = append(senders, adj)
			} else {
				fmt.Printf("-> Account %s has zero balance, cannot send funds.\n", adj.Account.Address)
			}
		} else if adj.Amount.IsPositive() {
			fmt.Printf("Receiver Account %s - Needs: %s %s\n",
				adj.Account.Address, adj.Amount.String(), config.Denom)
			receivers = append(receivers, adj)
		}
	}

	// Perform transfers from senders to receivers
	for _, sender := range senders {
		// The total amount the sender needs to transfer (their surplus)
		amountToSend := sender.Amount.Abs()
		fmt.Printf("\nStarting transfers from Sender Account %s - Total Surplus to send: %s %s\n",
			sender.Account.Address, amountToSend.String(), config.Denom)

		// Iterate over the receivers who need funds
		for i := range receivers {
			receiver := &receivers[i]

			// Check if the receiver still needs funds
			if receiver.Amount.GT(sdkmath.ZeroInt()) {
				// Determine the amount to transfer:
				// It's the minimum of what the sender can send and what the receiver needs
				transferAmount := sdkmath.MinInt(amountToSend, receiver.Amount)

				fmt.Printf("Transferring %s %s from %s to %s\n",
					transferAmount.String(), config.Denom, sender.Account.Address, receiver.Account.Address)

				// Transfer funds from the sender to the receiver
				err := TransferFunds(sender.Account, receiver.Account.Address, transferAmount, config)
				if err != nil {
					return fmt.Errorf("failed to transfer funds from %s to %s: %v",
						sender.Account.Address, receiver.Account.Address, err)
				}

				fmt.Printf("-> Successfully transferred %s %s from %s to %s\n",
					transferAmount.String(), config.Denom, sender.Account.Address, receiver.Account.Address)

				// Update the sender's remaining amount to send
				amountToSend = amountToSend.Sub(transferAmount)
				fmt.Printf("Sender %s remaining surplus to send: %s %s\n",
					sender.Account.Address, amountToSend.String(), config.Denom)

				// Update the receiver's remaining amount to receive
				receiver.Amount = receiver.Amount.Sub(transferAmount)
				fmt.Printf("Receiver %s remaining amount needed: %s %s\n",
					receiver.Account.Address, receiver.Amount.String(), config.Denom)

				// If the sender has sent all their surplus, move to the next sender
				if amountToSend.IsZero() {
					fmt.Printf("Sender %s has sent all surplus funds.\n", sender.Account.Address)
					break
				}
			} else {
				fmt.Printf("Receiver %s no longer needs funds.\n", receiver.Account.Address)
			}
		}
	}

	fmt.Println("\nBalance adjustment complete.")
	return nil
}

func TransferFunds(sender types.Account, receiverAddress string, amount sdkmath.Int, config types.Config) error {
	// Create a transaction params struct for the funds transfer
	txParams := types.TransactionParams{
		Config:      config,
		NodeURL:     config.Nodes.RPC[0],
		ChainID:     config.Chain,
		PrivKey:     sender.PrivKey,
		PubKey:      sender.PubKey,
		AcctAddress: sender.Address,
		MsgType:     "bank_send",
		MsgParams: map[string]interface{}{
			"from_address": sender.Address,
			"to_address":   receiverAddress,
			"amount":       amount.Int64(),
			"denom":        config.Denom,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		fmt.Printf("Attempt %d to send transaction with sequence %d\n", attempt+1, txParams.Sequence)

		// Create GRPC client with proper error handling
		grpcClient, err := client.NewGRPCClient(config.Nodes.GRPC)
		if err != nil {
			fmt.Printf("Failed to create GRPC client: %v\n", err)
			continue
		}

		resp, _, err := broadcast.SendTransactionViaGRPC(ctx, txParams, txParams.Sequence, grpcClient)
		if err != nil {
			fmt.Printf("Transaction failed: %v\n", err)

			// Check if the error is a sequence mismatch error
			if resp != nil && resp.Code == 32 {
				expectedSeq, parseErr := lib.ExtractExpectedSequence(resp.RawLog)
				if parseErr == nil {
					// Update sequence and retry
					txParams.Sequence = expectedSeq
					fmt.Printf("Sequence mismatch detected. Updating sequence to %d and retrying...\n", expectedSeq)
					continue
				}
			}
			continue
		}

		if resp.Code != 0 {
			fmt.Printf("Transaction failed with code %d: %s\n", resp.Code, resp.RawLog)

			// Check for sequence mismatch error
			if resp.Code == 32 {
				expectedSeq, parseErr := lib.ExtractExpectedSequence(resp.RawLog)
				if parseErr == nil {
					// Update sequence and retry
					txParams.Sequence = expectedSeq
					fmt.Printf("Sequence mismatch detected. Updating sequence to %d and retrying...\n", expectedSeq)
					continue
				}
			}
			return fmt.Errorf("transaction failed with code %d: %s", resp.Code, resp.RawLog)
		}

		// Successfully broadcasted transaction
		fmt.Printf("-> Successfully transferred %s %s from %s to %s\n",
			amount.String(), config.Denom, sender.Address, receiverAddress)
		return nil
	}

	return fmt.Errorf("failed to send transaction after %d attempts", maxRetries)
}

// Add this new function
func handleBalanceAdjustment(accounts []types.Account, balances map[string]sdkmath.Int, config types.Config) error {
	if err := adjustBalances(accounts, balances, config); err != nil {
		return fmt.Errorf("failed to adjust balances: %v", err)
	}

	balances, err := lib.GetBalances(accounts, config)
	if err != nil {
		return fmt.Errorf("failed to get balances after adjustment: %v", err)
	}

	if !shouldProceedWithBalances(balances) {
		return errors.New("account balances are still not within threshold after adjustment")
	}

	return nil
}

func shouldProceedWithBalances(balances map[string]sdkmath.Int) bool {
	if lib.CheckBalancesWithinThreshold(balances, 0.15) {
		fmt.Println("Balances successfully adjusted within acceptable range")
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
		fmt.Println("Remaining balance differences are below minimum threshold, proceeding")
		return true
	}

	return false
}
