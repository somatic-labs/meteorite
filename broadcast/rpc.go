package broadcast

import (
	"context"
	"fmt"
	"strings"
	"time"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/ibc-go/modules/apps/callbacks/testing/simapp/params"
	"github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"
)

// SendTransactionViaRPC sends a transaction using the provided TransactionParams and sequence number.
func SendTransactionViaRPC(
	ctx context.Context,
	txParams types.TransactionParams,
	sequence uint64,
) (*coretypes.ResultBroadcastTx, string, error) {
	encodingConfig := params.MakeTestEncodingConfig()
	encodingConfig.Codec = cdc

	// Create a context with 120 seconds timeout to avoid context deadline exceeded
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Get the sequence manager for tracking sequences per node
	sequenceManager := lib.GetSequenceManager()

	// Make sure we use the node-specific sequence
	nodeURL := txParams.NodeURL
	nodeSpecificSequence, err := sequenceManager.GetSequence(txParams.AcctAddress, nodeURL, txParams.Config, false)
	if err == nil && nodeSpecificSequence > sequence {
		// Use the node's tracked sequence if it's higher
		sequence = nodeSpecificSequence
		fmt.Printf("Using node-specific sequence %d for %s on node %s\n",
			sequence, txParams.AcctAddress, nodeURL)
	}

	// Log sequence use for debugging
	fmt.Printf("Building transaction for %s with sequence %d on node %s\n",
		txParams.AcctAddress, sequence, nodeURL)

	// Allow up to one fee adjustment retry
	maxRetries := 1
	retryCount := 0

	for {
		// Override sequence in params with the one explicitly provided
		txParams.Sequence = sequence

		// Build and sign the transaction
		txBytes, err := BuildAndSignTransaction(ctx, txParams, sequence, encodingConfig)
		if err != nil {
			return nil, "", fmt.Errorf("failed to build transaction: %w", err)
		}

		// Create a broadcast client
		broadcastClient, err := GetClient(nodeURL)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create broadcast client: %w", err)
		}

		// Broadcast the transaction
		resp, err := broadcastClient.Transaction(txBytes)

		// Handle insufficient fee error - either directly from the broadcast error or from the response
		insufficientFeeErr := false
		errorMsg := ""

		if err != nil {
			errorMsg = err.Error()
			insufficientFeeErr = strings.Contains(errorMsg, "insufficient fee")
		} else if resp != nil && resp.Code != 0 {
			errorMsg = resp.Log
			insufficientFeeErr = strings.Contains(errorMsg, "insufficient fee")
		}

		// Retry if we have an insufficient fee error and haven't exceeded retry limit
		if insufficientFeeErr && retryCount < maxRetries {
			// Try to extract the required fee
			requiredAmount, requiredDenom, parseErr := lib.ExtractRequiredFee(errorMsg)
			if parseErr == nil {
				// For retry with higher fee, modify the config's gas strategy
				fmt.Printf("Fee adjustment: Retry with fee %d%s on node %s\n",
					requiredAmount, requiredDenom, nodeURL)

				// Create a copy of the config to avoid modifying the original
				updatedConfig := txParams.Config

				// Override the default gas calculation by setting a custom gas amount in MsgParams
				if txParams.MsgParams == nil {
					txParams.MsgParams = make(map[string]interface{})
				}

				// Force using a specific gas amount that will result in the required fee
				txParams.MsgParams["calculated_gas_amount"] = uint64(requiredAmount * 10) // *10 because fee is typically gasLimit/10

				// Ensure we use the correct denom
				updatedConfig.Denom = requiredDenom
				txParams.Config = updatedConfig

				retryCount++
				continue
			}
		}

		// Handle sequence mismatch
		if err != nil && strings.Contains(err.Error(), "account sequence mismatch") {
			// Extract expected sequence if possible
			expectedSeq, parseErr := lib.ExtractExpectedSequence(err.Error())
			if parseErr == nil {
				// Update sequence in our manager
				sequenceManager.SetSequence(txParams.AcctAddress, nodeURL, expectedSeq)

				fmt.Printf("Sequence mismatch: node %s expects sequence %d for %s (was: %d)\n",
					nodeURL, expectedSeq, txParams.AcctAddress, sequence)

				return nil, string(txBytes), fmt.Errorf("account sequence mismatch: expected %d, got %d",
					expectedSeq, sequence)
			}
			return nil, string(txBytes), fmt.Errorf("account sequence mismatch: %w", err)
		}

		// Handle other broadcast errors
		if err != nil {
			return nil, "", fmt.Errorf("failed to broadcast transaction: %w", err)
		}

		// Check for standard errors in the response
		if resp.Code != 0 {
			// Handle sequence mismatch in response
			if strings.Contains(resp.Log, "account sequence mismatch") {
				expectedSeq, parseErr := lib.ExtractExpectedSequence(resp.Log)
				if parseErr == nil {
					// Update sequence in our manager
					sequenceManager.SetSequence(txParams.AcctAddress, nodeURL, expectedSeq)

					fmt.Printf("Sequence mismatch in response: node %s expects sequence %d for %s (was: %d)\n",
						nodeURL, expectedSeq, txParams.AcctAddress, sequence)

					return resp, string(txBytes), fmt.Errorf("account sequence mismatch: expected %d, got %d",
						expectedSeq, sequence)
				}
				return resp, string(txBytes), fmt.Errorf("account sequence mismatch: %s", resp.Log)
			}

			return resp, string(txBytes), fmt.Errorf("broadcast error code %d: %s", resp.Code, resp.Log)
		}

		// Transaction succeeded - update the sequence in our manager
		sequenceManager.SetSequence(txParams.AcctAddress, nodeURL, sequence+1)

		// Print transaction hash on success
		fmt.Printf("Transaction successful! TxID: %s (sequence: %d, node: %s)\n",
			resp.Hash.String(), sequence, nodeURL)

		// If we get here, we have a successful response
		return resp, string(txBytes), nil
	}
}
