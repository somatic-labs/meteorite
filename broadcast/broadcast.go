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

// handleSequenceMismatch handles the case where a transaction fails due to sequence mismatch
func handleSequenceMismatch(txParams types.TransactionParams, position int, sequence uint64, err error) (uint64, bool, *coretypes.ResultBroadcastTx) {
	expectedSeq, parseErr := lib.ExtractExpectedSequence(err.Error())
	if parseErr != nil {
		fmt.Printf("[POS-%d] Failed to parse expected sequence: %v\n", position, parseErr)
		return sequence, false, nil
	}

	// Get sequence manager to track node-specific sequences
	sequenceManager := lib.GetSequenceManager()

	// Use the expected sequence from the error message
	nodeURL := txParams.NodeURL
	sequenceManager.SetSequence(txParams.AcctAddress, nodeURL, expectedSeq)

	fmt.Printf("[POS-%d] Set sequence to expected value %d for node %s due to mismatch\n",
		position, expectedSeq, nodeURL)

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

	// Initialize and configure the NodeSelector
	nodeSelector := GetNodeSelector()
	if len(txParams.Config.Nodes.RPC) > 0 {
		// Set all available RPC nodes
		nodeSelector.SetNodes(txParams.Config.Nodes.RPC)
	}

	// Get sequence manager to use node-specific sequence tracking
	sequenceManager := lib.GetSequenceManager()

	// We'll track sequences per node in this map
	nodeSequences := make(map[string]uint64)
	for _, nodeURL := range txParams.Config.Nodes.RPC {
		// Get the node-specific sequence for each available node
		nodeSequence, err := sequenceManager.GetSequence(txParams.AcctAddress, nodeURL, txParams.Config, false)
		if err == nil {
			nodeSequences[nodeURL] = nodeSequence
			// Use the highest sequence value we find as our starting point
			if nodeSequence > sequence {
				sequence = nodeSequence
				fmt.Printf("[POS-%d] Updated starting sequence to %d based on node %s\n",
					position, sequence, nodeURL)
			}
		} else {
			// If we can't get a sequence, default to the provided one
			nodeSequences[nodeURL] = sequence
		}
	}

	// Log the start of broadcasting for this position
	LogVisualizerDebug(fmt.Sprintf("Starting broadcasts for position %d (batchSize: %d) across %d nodes",
		position, batchSize, len(txParams.Config.Nodes.RPC)))

	for i := 0; i < batchSize; i++ {
		// Get the next node to use for this transaction
		nodeURL := nodeSelector.GetNextNode(uint32(position))
		if nodeURL == "" {
			// Fallback to the primary node if node selection fails
			nodeURL = txParams.NodeURL
		}

		// Get the current sequence for this node
		currentSequence := nodeSequences[nodeURL]

		// Log which node we're using for this transaction
		fmt.Printf("[POS-%d] Using node %s for transaction %d/%d with sequence %d\n",
			position, nodeURL, i+1, batchSize, currentSequence)

		// Create a copy of txParams with the selected node
		nodeTxParams := txParams
		nodeTxParams.NodeURL = nodeURL

		metrics := &TimingMetrics{
			PrepStart: time.Now(),
			Position:  position,
		}

		metrics.SignStart = time.Now()
		metrics.BroadStart = time.Now()
		resp, _, err := SendTransactionViaRPC(context.Background(), nodeTxParams, currentSequence)
		metrics.Complete = time.Now()

		// Calculate total transaction time for visualization
		txLatency := metrics.Complete.Sub(metrics.PrepStart)

		if err != nil {
			metrics.LogTiming(currentSequence, false, err)
			failedTxs++

			// Update visualizer with failed tx
			UpdateVisualizerStats(0, 1, txLatency)

			if resp != nil && resp.Code == 32 {
				newSeq, success, newResp := handleSequenceMismatch(nodeTxParams, position, currentSequence, err)
				// Update the sequence for this specific node
				nodeSequences[nodeURL] = newSeq
				if success {
					successfulTxs++
					responseCodes[newResp.Code]++

					// Record usage for successful sequence recovery
					nodeSelector.RecordUsage(nodeURL)

					// Update visualizer with successful tx after sequence recovery
					UpdateVisualizerStats(1, 0, metrics.Complete.Sub(metrics.PrepStart))
				}
				continue
			}
			continue
		}

		// Print transaction hash on success
		fmt.Printf("[POS-%d] Transaction successful! TxID: %s (Node: %s)\n", position, resp.Hash.String(), nodeURL)

		metrics.LogTiming(currentSequence, true, nil)
		successfulTxs++
		responseCodes[resp.Code]++

		// Record usage for this node
		nodeSelector.RecordUsage(nodeURL)

		// Update the sequence for this specific node
		newSequence := currentSequence + 1
		nodeSequences[nodeURL] = newSequence
		sequenceManager.SetSequence(txParams.AcctAddress, nodeURL, newSequence)

		// Update visualizer with successful tx
		UpdateVisualizerStats(1, 0, txLatency)
	}

	// Find the highest sequence used across all nodes to return
	highestSequence := sequence
	for _, seq := range nodeSequences {
		if seq > highestSequence {
			highestSequence = seq
		}
	}

	// Log node usage statistics for visibility
	nodeUsage := nodeSelector.GetNodeUsage()
	fmt.Printf("[POS-%d] Node usage: %v\n", position, nodeUsage)

	// Log the completion of broadcasting for this position
	LogVisualizerDebug(fmt.Sprintf("Completed broadcasts for position %d: %d successful, %d failed across nodes",
		position, successfulTxs, failedTxs))

	updatedSequence = highestSequence
	return successfulTxs, failedTxs, responseCodes, updatedSequence
}
