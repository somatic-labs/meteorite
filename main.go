package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"cosmossdk.io/log"
	"github.com/BurntSushi/toml"
	"github.com/somatic-labs/meteorite/broadcast"
	"github.com/somatic-labs/meteorite/client"
	"github.com/somatic-labs/meteorite/lib"
	"github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	BatchSize       = 100000000
	TimeoutDuration = 50 * time.Millisecond
)

func main() {

	config := types.Config{}
	config.Logger = log.NewLogger(os.Stdout)

	if _, err := toml.DecodeFile("nodes.toml", &config); err != nil {
		config.Logger.Error("Failed to load config: %v", err)
	}

	mnemonic, err := os.ReadFile("seedphrase")
	if err != nil {
		config.Logger.Error("Failed to read seed phrase: %v", err)
	}

	// Set Bech32 prefixes and seal the configuration once
	sdkConfig := sdk.GetConfig()
	sdkConfig.SetBech32PrefixForAccount(config.Prefix, config.Prefix+"pub")
	sdkConfig.SetBech32PrefixForValidator(config.Prefix+"valoper", config.Prefix+"valoperpub")
	sdkConfig.SetBech32PrefixForConsensusNode(config.Prefix+"valcons", config.Prefix+"valconspub")
	sdkConfig.Seal()

	positions := config.Positions
	const MaxPositions = 100 // Adjust based on requirements
	if positions <= 0 || positions > MaxPositions {
		config.Logger.Error("Number of positions must be between 1 and %d, got: %d", MaxPositions, positions)
	}
	fmt.Println("Positions", positions)

	var accounts []types.Account
	for i := 0; i < int(positions); i++ {
		position := uint32(i)
		privKey, pubKey, acctAddress := lib.GetPrivKey(config, mnemonic, position)
		if privKey == nil || pubKey == nil || len(acctAddress) == 0 {
			config.Logger.Error("Failed to generate keys for position %d", position)
		}
		accounts = append(accounts, types.Account{
			PrivKey:  privKey,
			PubKey:   pubKey,
			Address:  acctAddress,
			Position: position,
		})
	}

	// **Print addresses and positions at startup**
	fmt.Println("Addresses and Positions:")
	for _, acct := range accounts {
		fmt.Printf("Position %d: Address: %s\n", acct.Position, acct.Address)
	}

	// Get balances and ensure they are within 10% of each other
	balances, err := lib.GetBalances(accounts, config)
	if err != nil {
		config.Logger.Error("Failed to get balances: %v", err)
	}

	// Print addresses and balances
	fmt.Println("Wallets and Balances:")
	for _, acct := range accounts {
		balance, err := lib.GetAccountBalance(acct.Address, config)
		if err != nil {
			config.Logger.Error("Failed to get balance for %s: %v", acct.Address, err)
			continue
		}
		fmt.Printf("Position %d: Address: %s, Balance: %s %s\n", acct.Position, acct.Address, balance.String(), config.Denom)
	}

	fmt.Println("balances", balances)

	if !lib.CheckBalancesWithinThreshold(balances, 0.10) {
		fmt.Println("Account balances are not within 10% of each other. Adjusting balances...")
		if err := handleBalanceAdjustment(accounts, balances, config); err != nil {
			config.Logger.Error("Failed to handle balance adjustment: %v", err)
		}
	}

	nodeURL := config.Nodes.RPC[0] // Use the first node

	chainID, err := lib.GetChainID(nodeURL)
	if err != nil {
		config.Logger.Error("Failed to get chain ID: %v", err)
	}

	msgParams := config.MsgParams

	// Initialize gRPC client
	//	grpcClient, err := client.NewGRPCClient(config.Nodes.GRPC)
	//	if err != nil {
	//		log.Fatalf("Failed to create gRPC client: %v", err)
	//	}

	var wg sync.WaitGroup
	for _, account := range accounts {
		wg.Add(1)
		go func(acct types.Account) {
			defer wg.Done()

			// Get account info
			sequence, accNum, err := lib.GetAccountInfo(acct.Address, config)
			if err != nil {
				config.Logger.Error("Warning: Failed to get account info for %s: %v. Proceeding with sequence 0", acct.Address, err)
				sequence = 0
				accNum = 0
			}

			txParams := types.TransactionParams{
				Config:      config,
				NodeURL:     nodeURL,
				ChainID:     chainID,
				Sequence:    sequence,
				AccNum:      accNum,
				PrivKey:     acct.PrivKey,
				PubKey:      acct.PubKey,
				AcctAddress: acct.Address,
				MsgType:     config.MsgType,
				MsgParams:   msgParams,
			}

			// Broadcast transactions
			successfulTxns, failedTxns, responseCodes, _ := broadcast.Loop(txParams, BatchSize, int(acct.Position), config.Denom)

			fmt.Printf("Account %s: Successful transactions: %d, Failed transactions: %d\n", acct.Address, successfulTxns, failedTxns)
			fmt.Println("Response code breakdown:")
			for code, count := range responseCodes {
				percentage := float64(count) / float64(successfulTxns+failedTxns) * 100
				fmt.Printf("Code %d: %d (%.2f%%)\n", code, count, percentage)
			}
		}(account)
	}

	wg.Wait()
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

	// Calculate target balance per account
	accountCount := sdkmath.NewInt(int64(len(accounts)))
	targetBalance := totalBalance.Quo(accountCount)

	// Track adjustments needed
	adjustments := make(map[string]sdkmath.Int)

	// Calculate needed adjustments
	for _, account := range accounts {
		currentBalance := balances[account.Address]
		diff := targetBalance.Sub(currentBalance)
		if !diff.IsZero() {
			adjustments[account.Address] = diff
		}
	}

	return nil
}

