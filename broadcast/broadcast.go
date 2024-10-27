package broadcast

import (
	"context"
	"fmt"
	"log"
	"time"

	cometrpc "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	tmtypes "github.com/cometbft/cometbft/types"
	transfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	"github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

var cdc = codec.NewProtoCodec(codectypes.NewInterfaceRegistry())

func init() {
	transfertypes.RegisterInterfaces(cdc.InterfaceRegistry())
	banktypes.RegisterInterfaces(cdc.InterfaceRegistry())
}

// Transaction broadcasts the transaction bytes to the given RPC endpoint.
func Transaction(txBytes []byte, rpcEndpoint string) (*coretypes.ResultBroadcastTx, error) {
	cmtCli, err := cometrpc.New(rpcEndpoint, "/websocket")
	if err != nil {
		log.Fatal(err)
	}

	t := tmtypes.Tx(txBytes)

	ctx := context.Background()
	res, err := cmtCli.BroadcastTxSync(ctx, t)
	if err != nil {
		fmt.Println("Error at broadcast:", err)
		return nil, err
	}

	if res.Code != 0 {
		// Return an error containing the code and log message
		return res, fmt.Errorf("broadcast error code %d: %s", res.Code, res.Log)
	}

	return res, nil
}

// broadcastLoop handles the main transaction broadcasting logic
func BroadcastLoop(
	txParams types.TransactionParams,
	batchSize int,
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

		start := time.Now()
		resp, _, err := SendTransactionViaRPC(
			txParams,
			currentSequence,
		)
		elapsed := time.Since(start)

		fmt.Println("FROM MAIN, err", err)
		fmt.Println("FROM MAIN, resp", resp.Code)

		if err == nil {
			fmt.Printf("%s Transaction succeeded, sequence: %d, time: %v\n",
				time.Now().Format("15:04:05"), currentSequence, elapsed)
			successfulTxns++
			responseCodes[resp.Code]++
			sequence++ // Increment sequence for next transaction
			continue
		}

		fmt.Printf("%s Error: %v\n", time.Now().Format("15:04:05.000"), err)
		fmt.Println("FROM MAIN, resp.Code", resp.Code)

		if resp.Code == 32 {
			// Extract the expected sequence number from the error message
			expectedSeq, parseErr := lib.ExtractExpectedSequence(err.Error())
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
			resp, _, err = SendTransactionViaRPC(
				txParams,
				sequence,
			)
			elapsed = time.Since(start)

			if err != nil {
				fmt.Printf("%s Error after adjusting sequence: %v\n", time.Now().Format("15:04:05.000"), err)
				failedTxns++
				continue
			}

			fmt.Printf("%s Transaction succeeded after adjusting sequence, sequence: %d, time: %v\n",
				time.Now().Format("15:04:05"), sequence, elapsed)
			successfulTxns++
			responseCodes[resp.Code]++
			sequence++ // Increment sequence for next transaction
			continue
		}
		failedTxns++

	}
	updatedSequence = sequence
	return successfulTxns, failedTxns, responseCodes, updatedSequence
}
