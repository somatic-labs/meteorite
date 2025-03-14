package broadcast

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cosmos/ibc-go/modules/apps/callbacks/testing/simapp/params"
	client "github.com/somatic-labs/meteorite/client"
	lib "github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func SendTransactionViaGRPC(
	ctx context.Context,
	txParams types.TransactionParams,
	sequence uint64,
	grpcClient *client.GRPCClient,
) (*sdk.TxResponse, string, error) {
	encodingConfig := params.MakeTestEncodingConfig()
	encodingConfig.Codec = cdc

	// Get the sequence manager for tracking sequences per node
	sequenceManager := lib.GetSequenceManager()

	// Use gRPC endpoint as the node identifier for sequence tracking
	nodeURL := txParams.Config.Nodes.GRPC

	// Make sure we use the node-specific sequence
	nodeSpecificSequence, err := sequenceManager.GetSequence(txParams.AcctAddress, nodeURL, txParams.Config, false)
	if err == nil && nodeSpecificSequence > sequence {
		// Use the node's tracked sequence if it's higher
		sequence = nodeSpecificSequence
		fmt.Printf("Using node-specific sequence %d for %s on gRPC node %s\n",
			sequence, txParams.AcctAddress, nodeURL)
	}

	// Log sequence use for debugging
	fmt.Printf("Building transaction for %s with sequence %d on gRPC node %s\n",
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

		// Create a context with timeout for just the broadcast operation
		broadcastCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		// Broadcast the transaction via gRPC
		grpcRes, err := grpcClient.SendTx(broadcastCtx, txBytes)

		// Handle insufficient fee error - either from error or response
		insufficientFeeErr := false
		errorMsg := ""

		if err != nil {
			errorMsg = err.Error()
			insufficientFeeErr = strings.Contains(errorMsg, "insufficient fee")
		} else if grpcRes != nil && grpcRes.Code != 0 {
			errorMsg = grpcRes.RawLog
			insufficientFeeErr = strings.Contains(errorMsg, "insufficient fee")
		}

		// Retry if we have an insufficient fee error and haven't exceeded retry limit
		if insufficientFeeErr && retryCount < maxRetries {
			// Try to extract the required fee
			requiredAmount, requiredDenom, parseErr := lib.ExtractRequiredFee(errorMsg)
			if parseErr == nil {
				// For retry with higher fee, modify the config's gas strategy
				fmt.Printf("Fee adjustment: Retry with fee %d%s on gRPC node %s\n",
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

				// Create new cancel function for next retry
				cancel()

				continue
			}
		}

		// Check for specific error patterns
		if err != nil && strings.Contains(err.Error(), "account sequence mismatch") {
			// Extract expected sequence if possible
			expectedSeq, parseErr := lib.ExtractExpectedSequence(err.Error())
			if parseErr == nil {
				// Update sequence in our manager
				sequenceManager.SetSequence(txParams.AcctAddress, nodeURL, expectedSeq)

				fmt.Printf("Sequence mismatch: gRPC node %s expects sequence %d for %s (was: %d)\n",
					nodeURL, expectedSeq, txParams.AcctAddress, sequence)

				cancel() // Clean up the context
				return nil, string(txBytes), fmt.Errorf("account sequence mismatch: expected %d, got %d",
					expectedSeq, sequence)
			}
			cancel() // Clean up the context
			return nil, string(txBytes), fmt.Errorf("account sequence mismatch: %w", err)
		}

		// Other broadcast errors
		if err != nil {
			cancel() // Clean up the context
			return nil, "", fmt.Errorf("failed to broadcast transaction via gRPC: %w", err)
		}

		// Log detailed transaction response for debugging
		txResponse := fmt.Sprintf("Code: %d, TxHash: %s, GasUsed: %d, GasWanted: %d",
			grpcRes.Code, grpcRes.TxHash, grpcRes.GasUsed, grpcRes.GasWanted)
		fmt.Printf("Transaction response: %s\n", txResponse)

		// Check for errors in the response
		if grpcRes.Code != 0 {
			// Handle sequence mismatch specifically
			if strings.Contains(grpcRes.RawLog, "account sequence mismatch") {
				expectedSeq, parseErr := lib.ExtractExpectedSequence(grpcRes.RawLog)
				if parseErr == nil {
					// Update sequence in our manager
					sequenceManager.SetSequence(txParams.AcctAddress, nodeURL, expectedSeq)

					fmt.Printf("Sequence mismatch in response: gRPC node %s expects sequence %d for %s (was: %d)\n",
						nodeURL, expectedSeq, txParams.AcctAddress, sequence)

					cancel() // Clean up the context
					return grpcRes, string(txBytes), fmt.Errorf("account sequence mismatch: expected %d, got %d",
						expectedSeq, sequence)
				}
				cancel() // Clean up the context
				return grpcRes, string(txBytes), fmt.Errorf("account sequence mismatch: %s", grpcRes.RawLog)
			}

			cancel() // Clean up the context
			return grpcRes, string(txBytes), fmt.Errorf("broadcast error code %d: %s", grpcRes.Code, grpcRes.RawLog)
		}

		// Transaction succeeded - update the sequence in our manager
		sequenceManager.SetSequence(txParams.AcctAddress, nodeURL, sequence+1)

		// Print transaction hash on success
		fmt.Printf("Transaction successful! TxID: %s (sequence: %d, gRPC node: %s)\n",
			grpcRes.TxHash, sequence, nodeURL)

		// Success case
		cancel() // Clean up the context
		return grpcRes, string(txBytes), nil
	}
}
