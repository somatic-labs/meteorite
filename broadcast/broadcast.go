package broadcast

import (
	"context"
	"fmt"
	"time"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	transfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	"github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
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

func (t *TimingMetrics) LogTiming(sequence uint64, success bool, err error) {
	prepTime := t.SignStart.Sub(t.PrepStart)
	signTime := t.BroadStart.Sub(t.SignStart)
	broadcastTime := t.Complete.Sub(t.BroadStart)
	totalTime := t.Complete.Sub(t.PrepStart)

	status := "SUCCESS"
	if !success {
		status = "FAILED"
	}

	fmt.Printf("[POS-%d] %s Transaction %s: seq=%d prep=%v sign=%v broadcast=%v total=%v%s\n",
		t.Position,
		time.Now().Format("15:04:05.000"),
		status,
		sequence,
		prepTime,
		signTime,
		broadcastTime,
		totalTime,
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
) (successfulTxs, failedTxs int, responseCodes map[uint32]int, updatedSequence uint64) {
	successfulTxs = 0
	failedTxs = 0
	responseCodes = make(map[uint32]int)
	sequence := txParams.Sequence

	// Log the start of broadcasting for this position
	LogVisualizerDebug(fmt.Sprintf("Starting broadcasts for position %d (batchSize: %d)", position, batchSize))

	for i := 0; i < batchSize; i++ {
		currentSequence := sequence
		metrics := &TimingMetrics{
			PrepStart: time.Now(),
			Position:  position,
		}

		metrics.SignStart = time.Now()
		metrics.BroadStart = time.Now()
		resp, _, err := SendTransactionViaRPC(context.Background(), txParams, currentSequence)
		metrics.Complete = time.Now()

		// Calculate total transaction time for visualization
		txLatency := metrics.Complete.Sub(metrics.PrepStart)

		if err != nil {
			metrics.LogTiming(currentSequence, false, err)
			failedTxs++

			// Update visualizer with failed tx
			UpdateVisualizerStats(0, 1, txLatency)

			if resp != nil && resp.Code == 32 {
				newSeq, success, newResp := handleSequenceMismatch(txParams, position, sequence, err)
				sequence = newSeq
				if success {
					successfulTxs++
					responseCodes[newResp.Code]++

					// Update visualizer with successful tx after sequence recovery
					UpdateVisualizerStats(1, 0, metrics.Complete.Sub(metrics.PrepStart))
				}
				continue
			}
			continue
		}

		metrics.LogTiming(currentSequence, true, nil)
		successfulTxs++
		responseCodes[resp.Code]++
		sequence++

		// Update visualizer with successful tx
		UpdateVisualizerStats(1, 0, txLatency)
	}

	// Log the completion of broadcasting for this position
	LogVisualizerDebug(fmt.Sprintf("Completed broadcasts for position %d: %d successful, %d failed",
		position, successfulTxs, failedTxs))

	updatedSequence = sequence
	return successfulTxs, failedTxs, responseCodes, updatedSequence
}

// handleSequenceMismatch handles the case where a transaction fails due to sequence mismatch
func handleSequenceMismatch(txParams types.TransactionParams, position int, sequence uint64, err error) (uint64, bool, *coretypes.ResultBroadcastTx) {
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
	resp, _, err := SendTransactionViaRPC(context.Background(), txParams, expectedSeq)
	metrics.Complete = time.Now()

	if err != nil {
		metrics.LogTiming(expectedSeq, false, err)
		return expectedSeq, false, nil
	}

	metrics.LogTiming(expectedSeq, true, nil)
	return expectedSeq + 1, true, resp
}
