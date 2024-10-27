package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

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
	if _, err := toml.DecodeFile("nodes.toml", &config); err != nil {
		log.Fatalf("Failed to load config: %v", err)
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

	positions := config.Positions
	if positions <= 0 {
		log.Fatalf("Invalid number of positions: %d", positions)
	}
	fmt.Println("Positions", positions)

	var accounts []types.Account
	for i := 0; i < positions; i++ {
		position := uint32(i)
		privKey, pubKey, acctAddress := lib.GetPrivKey(config, mnemonic, position)
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
		log.Fatalf("Failed to get balances: %v", err)
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

	fmt.Println("balances", balances)

	if !lib.CheckBalancesWithinThreshold(balances, 0.10) {
		fmt.Println("Account balances are not within 10% of each other. Adjusting balances...")

		// Adjust balances to bring them within threshold
		err = adjustBalances(accounts, balances, config)
		if err != nil {
			log.Fatalf("Failed to adjust balances: %v", err)
		}

		// Re-fetch balances after adjustment
		balances, err = lib.GetBalances(accounts, config)
		if err != nil {
			log.Fatalf("Failed to get balances after adjustment: %v", err)
		}

		if !lib.CheckBalancesWithinThreshold(balances, 0.10) {
			totalBalance := sdkmath.ZeroInt()
			for _, balance := range balances {
				totalBalance = totalBalance.Add(balance)
			}
			if totalBalance.IsZero() {
				fmt.Println("All accounts have zero balance. Proceeding without adjusting balances.")
			} else {
				log.Fatalf("Account balances are still not within 10%% of each other after adjustment")
			}
		}
	}

	nodeURL := config.Nodes.RPC[0] // Use the first node

	chainID, err := lib.GetChainID(nodeURL)
	if err != nil {
		log.Fatalf("Failed to get chain ID: %v", err)
	}

	msgParams := config.MsgParams

	// Initialize gRPC client
	grpcClient, err := client.NewGRPCClient(config.Nodes.GRPC)
	if err != nil {
		log.Fatalf("Failed to create gRPC client: %v", err)
	}

	var wg sync.WaitGroup
	for _, account := range accounts {
		wg.Add(1)
		go func(acct types.Account) {
			defer wg.Done()

			// Get account info
			sequence, accNum, err := lib.GetAccountInfo(acct.Address, config)
			if err != nil {
				log.Printf("Failed to get account info for %s: %v", acct.Address, err)
				return
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
			successfulTxns, failedTxns, responseCodes, _ := broadcastLoop(txParams, BatchSize, grpcClient)

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

// broadcastLoop handles the main transaction broadcasting logic
func broadcastLoop(
	txParams types.TransactionParams,
	batchSize int,
	grpcClient *client.GRPCClient,
) (successfulTxns, failedTxns int, responseCodes map[uint32]int, updatedSequence uint64) {
	successfulTxns = 0
	failedTxns = 0
	responseCodes = make(map[uint32]int)
	sequence := txParams.Sequence

	for i := 0; i < batchSize; i++ {
		currentSequence := sequence

		fmt.Println("FROM LOOP, currentSequence", currentSequence)
		fmt.Println("FROM LOOP, accNum", txParams.AccNum)
		fmt.Println("FROM LOOP, chainID", txParams.ChainID)

		ctx := context.Background()
		start := time.Now()
		grpcResp, _, err := broadcast.SendTransactionViaGRPC(
			ctx,
			txParams,
			sequence,
			grpcClient,
		)
		elapsed := time.Since(start)

		fmt.Println("FROM MAIN, err", err)
		fmt.Println("FROM MAIN, resp", grpcResp.Code)

		if err == nil {
			fmt.Printf("%s Transaction succeeded, sequence: %d, time: %v\n",
				time.Now().Format("15:04:05.000"), currentSequence, elapsed)
			successfulTxns++
			responseCodes[grpcResp.Code]++
			sequence++ // Increment sequence for next transaction
			continue
		}

		fmt.Printf("%s Error: %v\n", time.Now().Format("15:04:05.000"), err)
		fmt.Println("FROM MAIN, resp.Code", grpcResp.Code)

		if grpcResp.Code == 32 {
			// Extract the expected sequence number from the error message
			expectedSeq, parseErr := extractExpectedSequence(err.Error())
			if parseErr != nil {
				fmt.Printf("%s Failed to parse expected sequence: %v\n", time.Now().Format("15:04:05.000"), parseErr)
				failedTxns++
				continue
			}

			sequence = expectedSeq
			fmt.Printf("%s Set sequence to expected value %d due to mismatch\n",
				time.Now().Format("15:04:05"), sequence)

			// Re-send the transaction with the correct sequence
			start = time.Now()
			grpcResp, respBytes, err := broadcast.SendTransactionViaGRPC(
				ctx,
				txParams,
				sequence,
				grpcClient,
			)

			fmt.Println("FROM MAIN, grpcResp", grpcResp)
			fmt.Println("FROM MAIN, respBytes", respBytes)

			elapsed = time.Since(start)

			if err != nil {
				fmt.Printf("%s Error after adjusting sequence: %v\n", time.Now().Format("15:04:05.000"), err)
				failedTxns++
				continue
			}

			fmt.Printf("%s Transaction succeeded after adjusting sequence, sequence: %d, time: %v\n",
				time.Now().Format("15:04:05"), sequence, elapsed)
			successfulTxns++
			responseCodes[grpcResp.Code]++
			sequence++ // Increment sequence for next transaction
			continue
		}
		failedTxns++

	}
	updatedSequence = sequence
	return successfulTxns, failedTxns, responseCodes, updatedSequence
}

// Function to extract the expected sequence number from the error message
func extractExpectedSequence(errMsg string) (uint64, error) {
	// Parse the error message to extract the expected sequence number
	// Example error message:
	// "account sequence mismatch, expected 42, got 41: incorrect account sequence"
	index := strings.Index(errMsg, "expected ")
	if index == -1 {
		return 0, errors.New("expected sequence not found in error message")
	}

	start := index + len("expected ")
	rest := errMsg[start:]
	parts := strings.SplitN(rest, ",", 2)
	if len(parts) < 1 {
		return 0, errors.New("failed to split expected sequence from error message")
	}

	expectedSeqStr := strings.TrimSpace(parts[0])
	expectedSeq, err := strconv.ParseUint(expectedSeqStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse expected sequence number: %v", err)
	}

	return expectedSeq, nil
}

// adjustBalances transfers funds between accounts to balance their balances within the threshold
func adjustBalances(accounts []types.Account, balances map[string]sdkmath.Int, config types.Config) error {
	// Calculate the total balance
	totalBalance := sdkmath.ZeroInt()
	for _, balance := range balances {
		totalBalance = totalBalance.Add(balance)
	}

	if totalBalance.IsZero() {
		// All accounts have zero balance; cannot adjust balances
		fmt.Println("All accounts have zero balance. Cannot adjust balances.")
		return nil
	}

	numAccounts := sdkmath.NewInt(int64(len(accounts)))
	averageBalance := totalBalance.Quo(numAccounts)

	// Create a slice to track balances that need to send or receive funds
	type balanceAdjustment struct {
		Account types.Account
		Amount  sdkmath.Int // Positive if needs to receive, negative if needs to send
	}
	var adjustments []balanceAdjustment

	for _, acct := range accounts {
		currentBalance := balances[acct.Address]
		difference := averageBalance.Sub(currentBalance)

		// Only consider adjustments exceeding the threshold (10% of average)
		threshold := averageBalance.MulRaw(10).QuoRaw(100) // threshold = averageBalance * 10 / 100
		if difference.Abs().GT(threshold) {
			adjustments = append(adjustments, balanceAdjustment{
				Account: acct,
				Amount:  difference,
			})
		}
	}

	// Separate adjustments into senders (negative amounts) and receivers (positive amounts)
	var senders, receivers []balanceAdjustment
	for _, adj := range adjustments {
		if adj.Amount.IsNegative() {
			// Check if the account has enough balance to send
			accountBalance := balances[adj.Account.Address]
			if accountBalance.GT(sdkmath.ZeroInt()) {
				senders = append(senders, adj)
			} else {
				fmt.Printf("Account %s has zero balance, cannot send funds.\n", adj.Account.Address)
			}
		} else if adj.Amount.IsPositive() {
			receivers = append(receivers, adj)
		}
	}

	// Initialize gRPC client
	grpcClient, err := client.NewGRPCClient(config.Nodes.GRPC)
	if err != nil {
		return fmt.Errorf("failed to create gRPC client: %v", err)
	}

	// Perform transfers from senders to receivers
	for _, sender := range senders {
		amountToSend := sender.Amount.Abs()
		for i := range receivers {
			receiver := &receivers[i]
			if receiver.Amount.GT(sdkmath.ZeroInt()) {
				transferAmount := sdkmath.MinInt(amountToSend, receiver.Amount)

				// Ensure you're using the correct sender account with matching PrivKey
				err := TransferFunds(grpcClient, sender.Account, receiver.Account.Address, transferAmount, config)
				if err != nil {
					return fmt.Errorf("failed to transfer funds from %s to %s: %v",
						sender.Account.Address, receiver.Account.Address, err)
				}

				// Update the amounts
				amountToSend = amountToSend.Sub(transferAmount)
				receiver.Amount = receiver.Amount.Sub(transferAmount)

				if amountToSend.IsZero() {
					break
				}
			}
		}
	}

	return nil
}

func TransferFunds(grpcClient *client.GRPCClient, sender types.Account, receiverAddress string, amount sdkmath.Int, config types.Config) error {
	// Get the sender's account info
	sequence, accNum, err := lib.GetAccountInfo(sender.Address, config)
	if err != nil {
		return fmt.Errorf("failed to get account info for sender %s: %v", sender.Address, err)
	}

	fmt.Printf("TransferFunds - Sender: %s, Account Number: %d, Sequence: %d\n", sender.Address, accNum, sequence)

	nodeURL := config.Nodes.RPC[0]
	chainID, err := lib.GetChainID(nodeURL)
	if err != nil {
		return fmt.Errorf("failed to get chain ID: %v", err)
	}

	fmt.Printf("TransferFunds - Chain ID: %s\n", chainID)

	// Prepare the transaction parameters
	txParams := types.TransactionParams{
		Config:      config,
		NodeURL:     nodeURL,
		ChainID:     chainID,
		Sequence:    sequence,
		AccNum:      accNum,
		PrivKey:     sender.PrivKey,
		PubKey:      sender.PubKey,
		AcctAddress: sender.Address,
		MsgType:     config.MsgType,
		MsgParams: types.MsgParams{
			FromAddress: sender.Address,
			ToAddress:   receiverAddress,
			Amount:      amount.Int64(),
			Denom:       config.Denom,
		},
	}

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Send the transaction
	grpcResp, _, err := broadcast.SendTransactionViaGRPC(ctx, txParams, sequence, grpcClient)
	if err != nil {
		return fmt.Errorf("failed to send transaction: %v", err)
	}

	if grpcResp.Code != 0 {
		return fmt.Errorf("transaction failed with code %d: %s", grpcResp.Code, grpcResp.RawLog)
	}

	fmt.Printf("Transferred %s%s from %s to %s\n", amount.String(), config.Denom, sender.Address, receiverAddress)
	return nil
}
