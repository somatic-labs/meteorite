package broadcast

import (
	"context"
	"fmt"
	"time"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	transfertypes "github.com/cosmos/ibc-go/v7/modules/apps/transfer/types"
	client "github.com/somatic-labs/meteorite/client"
	"github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// Add these at the top of the file
type TimingMetrics struct {
	PrepStart  time.Time
	SignStart  time.Time
	BroadStart time.Time
	Complete   time.Time
	Position   int
}

func (t *TimingMetrics) LogTiming(sequence uint64, txHash string, success bool, err error) {
	timeFormat := "2006-01-02 15:04:05.000"
	prepTime := t.SignStart.Sub(t.PrepStart)
	signTime := t.BroadStart.Sub(t.SignStart)
	broadcastTime := t.Complete.Sub(t.BroadStart)
	totalTime := t.Complete.Sub(t.PrepStart)

	status := "SUCCESS"
	if !success {
		status = "FAILED"
	}

	txStatus := ""
	if txHash != "" {
		txStatus = fmt.Sprintf(" txhash=%s", txHash)
	}

	fmt.Printf("[POS-%d] [%s] %s seq=%d prep=%v sign=%v broadcast=%v total=%v%s%s\n",
		t.Position,
		time.Now().Format(timeFormat),
		status,
		sequence,
		prepTime,
		signTime,
		broadcastTime,
		totalTime,
		txStatus,
		formatError(err))
}

func formatError(err error) string {
	if err != nil {
		return fmt.Sprintf(" error=\"%v\"", err)
	}
	return ""
}

var cdc = codec.NewProtoCodec(codectypes.NewInterfaceRegistry())

func init() {
	transfertypes.RegisterInterfaces(cdc.InterfaceRegistry())
	banktypes.RegisterInterfaces(cdc.InterfaceRegistry())
}

// Transaction broadcasts the transaction bytes to the given RPC endpoint.
func Transaction(txBytes []byte, rpcEndpoint string) (*coretypes.ResultBroadcastTx, error) {
	client, err := GetClient(rpcEndpoint)
	if err != nil {
		return nil, err
	}

	return client.Transaction(txBytes)
}

// Loop handles the main transaction broadcasting logic
func Loop(
	txParams types.TransactionParams,
	batchSize int,
	position int,
	mode string,
) (successfulTxns, failedTxns int, responseCodes map[uint32]int, updatedSequence uint64) {
	// Initialize return values
	successfulTxns = 0
	failedTxns = 0
	responseCodes = make(map[uint32]int)
	sequence := txParams.Sequence

	// Add nil checks
	if txParams.Config.Logger == nil {
		fmt.Printf("Error: Logger is not initialized for position %d\n", position)
		failedTxns = batchSize
		return successfulTxns, failedTxns, responseCodes, sequence
	}

	txParams.Config.Logger.Info("Starting transaction loop",
		"position", position,
		"batch_size", batchSize,
		"mode", mode,
	)

	for i := 0; i < batchSize; i++ {
		currentSequence := sequence
		metrics := &TimingMetrics{
			PrepStart: time.Now(),
			Position:  position,
		}

		metrics.SignStart = time.Now()

		var resp *coretypes.ResultBroadcastTx
		var sdkResp *sdk.TxResponse
		var err error

		metrics.BroadStart = time.Now()
		switch mode {
		case "grpc":
			// Initialize gRPC client once if needed
			grpcClient, err := client.NewGRPCClient(txParams.Config.Nodes.GRPC)
			if err != nil {
				txParams.Config.Logger.Error("Failed to create gRPC client", "error", err)
				failedTxns++
				continue
			}
			sdkResp, _, err = SendTransactionViaGRPC(context.Background(), txParams, currentSequence, grpcClient)
		case "rpc":
			resp, _, err = SendTransactionViaRPC(txParams, currentSequence)
		case "api":
			sdkResp, _, err = SendTransactionViaAPI(txParams, currentSequence)
		default:
			txParams.Config.Logger.Error("Unknown broadcast mode", "mode", mode)
			failedTxns++
			return
		}
		metrics.Complete = time.Now()

		var txHash string
		if err != nil {
			metrics.LogTiming(currentSequence, "", false, err)
			failedTxns++

			if resp != nil && resp.Code == 32 {
				newSeq, success, newResp := handleSequenceMismatch(txParams, position, sequence, err, mode)
				sequence = newSeq
				if success {
					successfulTxns++
					responseCodes[newResp.Code]++
				}
				continue
			}
			continue
		}

		// Extract txHash based on the mode
		if mode == "rpc" && resp != nil {
			txHash = resp.Hash.String()
		} else if (mode == "grpc" || mode == "api") && sdkResp != nil {
			txHash = sdkResp.TxHash
		}

		metrics.LogTiming(currentSequence, txHash, true, nil)
		successfulTxns++
		if mode == "rpc" && resp != nil {
			responseCodes[resp.Code]++
			if resp.Code == 0 {
				txParams.Config.Logger.Info("Transaction successful",
					"txhash", resp.Hash.String(),
					"sequence", sequence,
					"position", position)
			}
		} else if (mode == "grpc" || mode == "api") && sdkResp != nil {
			responseCodes[sdkResp.Code]++
			if sdkResp.Code == 0 {
				txParams.Config.Logger.Info("Transaction successful",
					"txhash", sdkResp.TxHash,
					"sequence", sequence,
					"position", position)
			}
		}

		sequence++
	}

	updatedSequence = sequence
	return successfulTxns, failedTxns, responseCodes, updatedSequence
}

// handleSequenceMismatch handles the case where a transaction fails due to sequence mismatch
func handleSequenceMismatch(txParams types.TransactionParams, position int, sequence uint64, err error, mode string) (uint64, bool, *coretypes.ResultBroadcastTx) {
	expectedSeq, parseErr := lib.ExtractExpectedSequence(err.Error())
	if parseErr != nil {
		fmt.Printf("[POS-%d] Failed to parse expected sequence: %v\n", position, parseErr)
		return sequence, false, nil
	}

	fmt.Printf("[POS-%d] Set sequence to expected value %d due to mismatch\n", position, expectedSeq)

	metrics := &TimingMetrics{
		PrepStart: time.Now(),
		Position:  position,
	}

	metrics.SignStart = time.Now()
	metrics.BroadStart = time.Now()

	var resp *coretypes.ResultBroadcastTx
	var err2 error

	switch mode {
	case "grpc":
		// Use gRPC client
		grpcClient, err := client.NewGRPCClient(txParams.Config.Nodes.GRPC)
		if err != nil {
			fmt.Printf("Failed to create gRPC client: %v\n", err)
			return expectedSeq, false, nil
		}
		_, _, err2 = SendTransactionViaGRPC(context.Background(), txParams, expectedSeq, grpcClient)
	case "rpc":
		resp, _, err2 = SendTransactionViaRPC(txParams, expectedSeq)
	case "api":
		_, _, err2 = SendTransactionViaAPI(txParams, expectedSeq)
	}

	metrics.Complete = time.Now()

	if err2 != nil {
		metrics.LogTiming(expectedSeq, "", false, err2)
		return expectedSeq, false, nil
	}

	metrics.LogTiming(expectedSeq, "", true, nil)
	return expectedSeq + 1, true, resp
}
