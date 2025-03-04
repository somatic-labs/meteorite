package broadcast

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
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
	startTime := time.Now()

	// Log the start of broadcasting for this position
	log.Printf("[POS-%d] Starting broadcasts for position %d (batchSize: %d, node: %s)",
		position, position, batchSize, txParams.NodeURL)

	for i := 0; i < batchSize; i++ {
		txStartTime := time.Now()
		currentSequence := sequence
		metrics := &TimingMetrics{
			PrepStart: time.Now(),
			Position:  position,
		}

		metrics.SignStart = time.Now()
		metrics.BroadStart = time.Now()
		resp, _, err := SendTransactionViaRPC(context.Background(), txParams, currentSequence)
		metrics.Complete = time.Now()
		txDuration := metrics.Complete.Sub(txStartTime)

		// Calculate total transaction time for visualization
		txLatency := metrics.Complete.Sub(metrics.PrepStart)

		if err != nil {
			// More detailed error logging
			errMsg := err.Error()
			if len(errMsg) > 100 {
				errMsg = errMsg[:100] + "..." // Truncate long errors
			}

			log.Printf("[POS-%d] TX FAILED seq=%d node=%s error=\"%s\" time=%v",
				position, currentSequence, txParams.NodeURL, errMsg, txDuration)

			metrics.LogTiming(currentSequence, false, err)
			failedTxs++

			// Update visualizer with failed tx
			UpdateVisualizerStats(0, 1, txLatency)

			// Handle specific error types
			if resp != nil && resp.Code == 32 {
				// Sequence mismatch
				newSeq, success, newResp := handleSequenceMismatch(txParams, position, sequence, err)
				sequence = newSeq
				if success {
					successfulTxs++
					responseCodes[newResp.Code]++
					log.Printf("[POS-%d] Recovered from sequence mismatch! New seq=%d", position, newSeq)

					// Update visualizer with successful tx after sequence recovery
					UpdateVisualizerStats(1, 0, metrics.Complete.Sub(metrics.PrepStart))
				}
				continue
			}

			// For other specific error codes, check if we should continue or abort this batch
			if resp != nil {
				log.Printf("[POS-%d] Response code %d from node %s", position, resp.Code, txParams.NodeURL)
				responseCodes[resp.Code]++

				// If we get multiple fee errors in a row, we might need to adjust fees
				if resp.Code == 13 { // Insufficient fee error code
					if i < 3 { // Only log aggressive warning for early failures
						log.Printf("[POS-%d] WARNING: Insufficient fee error from node %s. Consider increasing fees.",
							position, txParams.NodeURL)
					}
				}
			}

			continue
		}

		metrics.LogTiming(currentSequence, true, nil)
		log.Printf("[POS-%d] TX SUCCESS seq=%d node=%s code=%d time=%v",
			position, currentSequence, txParams.NodeURL, resp.Code, txDuration)

		successfulTxs++
		responseCodes[resp.Code]++
		sequence++

		// Update visualizer with successful tx
		UpdateVisualizerStats(1, 0, txLatency)

		// Add a small delay between transactions to avoid overwhelming nodes
		// This helps with mempool congestion
		if i < batchSize-1 {
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Calculate success rate and total time
	totalTxs := successfulTxs + failedTxs
	successRate := 0.0
	if totalTxs > 0 {
		successRate = float64(successfulTxs) / float64(totalTxs) * 100
	}
	totalTime := time.Since(startTime)
	txPerSecond := float64(totalTxs) / totalTime.Seconds()

	// Log the completion of broadcasting with detailed stats
	log.Printf("[POS-%d] BATCH COMPLETE: %d/%d successful (%.1f%%), %.1f tx/sec, time=%v, node=%s, codes=%v",
		position, successfulTxs, totalTxs, successRate, txPerSecond, totalTime, txParams.NodeURL, responseCodes)

	// Log the completion of broadcasting for visualization
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

// ParallelBroadcast broadcasts transactions in parallel across multiple nodes
// This dramatically improves throughput by utilizing all available RPC nodes
func ParallelBroadcast(
	accounts []types.Account,
	config types.Config,
	chainID string,
	txsPerAccount int,
) (int, int) {
	// Validate inputs
	if len(accounts) == 0 {
		log.Printf("Error: No accounts provided for parallel broadcast")
		return 0, 0
	}

	if len(config.Nodes.RPC) == 0 {
		log.Printf("Error: No RPC nodes configured for parallel broadcast")
		return 0, 0
	}

	startTime := time.Now()
	log.Printf("🚀 Starting ultra-high-speed parallel broadcast across %d accounts and %d nodes",
		len(accounts), len(config.Nodes.RPC))
	log.Printf("Transaction type: %s", config.MsgType)

	// Track nodes that are known to be down
	badNodesMap := make(map[string]bool)
	var badNodesMutex sync.RWMutex

	// Get healthy nodes only
	healthyNodes := GetHealthyNodesOnly(config.Nodes.RPC)
	if len(healthyNodes) == 0 {
		log.Printf("Warning: No healthy nodes found, will attempt to use all configured nodes")
		healthyNodes = config.Nodes.RPC
	}

	// Create position to node mapping
	positionToNode := make(map[int]string)

	// Create a mapping of account address to Account for easy lookup
	accountMap := make(map[string]types.Account)
	for _, acct := range accounts {
		accountMap[acct.Address] = acct
	}

	// Track sequence numbers per node per account
	type accountNodeKey struct {
		address string
		nodeURL string
	}
	sequenceMap := make(map[accountNodeKey]uint64)
	var sequenceMutex sync.RWMutex

	// Track destination addresses for IBC transactions if needed
	destinationAddresses := make(map[string]string)

	// Cache for IBC channel info
	ibcChannelCache := make(map[string]string)
	var ibcCacheMutex sync.RWMutex

	// Create synchronization primitives
	var wg sync.WaitGroup
	var successMutex sync.Mutex
	totalSuccessful := 0
	totalFailed := 0

	// Increase parallelism substantially for higher throughput
	maxConcurrentPositions := len(healthyNodes) * 6 // 6 positions per node for even more concurrency
	if maxConcurrentPositions > len(accounts) {
		maxConcurrentPositions = len(accounts)
	}

	log.Printf("Using %d concurrent transaction senders", maxConcurrentPositions)

	// Use a worker pool model for maximum throughput
	positionsToProcess := make(chan int, len(accounts))

	// Fill the channel with positions to process
	for pos := 0; pos < len(accounts); pos++ {
		positionsToProcess <- pos
	}
	close(positionsToProcess)

	// Launch worker pool
	for i := 0; i < maxConcurrentPositions; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Each worker processes positions from the channel
			for position := range positionsToProcess {
				// Assign this position to a node
				nodeIdx := position % len(healthyNodes)
				nodeURL := healthyNodes[nodeIdx]

				// Store the assignment
				positionToNode[position] = nodeURL

				// Get the account for this position
				account := accounts[position]

				// Get or initialize sequence
				sequenceMutex.RLock()
				key := accountNodeKey{address: account.Address, nodeURL: nodeURL}
				sequence, hasSequence := sequenceMap[key]
				sequenceMutex.RUnlock()

				if !hasSequence {
					// Get account info using the assigned node
					initialSeq, _, err := lib.GetAccountInfo(account.Address, config)
					if err != nil {
						log.Printf("[Worker %d] Failed to get account info for %s from node %s: %v",
							workerID, account.Address, nodeURL, err)

						// Mark this node as bad
						badNodesMutex.Lock()
						badNodesMap[nodeURL] = true
						badNodesMutex.Unlock()

						// Try to find a new node
						badNodesMutex.RLock()
						availableNodes := make([]string, 0)
						for _, node := range healthyNodes {
							if !badNodesMap[node] {
								availableNodes = append(availableNodes, node)
							}
						}
						badNodesMutex.RUnlock()

						if len(availableNodes) == 0 {
							log.Printf("[Worker %d] No available nodes, skipping position %d", workerID, position)
							continue
						}

						// Reassign to a new node
						newNodeIdx := position % len(availableNodes)
						nodeURL = availableNodes[newNodeIdx]
						positionToNode[position] = nodeURL

						// Try again with the new node
						initialSeq, _, err = lib.GetAccountInfo(account.Address, config)
						if err != nil {
							log.Printf("[Worker %d] Still failed to get account info after node reassignment: %v", workerID, err)
							continue
						}
					}

					// Store the sequence
					sequenceMutex.Lock()
					sequenceMap[key] = initialSeq
					sequenceMutex.Unlock()

					sequence = initialSeq
				}

				// Prepare transaction parameters
				txParams := types.TransactionParams{
					Config:      config,
					NodeURL:     nodeURL,
					ChainID:     chainID,
					PrivKey:     account.PrivKey,
					AcctAddress: account.Address,
					AccNum:      0, // Will be fetched during tx building
					Sequence:    sequence,
					MsgType:     config.MsgType,
					MsgParams: map[string]interface{}{
						"from_address": account.Address,
						"amount":       1000, // Starting with a very low amount
						"denom":        config.Denom,
					},
				}

				// Set up transaction parameters based on message type
				switch config.MsgType {
				case "bank_send":
					// Use the next account as receiver, wrap around if needed
					receiverIdx := (position + 1) % len(accounts)
					receiver := accounts[receiverIdx].Address
					txParams.MsgParams["to_address"] = receiver

				case "multisend":
					// Set up multisend parameters
					// Send to multiple receivers (up to 10)
					recipients := make([]map[string]interface{}, 0, 5)
					for i := 1; i <= 5; i++ {
						recipientIdx := (position + i) % len(accounts)
						recipient := accounts[recipientIdx].Address
						recipients = append(recipients, map[string]interface{}{
							"address": recipient,
							"amount":  200, // Send a small amount to each recipient
							"denom":   config.Denom,
						})
					}
					txParams.MsgParams["recipients"] = recipients

				case "ibc_transfer":
					// Check if we have a cached destination address
					destAddr, exists := destinationAddresses[account.Address]
					if !exists {
						// Generate a random destination address if not using real ones
						destAddr = fmt.Sprintf("cosmos1random%d", position)
						destinationAddresses[account.Address] = destAddr
					}

					// Get or determine IBC channel
					ibcCacheMutex.RLock()
					channelID, channelExists := ibcChannelCache[config.Chain]
					ibcCacheMutex.RUnlock()

					if !channelExists {
						// Let's use either a configured channel or a reasonable default
						channelID = "channel-0" // Default fallback

						// Cache it
						ibcCacheMutex.Lock()
						ibcChannelCache[config.Chain] = channelID
						ibcCacheMutex.Unlock()
					}

					// Set up IBC transfer parameters
					txParams.MsgParams["to_address"] = destAddr
					txParams.MsgParams["source_channel"] = channelID
					txParams.MsgParams["source_port"] = "transfer"
					txParams.MsgParams["timeout_height"] = "0-0"                                          // No timeout height
					txParams.MsgParams["timeout_timestamp"] = time.Now().Add(10 * time.Minute).UnixNano() // 10 min timeout
				}

				// Send transactions aggressively
				successful := 0
				failed := 0

				// Launch multiple concurrent transactions for this position
				var txWg sync.WaitGroup
				const concurrentTxsPerPosition = 10 // Increased from 5 to 10 for more speed

				for batch := 0; batch < txsPerAccount; batch += concurrentTxsPerPosition {
					// Calculate how many transactions to send in this batch
					remainingTxs := txsPerAccount - batch
					batchSize := concurrentTxsPerPosition
					if remainingTxs < batchSize {
						batchSize = remainingTxs
					}

					if batchSize <= 0 {
						break
					}

					// Send transactions in this batch concurrently
					for txOffset := 0; txOffset < batchSize; txOffset++ {
						txWg.Add(1)

						go func(txNum int) {
							defer txWg.Done()

							// Create a local copy of transaction parameters
							localParams := txParams

							// Calculate sequence for this transaction
							sequenceMutex.RLock()
							currentSequence := sequenceMap[key]
							sequenceMutex.RUnlock()

							// Update sequence for this tx
							localParams.Sequence = currentSequence + uint64(txNum)

							// Create a timeout context for this transaction - shorter timeout for faster feedback
							ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
							defer cancel()

							// Send the transaction
							resp, err := SendTx(ctx, localParams, localParams.Sequence, config.BroadcastMode)

							if err != nil {
								successMutex.Lock()
								failed++
								totalFailed++
								successMutex.Unlock()

								// Only log errors occasionally to reduce spam
								if failed%20 == 0 {
									log.Printf("[Worker %d] Position %d: Error on tx %d: %v",
										workerID, position, txNum, err)
								}

								// Check if this was a node failure
								if strings.Contains(err.Error(), "context deadline exceeded") ||
									strings.Contains(err.Error(), "connection refused") ||
									strings.Contains(err.Error(), "EOF") {
									// Mark this node as bad
									badNodesMutex.Lock()
									badNodesMap[nodeURL] = true
									badNodesMutex.Unlock()
								}

								return
							}

							// Update node performance metrics
							UpdateNodePerformance(nodeURL, true, time.Since(startTime))

							if resp != nil && resp.Code == 0 {
								successMutex.Lock()
								successful++
								totalSuccessful++
								successMutex.Unlock()

								// Update sequence
								sequenceMutex.Lock()
								if localParams.Sequence >= sequenceMap[key] {
									sequenceMap[key] = localParams.Sequence + 1
								}
								sequenceMutex.Unlock()

								// Only log successes occasionally to reduce spam
								if successful%100 == 0 {
									successRate := float64(successful) / float64(successful+failed) * 100
									log.Printf("[Worker %d] Position %d: %d/%d successful (%.1f%%)",
										workerID, position, successful, successful+failed, successRate)
								}
							} else {
								successMutex.Lock()
								failed++
								totalFailed++
								successMutex.Unlock()
							}
						}(txOffset)
					}

					// Wait a very short time between batches to avoid overwhelming the node
					// Reduced from 10ms to 5ms for more speed
					time.Sleep(5 * time.Millisecond)
				}

				// Wait for all transactions for this position to complete
				txWg.Wait()

				// Log completion for this position
				successRate := 0.0
				if successful+failed > 0 {
					successRate = float64(successful) / float64(successful+failed) * 100
				}

				log.Printf("[Worker %d] Position %d complete: %d successful, %d failed (%.1f%%)",
					workerID, position, successful, failed, successRate)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Calculate duration and transactions per second
	duration := time.Since(startTime)
	var tps float64
	if duration.Seconds() > 0 {
		tps = float64(totalSuccessful) / duration.Seconds()
	}

	// Log overall statistics
	log.Printf("🚀 Ultra high-speed parallel broadcast complete: %d successful, %d failed in %v (%.2f TPS)",
		totalSuccessful, totalFailed, duration, tps)

	// Log per-node success rates
	log.Printf("Node performance summary:")
	for _, node := range config.Nodes.RPC {
		perf := GetOrCreateNodePerformance(node)
		total := perf.SuccessCount + perf.FailureCount
		successRate := 0.0
		if total > 0 {
			successRate = float64(perf.SuccessCount) / float64(total) * 100
		}
		log.Printf("  Node %s: %d/%d txs (%.1f%%), avg latency: %v",
			node, perf.SuccessCount, total, successRate, perf.AverageLatency)
	}

	return totalSuccessful, totalFailed
}
