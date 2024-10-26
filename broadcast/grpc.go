package broadcast

import (
	"context"
	"fmt"

	"github.com/cosmos/ibc-go/modules/apps/callbacks/testing/simapp/params"
	client "github.com/somatic-labs/meteorite/client"
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

	// Build and sign the transaction
	txBytes, err := BuildAndSignTransaction(ctx, txParams, sequence, encodingConfig)
	if err != nil {
		return nil, "", err
	}

	// Broadcast the transaction via gRPC
	grpcRes, err := grpcClient.SendTx(ctx, txBytes)
	if err != nil {
		return nil, "", fmt.Errorf("failed to broadcast transaction via gRPC: %w", err)
	}

	// Check for errors in the response
	if grpcRes.Code != 0 {
		return grpcRes, string(txBytes), fmt.Errorf("broadcast error code %d: %s", grpcRes.Code, grpcRes.RawLog)
	}

	return grpcRes, string(txBytes), nil
}