func shouldProceedWithBalances(balances map[string]sdkmath.Int) bool {
	// Check if balances map is nil or empty
	if balances == nil || len(balances) == 0 {
		fmt.Println("No balances to check")
		return false
	}

	if lib.CheckBalancesWithinThreshold(balances, 0.15) {
		fmt.Println("Balances successfully adjusted within acceptable range")
		return true
	}

	var maxBalance sdkmath.Int
	for _, balance := range balances {
		if balance.IsNil() {
			continue // Skip nil balances
		}
		if maxBalance.IsNil() {
			maxBalance = balance
			continue
		}
		if balance.GT(maxBalance) {
			maxBalance = balance
		}
	}

	return false
}

func TransferFunds(sender types.Account, receiverAddress string, amount sdkmath.Int, config types.Config) error {
	fmt.Printf("\n=== Starting Transfer ===\n")
	fmt.Printf("Sender Address: %s\n", sender.Address)
	fmt.Printf("Receiver Address: %s\n", receiverAddress)
	fmt.Printf("Amount: %s %s\n", amount.String(), config.Denom)

	if sender.PrivKey == nil {
		return errors.New("sender private key is nil")
	}
	if sender.PubKey == nil {
		return errors.New("sender public key is nil")
	}

	// Get the sender's account info
	sequence, accnum, err := lib.GetAccountInfo(sender.Address, config)
	if err != nil {
		return fmt.Errorf("failed to get account info for sender %s: %v", sender.Address, err)
	}

	nodeURL := config.Nodes.RPC[0]

	grpcClient, err := client.NewGRPCClient(config.Nodes.GRPC)
	if err != nil {
		return fmt.Errorf("failed to create gRPC client: %v", err)
	}

	txParams := types.TransactionParams{
		Config:      config,
		NodeURL:     nodeURL,
		ChainID:     config.Chain,
		Sequence:    sequence,
		AccNum:      accnum,
		PrivKey:     sender.PrivKey,
		PubKey:      sender.PubKey,
		AcctAddress: sender.Address,
		MsgType:     "bank_send",
		MsgParams: types.MsgParams{
			FromAddress: sender.Address,
			ToAddress:   receiverAddress,
			Amount:      amount.Int64(),
			Denom:       config.Denom,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		fmt.Printf("Attempt %d to send transaction with sequence %d\n", attempt+1, sequence)

		resp, _, err := broadcast.SendTransactionViaGRPC(ctx, txParams, sequence, grpcClient)
		if err != nil {
			fmt.Printf("Transaction failed: %v\n", err)

			// Check if the error is a sequence mismatch error (code 32)
			if resp != nil && resp.Code == 32 {
				expectedSeq, parseErr := lib.ExtractExpectedSequence(resp.RawLog)
				if parseErr != nil {
					return fmt.Errorf("failed to parse expected sequence: %v", parseErr)
				}

				// Update sequence and retry
				sequence = expectedSeq
				txParams.Sequence = sequence
				fmt.Printf("Sequence mismatch detected. Updating sequence to %d and retrying...\n", sequence)
				continue
			}

			return fmt.Errorf("failed to send transaction: %v", err)
		}

		if resp.Code != 0 {
			fmt.Printf("Transaction failed with code %d: %s\n", resp.Code, resp.RawLog)

			// Check for sequence mismatch error
			if resp.Code == 32 {
				expectedSeq, parseErr := lib.ExtractExpectedSequence(resp.RawLog)
				if parseErr != nil {
					return fmt.Errorf("failed to parse expected sequence: %v", parseErr)
				}

				// Update sequence and retry
				sequence = expectedSeq
				txParams.Sequence = sequence
				fmt.Printf("Sequence mismatch detected. Updating sequence to %d and retrying...\n", sequence)
				continue
			}

			return fmt.Errorf("transaction failed with code %d: %s", resp.Code, resp.RawLog)
		}

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
func CheckBalancesWithinThreshold(balances map[string]sdkmath.Int, threshold float64) bool {
	if balances == nil || len(balances) == 0 {
		return false
	}

	var minBalance, maxBalance sdkmath.Int
	first := true

	for _, balance := range balances {
		if balance.IsNil() {
			continue // Skip nil balances
		}

		if first {
			minBalance = balance
			maxBalance = balance
			first = false
			continue
		}

		if balance.LT(minBalance) {
			minBalance = balance
		}
		if balance.GT(maxBalance) {
			maxBalance = balance
		}
	}

	// If we didn't find any valid balances
	if first {
		return false
	}

	// Skip check if all balances are below minimum threshold
	minThreshold := sdkmath.NewInt(1000000) // 1 token assuming 6 decimals
	if maxBalance.LT(minThreshold) {
		return true
	}

	// Calculate the difference as a percentage of the max balance
	if maxBalance.IsZero() {
		return minBalance.IsZero()
	}

	diff := maxBalance.Sub(minBalance)
	diffFloat := float64(diff.Int64())
	maxFloat := float64(maxBalance.Int64())

	percentage := diffFloat / maxFloat
	return percentage <= threshold
}
