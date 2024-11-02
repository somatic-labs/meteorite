package broadcast

import (
	"fmt"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	types "github.com/somatic-labs/meteorite/types"

	"cosmossdk.io/simapp/params"
)

// SendTransactionViaRPC sends a transaction using the provided TransactionParams and sequence number.
func SendTransactionViaRPC(txParams types.TransactionParams, sequence uint64) (*coretypes.ResultBroadcastTx, string, error) {
	encodingConfig := params.MakeTestEncodingConfig()
	encodingConfig.Codec = cdc

	// Build and sign the transaction
	txBytes, err := BuildAndSignTransaction(txParams, sequence, encodingConfig)
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
