package broadcast

import (
	"context"
	"fmt"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/ibc-go/modules/apps/callbacks/testing/simapp/params"
	types "github.com/somatic-labs/meteorite/types"
)

// SendTransactionViaRPC sends a transaction using the provided TransactionParams and sequence number.
func SendTransactionViaRPC(txParams types.TransactionParams, sequence uint64) (*coretypes.ResultBroadcastTx, string, error) {
	encodingConfig := params.MakeTestEncodingConfig()
	encodingConfig.Codec = cdc

	ctx := context.Background()

	// Build and sign the transaction
	txBytes, err := BuildAndSignTransaction(ctx, txParams, sequence, encodingConfig)
	if err != nil {
		return nil, "", err
	}

	// Broadcast the transaction via RPC
	resp, err := Transaction(txBytes, txParams.NodeURL)
	if err != nil {
		return resp, string(txBytes), fmt.Errorf("failed to broadcast transaction: %w", err)
	}

	return resp, string(txBytes), nil
}
