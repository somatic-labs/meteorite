package broadcast

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cosmos/ibc-go/modules/apps/callbacks/testing/simapp/params"
	client "github.com/somatic-labs/meteorite/client"
	types "github.com/somatic-labs/meteorite/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	lib "github.com/somatic-labs/meteorite/lib"
)

func SendTransactionViaGRPC(
	ctx context.Context,
	txParams types.TransactionParams,
	sequence uint64,
	grpcClient *client.GRPCClient,
) (*sdk.TxResponse, string, error) {
	encodingConfig := params.MakeTestEncodingConfig()
	encodingConfig.Codec = cdc

	// Log sequence use for debugging
	fmt.Printf("Building transaction for %s with sequence %d\n",
		txParams.AcctAddress, sequence)

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
	if err != nil {
		// Check for specific error patterns
		if strings.Contains(err.Error(), "account sequence mismatch") {
			// Extract expected sequence if possible
			expectedSeq, parseErr := lib.ExtractExpectedSequence(err.Error())
			if parseErr == nil {
				return nil, string(txBytes), fmt.Errorf("account sequence mismatch: expected %d, got %d",
					expectedSeq, sequence)
			}
			return nil, string(txBytes), fmt.Errorf("account sequence mismatch: %w", err)
		}

		// Other broadcast errors
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
				return grpcRes, string(txBytes), fmt.Errorf("account sequence mismatch: expected %d, got %d",
					expectedSeq, sequence)
			}
			return grpcRes, string(txBytes), fmt.Errorf("account sequence mismatch: %s", grpcRes.RawLog)
		}

		// Handle insufficient fee error specifically
		if strings.Contains(grpcRes.RawLog, "insufficient fee") {
			return grpcRes, string(txBytes), fmt.Errorf("insufficient fee: %s", grpcRes.RawLog)
		}

		return grpcRes, string(txBytes), fmt.Errorf("broadcast error code %d: %s", grpcRes.Code, grpcRes.RawLog)
	}

	return grpcRes, string(txBytes), nil
}
