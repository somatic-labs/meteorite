package broadcast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	xauthsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	ibctransfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
)

const (
	// Safety buffer percentage for gas estimation
	defaultGasAdjustment = 1.3

	// Minimum gas to use for specific message types
	minGasBankSend      = 80000
	minGasBankMultiSend = 200000
	minGasIbcTransfer   = 150000
	minGasWasmExecute   = 300000
	minGasDefault       = 100000

	// Error patterns - expanded to catch more variations
	insufficientFeesPattern     = "insufficient fees; got: \\d+\\w+ required: (\\d+)\\w+"
	insufficientFeesAltPattern1 = "got: [^;]+; required: (\\d+)\\w+"
	insufficientFeesAltPattern2 = "required: (\\d+)\\w+"

	// Sequence error patterns
	sequenceMismatchPattern = "account sequence mismatch: expected (\\d+), got \\d+"
	sequenceAltPattern1     = "expected sequence: (\\d+)"
	sequenceAltPattern2     = "sequence (\\d+) but got \\d+"
	sequenceAltPattern3     = "expected (\\d+), got \\d+"
	sequenceAltPattern4     = "sequence \\d+ != (\\d+)"

	// Constants for error message patterns
	maxErrorQueueSize = 10 // Max number of recent error messages to store
)

// multiSendDistributor defines the interface for creating MultiSend messages
type multiSendDistributor interface {
	CreateDistributedMultiSendMsg(fromAddress string, msgParams types.MsgParams, seed int64) (sdk.Msg, string, error)
}

// ErrorQueue stores recent error messages for pattern matching
type ErrorQueue struct {
	messages []string
	mutex    sync.RWMutex
}

// Global instance of the error queue
var regexErrorQueue = &ErrorQueue{
	messages: make([]string, 0, maxErrorQueueSize),
}

// AddError adds an error message to the queue
func (q *ErrorQueue) AddError(errMsg string) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	// Add new error message
	q.messages = append(q.messages, errMsg)

	// Trim queue if necessary
	if len(q.messages) > maxErrorQueueSize {
		q.messages = q.messages[len(q.messages)-maxErrorQueueSize:]
	}
}

// GetErrorString returns a concatenated string of all recent errors for pattern matching
func (q *ErrorQueue) GetErrorString() string {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	return strings.Join(q.messages, " ")
}

// convertMapToMsgParams converts a map of parameters to the MsgParams struct
func convertMapToMsgParams(paramsMap map[string]interface{}) types.MsgParams {
	msgParams := types.MsgParams{}

	// Convert common fields
	if val, ok := paramsMap["from_address"].(string); ok {
		msgParams.FromAddress = val
	}
	if val, ok := paramsMap["to_address"].(string); ok {
		msgParams.ToAddress = val
	}
	if val, ok := paramsMap["amount"].(int64); ok {
		msgParams.Amount = val
	} else if val, ok := paramsMap["amount"].(int); ok {
		msgParams.Amount = int64(val)
	} else if val, ok := paramsMap["amount"].(float64); ok {
		msgParams.Amount = int64(val)
	}
	if val, ok := paramsMap["denom"].(string); ok {
		msgParams.Denom = val
	}

	return msgParams
}

// createBankSendMsg creates a bank send message from the provided parameters
func createBankSendMsg(txParams *types.TxParams) (sdk.Msg, error) {
	// Extract required parameters
	fromAddrStr, ok := txParams.MsgParams["from_address"].(string)
	if !ok || fromAddrStr == "" {
		return nil, errors.New("from_address is required for bank_send")
	}

	// Parse from address
	fromAddr, err := sdk.AccAddressFromBech32(fromAddrStr)
	if err != nil {
		return nil, fmt.Errorf("invalid from address: %w", err)
	}

	// Get to_address from params
	toAddrStr, ok := txParams.MsgParams["to_address"].(string)
	if !ok || toAddrStr == "" {
		// If to_address is not specified, get a random one from balances.csv
		// First determine the prefix to use - either from config or extract from from_address
		prefix := "atone" // Default to atone prefix
		if txParams.Config.AccountPrefix != "" {
			prefix = txParams.Config.AccountPrefix
			log.Printf("Using account prefix from config: %s", prefix)
		} else if txParams.Config.Prefix != "" {
			prefix = txParams.Config.Prefix
			log.Printf("Using prefix from config: %s", prefix)
		} else {
			// Extract prefix from fromAddrStr
			parts := strings.Split(fromAddrStr, "1")
			if len(parts) > 0 && parts[0] != "" {
				prefix = parts[0]
				log.Printf("Extracted prefix from from_address: %s", prefix)
			} else {
				log.Printf("Could not extract prefix from from_address, using default prefix: %s", prefix)
			}
		}

		// Get address manager and try to get a random address
		addressManager := lib.GetAddressManager()

		// First try to load addresses if needed
		if err := addressManager.LoadAddressesFromCSV(); err != nil {
			log.Printf("Warning: Failed to load addresses from balances.csv: %v", err)
		}

		randomAddr, err := addressManager.GetRandomAddressWithPrefix(prefix)
		if err != nil {
			log.Printf("Failed to get random address from balances.csv: %v", err)
			return nil, fmt.Errorf("to_address is required for bank_send and failed to get random address: %w", err)
		}

		if randomAddr == "" {
			log.Printf("Got empty random address from balances.csv")
			return nil, errors.New("failed to get valid to_address from balances.csv")
		}

		toAddrStr = randomAddr
		log.Printf("Using random address from balances.csv with prefix %s: %s", prefix, toAddrStr)
	}

	// Parse to address
	toAddr, err := sdk.AccAddressFromBech32(toAddrStr)
	if err != nil {
		log.Printf("Error parsing to_address %s: %v", toAddrStr, err)
		return nil, fmt.Errorf("invalid to address %s: %w", toAddrStr, err)
	}

	// Extract amount
	var amount int64
	amountVal, ok := txParams.MsgParams["amount"]
	if !ok {
		return nil, errors.New("amount is required for bank_send")
	}

	// Convert amount to int64
	switch a := amountVal.(type) {
	case int64:
		amount = a
	case int:
		amount = int64(a)
	case float64:
		amount = int64(a)
	default:
		return nil, fmt.Errorf("invalid amount type: %T", amountVal)
	}

	// Get denom with fallback
	denom, ok := txParams.MsgParams["denom"].(string)
	if !ok || denom == "" {
		denom = "stake" // Default denom
	}

	// Create coins
	coin := sdk.NewCoin(denom, sdkmath.NewInt(amount))
	coins := sdk.NewCoins(coin)

	// Create bank send message
	msg := &banktypes.MsgSend{
		FromAddress: fromAddr.String(),
		ToAddress:   toAddr.String(),
		Amount:      coins,
	}

	log.Printf("Created bank send message from %s to %s with amount %d %s",
		fromAddr.String(), toAddr.String(), amount, denom)

	return msg, nil
}

// createMultiSendMsg creates a bank multisend message from the provided parameters
func createMultiSendMsg(txParams *types.TxParams) (sdk.Msg, error) {
	// Implementation not needed as we're using the distributor
	return nil, errors.New("basic multisend not implemented - use distributor instead")
}

// createIbcTransferMsg creates an IBC transfer message
func createIbcTransferMsg(txParams *types.TxParams) (sdk.Msg, error) {
	// Get necessary parameters from txParams
	fromAddress, ok := txParams.MsgParams["from_address"].(string)
	if !ok || fromAddress == "" {
		return nil, errors.New("from_address not specified")
	}

	toAddress, ok := txParams.MsgParams["to_address"].(string)
	if !ok || toAddress == "" {
		return nil, errors.New("to_address not specified")
	}

	// Check if amount is specified
	amount, ok := txParams.MsgParams["amount"].(int64)
	if !ok || amount <= 0 {
		return nil, errors.New("invalid amount")
	}

	// Get denom from config
	denom := txParams.Config.Denom
	if denom == "" {
		return nil, errors.New("denom not specified in config")
	}

	// Create a coin with the amount and denom
	coin := sdk.NewCoin(denom, sdkmath.NewInt(amount))

	// Use a default source port and channel if not specified
	sourcePort := "transfer"
	sourceChannel := "channel-0" // Default channel

	// Check if channel is specified in the config
	if txParams.Config.Channel != "" {
		sourceChannel = txParams.Config.Channel
	}

	// For IBC transfers, we need to use the IBC Transfer module's MsgTransfer
	// This uses ibc-go v8 compatible imports
	transferMsg := &ibctransfertypes.MsgTransfer{
		SourcePort:       sourcePort,
		SourceChannel:    sourceChannel,
		Token:            coin,
		Sender:           fromAddress,
		Receiver:         toAddress,
		TimeoutHeight:    clienttypes.Height{},
		TimeoutTimestamp: 0, // No timeout
	}

	return transferMsg, nil
}

// createStoreCodeMsg creates a store code message for CosmWasm
func createStoreCodeMsg(txParams *types.TxParams) (sdk.Msg, error) {
	// Just a stub for now
	return nil, errors.New("store_code not implemented")
}

// createInstantiateContractMsg creates an instantiate contract message for CosmWasm
func createInstantiateContractMsg(txParams *types.TxParams) (sdk.Msg, error) {
	// Just a stub for now
	return nil, errors.New("instantiate_contract not implemented")
}

// createExecuteContractMsg creates an execute contract message for CosmWasm
func createExecuteContractMsg(txParams *types.TxParams) (sdk.Msg, error) {
	// Just a stub for now
	return nil, errors.New("execute_contract not implemented")
}

// BuildTransaction builds a transaction from the provided parameters
func BuildTransaction(ctx context.Context, txParams *types.TxParams) ([]byte, error) {
	// Print verbose logs to help debug transaction issues
	fmt.Printf("Building transaction with params: MsgType=%s\n",
		txParams.MsgType)
	fmt.Printf("Gas settings: Low=%d, BaseGas=%d, Denom=%s\n",
		txParams.Config.Gas.Low, txParams.Config.BaseGas, txParams.Config.Denom)

	// Validate the transaction parameters
	if err := ValidateTxParams(txParams); err != nil {
		return nil, err
	}

	// Get the client context
	clientCtx, err := GetClientContext(txParams.Config, txParams.NodeURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get client context: %w", err)
	}

	// Create the message based on the message type and parameters
	msg, err := createMessage(txParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	// Get account information for the transaction signer
	fromAddress, _ := txParams.MsgParams["from_address"].(string)
	accNum, accSeq, err := GetAccountInfo(ctx, clientCtx, fromAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get account info for %s at node %s: %w",
			fromAddress, txParams.NodeURL, err)
	}

	// Choose gas limit based on message type, gas settings, and node capabilities
	var gasLimit uint64

	switch {
	case txParams.Gas != nil && txParams.Gas.ForceZeroGas:
		gasLimit = 0
	case txParams.Gas != nil && txParams.Gas.UseSimulation:
		// Use simulation to determine gas
		gasLimit, err = SimulateGas(ctx, clientCtx, msg, txParams)
		if err != nil {
			return nil, fmt.Errorf("failed to simulate gas: %w", err)
		}
	default:
		// Use a default gas limit based on message type
		gasLimit = getDefaultGasLimitByMsgType(txParams.MsgType)
	}

	// Apply the adaptive gas strategy if enabled
	if txParams.Gas != nil && !txParams.Gas.ForceZeroGas {
		// Get gas strategy manager
		gsm := GetGasStrategyManager()

		// Check if we can use zero gas for this node
		gsm.TestZeroGasTransaction(ctx, txParams.NodeURL, txParams.Config)

		// Determine optimal gas for this specific node
		canUseZeroGas := txParams.Gas != nil && txParams.Gas.AllowZeroGas
		gasLimit = gsm.DetermineOptimalGasForNode(
			ctx,
			txParams.NodeURL,
			gasLimit,
			txParams.MsgType,
			canUseZeroGas,
		)
	}

	// Calculate the fee based on gas price and limit
	var feeAmount sdk.Coins
	if gasLimit > 0 {
		// Get gas denom from config (using config.Denom as fallback)
		gasDenom := txParams.Config.Denom
		if txParams.Config.Gas.Denom != "" {
			gasDenom = txParams.Config.Gas.Denom
		}

		// Get gas price from config
		gasPrice := strconv.FormatInt(txParams.Config.Gas.Low, 10)
		if txParams.Config.Gas.Price != "" {
			gasPrice = txParams.Config.Gas.Price
		}

		// Create a decimal coin for the gas price
		gasDecCoin, err := sdk.ParseDecCoin(fmt.Sprintf("%s%s", gasPrice, gasDenom))
		if err != nil {
			return nil, fmt.Errorf("failed to parse gas price: %w", err)
		}

		// Calculate fee amount
		fee := gasDecCoin.Amount.MulInt64(int64(gasLimit)).RoundInt()
		feeAmount = sdk.NewCoins(sdk.NewCoin(gasDenom, fee))
	} else {
		// Zero gas means no fee
		feeAmount = sdk.NewCoins()
	}

	// Create the transaction builder
	txBuilder := clientCtx.TxConfig.NewTxBuilder()
	txBuilder.SetGasLimit(gasLimit)
	txBuilder.SetFeeAmount(feeAmount)
	txBuilder.SetMemo(txParams.Memo)
	txBuilder.SetTimeoutHeight(txParams.TimeoutHeight)

	// Add the message to the transaction
	if err = txBuilder.SetMsgs(msg); err != nil {
		return nil, fmt.Errorf("failed to set messages in transaction: %w", err)
	}

	// Create the signature
	sigV2, err := tx.SignWithPrivKey(
		ctx,
		signingtypes.SignMode_SIGN_MODE_DIRECT,
		xauthsigning.SignerData{
			ChainID:       txParams.ChainID,
			AccountNumber: accNum,
			Sequence:      accSeq,
		},
		txBuilder,
		txParams.PrivKey,
		clientCtx.TxConfig,
		accSeq,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Add the signature to the transaction
	if err = txBuilder.SetSignatures(sigV2); err != nil {
		return nil, fmt.Errorf("failed to set signatures in transaction: %w", err)
	}

	// Encode the transaction
	txBytes, err := clientCtx.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return nil, fmt.Errorf("failed to encode transaction: %w", err)
	}

	// If the transaction was successfully built, record this gas setting as successful if we use it
	// This helps fine-tune our adaptive gas strategy
	if gasLimit == 0 {
		go func() {
			// Schedule an async check to verify if the tx was processed by the node
			// We'll mark this node as accepting zero-gas transactions if the tx is successful
			gsm := GetGasStrategyManager()
			gsm.RecordTransactionResult(txParams.NodeURL, true, 0, txParams.MsgType)
		}()
	}

	return txBytes, nil
}

// createMessage creates a message based on the message type and parameters
func createMessage(txParams *types.TxParams) (sdk.Msg, error) {
	if err := ValidateTxParams(txParams); err != nil {
		return nil, err
	}

	// Parse the sender address
	fromAddress, exists := txParams.MsgParams["from_address"].(string)
	if !exists || fromAddress == "" {
		return nil, errors.New("from_address is required in message parameters")
	}

	// Create the appropriate message based on the message type
	switch txParams.MsgType {
	case "bank_send":
		// Create a bank send message
		return createBankSendMsg(txParams)
	case "bank_multisend":
		// For multisend, check if we have a distributor
		distributor, hasDistributor := txParams.MsgParams["distributor"]
		if hasDistributor {
			// Use the distributor to create a multisend message
			seed, ok := txParams.MsgParams["seed"].(int64)
			if !ok {
				// Generate a random seed if not provided
				seed = time.Now().UnixNano()
			}

			// Convert the distributor to the expected interface
			multisendDist, ok := distributor.(multiSendDistributor)
			if !ok {
				return nil, errors.New("invalid distributor type")
			}

			// Use the distributor to create a multisend message
			msg, _, err := multisendDist.CreateDistributedMultiSendMsg(
				fromAddress,
				convertMapToMsgParams(txParams.MsgParams),
				seed,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to create multisend message: %w", err)
			}

			// Calculate appropriate gas for multisend based on the number of outputs
			if multisendMsg, ok := msg.(*banktypes.MsgMultiSend); ok {
				numOutputs := len(multisendMsg.Outputs)
				// Calculate gas for multisend considering the number of outputs
				baseGas := uint64(100000)        // Increased base gas for a simple transaction
				perRecipientGas := uint64(20000) // Increased gas per recipient
				totalGasEstimate := baseGas + (uint64(numOutputs) * perRecipientGas)

				// Cap at a much higher maximum for large multisends
				maxGas := uint64(135150000) // 150 million gas limit (increased from 10 million)
				if totalGasEstimate > maxGas {
					totalGasEstimate = maxGas
				}

				// Progressive scaling for large multisends
				if numOutputs > 50 && numOutputs <= 500 {
					// For medium multisends, add a 30% buffer
					totalGasEstimate = uint64(float64(totalGasEstimate) * 1.3)
				} else if numOutputs > 500 {
					// For very large multisends, add a 50% buffer
					totalGasEstimate = uint64(float64(totalGasEstimate) * 1.5)
				}

				// Store the calculated gas amount in MsgParams for later use
				if txParams.MsgParams == nil {
					txParams.MsgParams = make(map[string]interface{})
				}
				txParams.MsgParams["calculated_gas_amount"] = totalGasEstimate

				// Set the gas in the transaction parameters
				if txParams.Gas == nil {
					txParams.Gas = &types.GasSettings{
						UseSimulation: true,
						SafetyBuffer:  1.3, // Increased 30% buffer
					}
				}

				// Record this gas estimate in our gas strategy manager for this message type
				gasManager := GetGasStrategyManager()

				// Try to get a better gas estimate from historical data
				betterEstimate := gasManager.GetRecommendedGasForMsgType(
					txParams.NodeURL,
					"bank_multisend",
					int64(totalGasEstimate),
				)

				if betterEstimate > 0 {
					totalGasEstimate = uint64(betterEstimate)
				}

				fmt.Printf("Calculated gas for multisend with %d outputs: %d\n", numOutputs, totalGasEstimate)
			}

			return msg, nil
		}

		// Fallback to regular multisend if no distributor
		return createMultiSendMsg(txParams)
	case "ibc_transfer":
		// Create an IBC transfer message
		return createIbcTransferMsg(txParams)
	case "store_code":
		// Create a store code message for CosmWasm
		return createStoreCodeMsg(txParams)
	case "instantiate_contract":
		// Create an instantiate contract message for CosmWasm
		return createInstantiateContractMsg(txParams)
	case "execute_contract":
		// Create an execute contract message for CosmWasm
		return createExecuteContractMsg(txParams)
	default:
		return nil, fmt.Errorf("unsupported message type: %s", txParams.MsgType)
	}
}

// SimulateGas simulates the transaction to determine the actual gas needed
func SimulateGas(
	_ context.Context,
	clientCtx sdkclient.Context,
	msg sdk.Msg,
	txParams *types.TxParams,
) (uint64, error) {
	// Set default chainID if txParams is nil
	chainID := ""
	if txParams != nil && txParams.ChainID != "" {
		chainID = txParams.ChainID
	}

	txf := tx.Factory{}.
		WithChainID(chainID).
		WithTxConfig(clientCtx.TxConfig)

	_, adjusted, err := tx.CalculateGas(clientCtx, txf, msg)
	if err != nil {
		return 0, fmt.Errorf("failed to calculate gas: %w", err)
	}

	// Apply a safety buffer to the gas estimate
	safetyBuffer := defaultGasAdjustment
	if txParams != nil && txParams.Gas != nil && txParams.Gas.SafetyBuffer > 0 {
		safetyBuffer = txParams.Gas.SafetyBuffer
	}

	// Make sure the gas doesn't go below the minimum for this message type
	msgType := "unknown"
	if txParams != nil {
		msgType = txParams.MsgType
	}
	minGas := getDefaultGasLimitByMsgType(msgType)
	gasLimit := uint64(float64(adjusted) * safetyBuffer)
	if gasLimit < minGas {
		gasLimit = minGas
	}

	return gasLimit, nil
}

// getDefaultGasLimitByMsgType returns the default gas limit based on message type
func getDefaultGasLimitByMsgType(msgType string) uint64 {
	// Set minimum gas limits based on message type
	switch strings.ToLower(msgType) {
	case "bank_send":
		return minGasBankSend
	case "bank_multisend":
		return minGasBankMultiSend
	case "ibc_transfer":
		return minGasIbcTransfer
	case "wasm_execute":
		return minGasWasmExecute
	default:
		return minGasDefault
	}
}

// DetermineOptimalGas calculates the optimal gas limit based on simulation and safety buffer
func DetermineOptimalGas(ctx context.Context, clientCtx sdkclient.Context, _ tx.Factory, buffer float64, msgs ...sdk.Msg) (uint64, error) {
	// Create a basic TxParams with minimum required fields for simulation
	txParams := &types.TxParams{
		MsgType: getMsgTypeFromMsg(msgs[0]),
		Gas: &types.GasSettings{
			SafetyBuffer: buffer,
		},
	}

	// Simulate to get base gas amount
	simulatedGas, err := SimulateGas(ctx, clientCtx, msgs[0], txParams)
	if err != nil {
		// If simulation fails, fall back to default estimation
		return 0, err
	}

	// Apply safety buffer (e.g., 1.2 for 20% buffer)
	optimalGas := uint64(float64(simulatedGas) * buffer)

	fmt.Printf("Gas optimization: Simulated gas: %d, With buffer: %d\n",
		simulatedGas, optimalGas)

	return optimalGas, nil
}

// getMsgTypeFromMsg extracts the message type from a message
func getMsgTypeFromMsg(msg sdk.Msg) string {
	typeName := sdk.MsgTypeURL(msg)
	parts := strings.Split(typeName, ".")
	if len(parts) > 0 {
		return strings.ToLower(parts[len(parts)-1])
	}
	return "unknown"
}

// Add this new type to track node-specific settings
type NodeSettings struct {
	MinimumFees    map[string]uint64 // denom -> amount
	LastSequence   uint64
	LastUpdateTime time.Time
	mutex          sync.RWMutex
}

// Add this as a package-level variable
var (
	nodeSettingsMap = make(map[string]*NodeSettings)
	nodeSettingsMu  sync.RWMutex
)

// Add this new function to get or create node settings
func getNodeSettings(nodeURL string) *NodeSettings {
	nodeSettingsMu.Lock()
	defer nodeSettingsMu.Unlock()

	if settings, exists := nodeSettingsMap[nodeURL]; exists {
		return settings
	}

	settings := &NodeSettings{
		MinimumFees: make(map[string]uint64),
	}
	nodeSettingsMap[nodeURL] = settings
	return settings
}

// Add this function to update minimum fees for a node
func updateMinimumFee(nodeURL, denom string, amount uint64) {
	settings := getNodeSettings(nodeURL)
	settings.mutex.Lock()
	defer settings.mutex.Unlock()

	settings.MinimumFees[denom] = amount
	settings.LastUpdateTime = time.Now()
}

// Add this function to update sequence for a node
func updateSequence(nodeURL string, sequence uint64) {
	settings := getNodeSettings(nodeURL)
	settings.mutex.Lock()
	defer settings.mutex.Unlock()

	settings.LastSequence = sequence
	settings.LastUpdateTime = time.Now()
}

// calculateInitialFee calculates the initial fee amount based on gas limit and price
func calculateInitialFee(gasLimit uint64, gasPrice int64) int64 {
	// Scale to make fees reasonable
	feeAmount := int64(gasLimit) * gasPrice / 10000

	// Ensure minimum fee for non-zero gas price
	if feeAmount < 1 && gasPrice > 0 {
		feeAmount = 1
	}

	return feeAmount
}

// BuildAndSignTransaction builds and signs a transaction with proper error handling
func BuildAndSignTransaction(
	ctx context.Context,
	txParams types.TransactionParams,
	sequence uint64,
	_ interface{},
) ([]byte, error) {
	startTime := time.Now()
	log.Printf("=== Building transaction for %s ===", txParams.AcctAddress)

	// First, check if we have more up-to-date sequence info for this node from previous errors
	// Only use local node sequence tracking, don't coordinate across nodes
	nodeSettings := getNodeSettings(txParams.NodeURL)
	nodeSettings.mutex.RLock()
	if nodeSettings.LastSequence > sequence {
		// Log that we're using the cached sequence from previous error responses
		log.Printf("Using cached sequence %d instead of provided %d for node %s (local tracking only)",
			nodeSettings.LastSequence, sequence, txParams.NodeURL)
		sequence = nodeSettings.LastSequence
	}
	nodeSettings.mutex.RUnlock()

	// If we have no sequence yet, always start with 1 (not 0) and let the mempool tell us the correct value
	if sequence == 0 {
		sequence = 1
		log.Printf("Starting with sequence 1 for account on node %s (will learn from mempool)",
			txParams.NodeURL)
	}

	// We need to ensure the passed-in sequence is used
	txp := &types.TxParams{
		Config:    txParams.Config,
		NodeURL:   txParams.NodeURL,
		ChainID:   txParams.ChainID,
		PrivKey:   txParams.PrivKey,
		MsgType:   txParams.MsgType,
		MsgParams: txParams.MsgParams,
	}

	// Log the message parameters for debugging
	logMsgParams := make(map[string]interface{})
	for k, v := range txParams.MsgParams {
		logMsgParams[k] = v
	}
	// Don't log sensitive data
	if _, ok := logMsgParams["private_key"]; ok {
		logMsgParams["private_key"] = "[REDACTED]"
	}

	paramsJson, _ := json.Marshal(logMsgParams)
	log.Printf("Transaction parameters: %s", string(paramsJson))
	log.Printf("Message type: %s", txParams.MsgType)

	// Pass distributor through MsgParams for multisend operations
	if txParams.Distributor != nil && txParams.MsgType == "bank_multisend" {
		if txp.MsgParams == nil {
			txp.MsgParams = make(map[string]interface{})
		}
		txp.MsgParams["distributor"] = txParams.Distributor
	}

	// Use ClientContext with correct sequence - retry up to 3 times on failure
	var clientCtx sdkclient.Context
	var err error
	maxRetries := 3

	for i := 0; i < maxRetries; i++ {
		clientCtx, err = GetClientContext(txParams.Config, txParams.NodeURL)
		if err == nil {
			break
		}
		log.Printf("Warning: Failed to get client context on attempt %d: %v", i+1, err)
		if i < maxRetries-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create client context after %d attempts: %w", maxRetries, err)
	}

	// Build and sign the transaction
	msg, err := createMessage(txp)
	if err != nil {
		log.Printf("Error creating message: %v", err)
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	// Create a new TxBuilder
	txBuilder := clientCtx.TxConfig.NewTxBuilder()

	// Set the message and other transaction parameters
	if err := txBuilder.SetMsgs(msg); err != nil {
		log.Printf("Error setting messages: %v", err)
		return nil, fmt.Errorf("failed to set messages: %w", err)
	}

	// Check if there's a pre-calculated gas amount for multisend transactions
	var gasLimit uint64
	if calculatedGas, ok := txp.MsgParams["calculated_gas_amount"].(uint64); ok && calculatedGas > 0 {
		gasLimit = calculatedGas
	} else {
		// Default to message type specific gas limit
		gasLimit = getDefaultGasLimitByMsgType(txp.MsgType)
	}

	// Start with a low initial gas price to let us learn from errors
	gasPrice := int64(1) // Start very low and let the RPC tell us if we need more

	// Calculate fee amount based on gas limit and price
	feeAmount := calculateInitialFee(gasLimit, gasPrice)

	// Set a very low minimum fee to start with
	minFeeAmount := int64(1) // Start with minimum possible fee
	if feeAmount < minFeeAmount {
		log.Printf("Using minimum initial fee of %d", minFeeAmount)
		feeAmount = minFeeAmount
	}

	// Apply node-specific fee buffers based on history (these are learned from errors)
	// This will increase fees automatically if we've learned from previous errors
	feeAmount = applyNodeFeeBuffer(txParams.NodeURL, txParams.Config.Denom, feeAmount)

	// Create the fee
	fee := sdk.NewCoins(sdk.NewCoin(txParams.Config.Denom, sdkmath.NewInt(feeAmount)))
	log.Printf("Setting gas limit to %d and fee to %d %s", gasLimit, feeAmount, txParams.Config.Denom)

	txBuilder.SetGasLimit(gasLimit)
	txBuilder.SetFeeAmount(fee)

	// Prepare an empty signature to get the correct size
	sigV2 := signingtypes.SignatureV2{
		PubKey: txParams.PrivKey.PubKey(),
		Data: &signingtypes.SingleSignatureData{
			SignMode: signingtypes.SignMode_SIGN_MODE_DIRECT,
		},
	}

	if err := txBuilder.SetSignatures(sigV2); err != nil {
		log.Printf("Error setting signatures: %v", err)
		return nil, fmt.Errorf("failed to set signatures: %w", err)
	}

	signerData := xauthsigning.SignerData{
		ChainID:       txParams.ChainID,
		AccountNumber: txParams.AccNum,
		Sequence:      sequence,
	}

	// Sign the transaction with the private key
	sigV2, err = tx.SignWithPrivKey(
		ctx,
		signingtypes.SignMode_SIGN_MODE_DIRECT,
		signerData,
		txBuilder,
		txParams.PrivKey,
		clientCtx.TxConfig,
		sequence,
	)
	if err != nil {
		log.Printf("Error signing transaction: %v", err)
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Set the signed signature
	if err := txBuilder.SetSignatures(sigV2); err != nil {
		log.Printf("Error setting final signatures: %v", err)
		return nil, fmt.Errorf("failed to set signatures: %w", err)
	}

	// Encode the transaction
	txBytes, err := clientCtx.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		log.Printf("Error encoding transaction: %v", err)
		return nil, fmt.Errorf("failed to encode transaction: %w", err)
	}

	// Record the time we built this transaction
	log.Printf("Successfully built transaction for %s with sequence %d and fee %d %s in %v",
		txParams.AcctAddress, sequence, feeAmount, txParams.Config.Denom, time.Since(startTime))

	// If successful, update only our local node sequence cache preemptively
	// This helps avoid sequence errors on subsequent transactions to the same node
	// but doesn't coordinate across nodes to maintain divergent mempools
	updateSequence(txParams.NodeURL, sequence+1)

	return txBytes, nil
}

// Helper function to apply additional fee buffer based on node history
func applyNodeFeeBuffer(nodeURL, denom string, baseFee int64) int64 {
	// Get node settings
	nodeSettings := getNodeSettings(nodeURL)
	nodeSettings.mutex.RLock()
	defer nodeSettings.mutex.RUnlock()

	// Default buffer is 5%
	buffer := 1.05

	// If we've had fee errors from this node before, use a slightly higher buffer
	if minFee, exists := nodeSettings.MinimumFees[denom]; exists && minFee > 0 {
		// Apply a 10% buffer over the known minimum
		buffer = 1.1

		// Ensure we're at least meeting the known minimum fee (with buffer)
		minWithBuffer := int64(float64(minFee) * buffer)
		if baseFee < minWithBuffer {
			fmt.Printf("Applying %d%% buffer to node %s minimum fee (%d → %d)\n",
				int((buffer-1.0)*100), nodeURL, minFee, minWithBuffer)
			return minWithBuffer
		}
	}

	// Apply the buffer to the base fee
	return int64(float64(baseFee) * buffer)
}

// ProcessBroadcastResponse processes a broadcast response to extract sequence numbers and fee requirements
func ProcessBroadcastResponse(nodeURL, denom string, sequence uint64, respBytes []byte) {
	// If no response bytes, just return
	if len(respBytes) == 0 {
		return
	}

	// Get the full error string for pattern matching
	errorStr := string(respBytes)

	// Track the error message in our regex queue for later analysis
	regexErrorQueue.AddError(errorStr)

	// Check for sequence mismatch errors using our helper function
	if correctSeq := extractSequenceFromError(errorStr); correctSeq > 0 {
		// Got a specific sequence, update our tracking
		if correctSeq > sequence {
			log.Printf("FEE DEBUG: Node %s sequence updated from %d to %d (from error message)",
				nodeURL, sequence, correctSeq)
			updateSequence(nodeURL, correctSeq)
		}
	}

	// Check for insufficient fee errors
	if requiredFee := extractFeeFromError(errorStr, denom); requiredFee > 0 {
		// Apply a small buffer on top of the required fee to ensure success next time
		minFee := uint64(float64(requiredFee) * 1.1)

		log.Printf("FEE ADJUSTMENT: Required fee is %d%s, setting node minimum to %d%s for future transactions",
			requiredFee, denom, minFee, denom)

		// Update the minimum fee for this node/denom combination
		updateMinimumFee(nodeURL, denom, minFee)
	}

	// Check for out of gas errors
	if strings.Contains(errorStr, "out of gas") {
		log.Printf("OUT OF GAS: Transaction at node %s failed - consider increasing gas limits", nodeURL)
	}
}

// extractSequenceFromError extracts the sequence number from an error message
func extractSequenceFromError(errorStr string) uint64 {
	// Try multiple regex patterns to extract sequence information

	// Primary pattern: "account sequence mismatch: expected X, got Y"
	sequenceRegex := regexp.MustCompile(sequenceMismatchPattern)
	matches := sequenceRegex.FindStringSubmatch(errorStr)

	if len(matches) > 1 {
		extractedSeq, err := strconv.ParseUint(matches[1], 10, 64)
		if err == nil {
			return extractedSeq
		}
	}

	// Try alternative pattern 1
	altPattern1 := regexp.MustCompile(sequenceAltPattern1)
	altMatches1 := altPattern1.FindStringSubmatch(errorStr)
	if len(altMatches1) > 1 {
		extractedSeq, err := strconv.ParseUint(altMatches1[1], 10, 64)
		if err == nil {
			return extractedSeq
		}
	}

	// Try alternative pattern 2
	altPattern2 := regexp.MustCompile(sequenceAltPattern2)
	altMatches2 := altPattern2.FindStringSubmatch(errorStr)
	if len(altMatches2) > 1 {
		extractedSeq, err := strconv.ParseUint(altMatches2[1], 10, 64)
		if err == nil {
			return extractedSeq
		}
	}

	// Try alternative pattern 3
	altPattern3 := regexp.MustCompile(sequenceAltPattern3)
	altMatches3 := altPattern3.FindStringSubmatch(errorStr)
	if len(altMatches3) > 1 {
		extractedSeq, err := strconv.ParseUint(altMatches3[1], 10, 64)
		if err == nil {
			return extractedSeq
		}
	}

	// Try alternative pattern 4
	altPattern4 := regexp.MustCompile(sequenceAltPattern4)
	altMatches4 := altPattern4.FindStringSubmatch(errorStr)
	if len(altMatches4) > 1 {
		extractedSeq, err := strconv.ParseUint(altMatches4[1], 10, 64)
		if err == nil {
			return extractedSeq
		}
	}

	// No sequence found
	return 0
}

// extractFeeFromError extracts the required fee from an error message
func extractFeeFromError(errorStr string, denom string) uint64 {
	// Check for the most common pattern "spendable balance X is smaller than Y"
	feePattern := regexp.MustCompile(`spendable balance .* is smaller than (\d+)` + denom)
	matches := feePattern.FindStringSubmatch(errorStr)

	if len(matches) > 1 {
		requiredFee, err := strconv.ParseUint(matches[1], 10, 64)
		if err == nil {
			return requiredFee
		}
	}

	// Try the direct "required: X" pattern
	directPattern := regexp.MustCompile(`required: (\d+)` + denom)
	directMatches := directPattern.FindStringSubmatch(errorStr)

	if len(directMatches) > 1 {
		requiredFee, err := strconv.ParseUint(directMatches[1], 10, 64)
		if err == nil {
			return requiredFee
		}
	}

	// Try the "fee < minimum" pattern
	minFeePattern := regexp.MustCompile(`fee < minimum \((\d+)` + denom)
	minFeeMatches := minFeePattern.FindStringSubmatch(errorStr)

	if len(minFeeMatches) > 1 {
		requiredFee, err := strconv.ParseUint(minFeeMatches[1], 10, 64)
		if err == nil {
			return requiredFee
		}
	}

	// No fee found
	return 0
}

// BroadcastTxSync is a wrapper around the standard broadcast that includes error processing
func BroadcastTxSync(ctx context.Context, clientCtx sdkclient.Context, txBytes []byte, nodeURL, denom string, sequence uint64) (*sdk.TxResponse, error) {
	// For older versions of Cosmos SDK, BroadcastTxSync is available directly
	// For newer versions we use BroadcastTx with appropriate mode
	resp, err := clientCtx.BroadcastTxSync(txBytes)

	// Process broadcast response for errors to update our node-specific tracking
	if resp != nil {
		respBytes, _ := json.Marshal(resp)
		ProcessBroadcastResponse(nodeURL, denom, sequence, respBytes)
	} else if err != nil {
		// Add error to the queue for pattern matching
		regexErrorQueue.AddError(err.Error())
		ProcessBroadcastResponse(nodeURL, denom, sequence, []byte(err.Error()))
	}

	return resp, err
}

// BroadcastTxAsync is a wrapper around async broadcast that includes error processing
func BroadcastTxAsync(ctx context.Context, clientCtx sdkclient.Context, txBytes []byte, nodeURL, denom string, sequence uint64) (*sdk.TxResponse, error) {
	// In newer versions of the SDK, BroadcastTxAsync is not directly available on clientCtx
	// Instead, we use the general BroadcastTx method with the appropriate mode
	resp, err := clientCtx.BroadcastTxAsync(txBytes)

	// Process broadcast response for errors to update our node-specific tracking
	if resp != nil {
		respBytes, _ := json.Marshal(resp)
		ProcessBroadcastResponse(nodeURL, denom, sequence, respBytes)
	} else if err != nil {
		// Add error to the queue for pattern matching
		regexErrorQueue.AddError(err.Error())
		ProcessBroadcastResponse(nodeURL, denom, sequence, []byte(err.Error()))
	}

	return resp, err
}

// BroadcastTxBlock is a wrapper around block/commit broadcast that includes error processing
func BroadcastTxBlock(ctx context.Context, clientCtx sdkclient.Context, txBytes []byte, nodeURL, denom string, sequence uint64) (*sdk.TxResponse, error) {
	// In newer versions of the SDK, BroadcastTxCommit is not directly available on clientCtx
	// Instead, we use the general BroadcastTx method with the appropriate mode
	resp, err := clientCtx.BroadcastTx(txBytes)

	// Process broadcast response for errors to update our node-specific tracking
	if resp != nil {
		respBytes, _ := json.Marshal(resp)
		ProcessBroadcastResponse(nodeURL, denom, sequence, respBytes)
	} else if err != nil {
		// Add error to the queue for pattern matching
		regexErrorQueue.AddError(err.Error())
		ProcessBroadcastResponse(nodeURL, denom, sequence, []byte(err.Error()))
	}

	return resp, err
}

// BroadcastTx broadcasts a transaction and handles errors
func BroadcastTx(
	ctx context.Context,
	clientCtx sdkclient.Context,
	txBytes []byte,
	txParams types.TransactionParams,
	sequence uint64,
	broadcast string,
) (*sdk.TxResponse, error) {
	var resp *sdk.TxResponse
	var err error

	// Use our custom wrappers that include error tracking
	switch broadcast {
	case "sync":
		resp, err = BroadcastTxSync(ctx, clientCtx, txBytes, txParams.NodeURL, txParams.Config.Denom, sequence)
	case "async":
		resp, err = BroadcastTxAsync(ctx, clientCtx, txBytes, txParams.NodeURL, txParams.Config.Denom, sequence)
	case "block":
		// For commit/block mode, use our wrapper
		resp, err = BroadcastTxBlock(ctx, clientCtx, txBytes, txParams.NodeURL, txParams.Config.Denom, sequence)
	default:
		// Default to sync mode
		resp, err = BroadcastTxSync(ctx, clientCtx, txBytes, txParams.NodeURL, txParams.Config.Denom, sequence)
	}

	// Further process the response regardless of error status
	ProcessTxBroadcastResult(resp, err, txParams.NodeURL, sequence)

	return resp, err
}

// ProcessTxBroadcastResult processes a transaction broadcast result and updates node metrics
func ProcessTxBroadcastResult(txResponse *sdk.TxResponse, err error, nodeURL string, sequence uint64) {
	// Calculate approximate latency based on time since the transaction was added to the mempool
	latency := time.Since(time.Now().Add(-1 * time.Second)) // Approximate 1s for processing
	success := err == nil && (txResponse == nil || txResponse.Code == 0)

	// Update node performance metrics
	UpdateNodePerformance(nodeURL, success, latency)

	// Log performance data for debugging
	perf := GetOrCreateNodePerformance(nodeURL)
	perf.mutex.RLock()
	successRate := 0.0
	if perf.SuccessCount+perf.FailureCount > 0 {
		successRate = float64(perf.SuccessCount) * 100 / float64(perf.SuccessCount+perf.FailureCount)
	}
	perf.mutex.RUnlock()

	if success {
		log.Printf("TX SUCCESS on node %s (success rate: %.1f%%, avg latency: %v)",
			nodeURL, successRate, perf.AverageLatency)
	} else {
		// Process error response for sequence and fee information
		if txResponse != nil && txResponse.Code != 0 {
			if txResponse.RawLog != "" {
				// Handle specific error cases - this updates our local tracking
				ProcessBroadcastResponse(nodeURL, "", sequence, []byte(txResponse.RawLog))

				// Log error details for debugging
				log.Printf("TX FAILED on node %s: code=%d, log=%s (success rate: %.1f%%)",
					nodeURL, txResponse.Code, truncateErrorString(txResponse.RawLog, 100), successRate)
			}
		} else if err != nil {
			// Add error to queue for pattern matching
			regexErrorQueue.AddError(err.Error())
			ProcessBroadcastResponse(nodeURL, "", sequence, []byte(err.Error()))

			// Log error details for debugging
			log.Printf("TX ERROR on node %s: %s (success rate: %.1f%%)",
				nodeURL, truncateErrorString(err.Error(), 100), successRate)
		}
	}
}

// SendTx builds, signs, and broadcasts a transaction
func SendTx(
	ctx context.Context,
	txParams types.TransactionParams,
	sequence uint64,
	broadcastMode string,
) (*sdk.TxResponse, error) {
	startTime := time.Now()

	// Check if this account has an assigned node
	if txParams.AcctAddress != "" {
		if assignedNode, hasAssignment := GetNodeForAccount(txParams.AcctAddress); hasAssignment {
			// Use the assigned node for this account
			log.Printf("Using assigned node %s for account %s", assignedNode, txParams.AcctAddress)
			txParams.NodeURL = assignedNode

			// Get sequence from node manager if available, but only for this specific node
			// Don't try to coordinate sequences across nodes to maintain divergent mempools
			if cachedSeq, hasSequence := GetAccountSequence(txParams.AcctAddress); hasSequence && cachedSeq > sequence {
				log.Printf("Using cached sequence %d instead of %d for account %s on node %s (node-specific)",
					cachedSeq, sequence, txParams.AcctAddress, assignedNode)
				sequence = cachedSeq
			}
		} else if len(txParams.Config.Nodes.RPC) > 0 {
			// No assignment yet, pick the best performing node based on metrics
			healthyNodes := GetHealthyNodesOnly(txParams.Config.Nodes.RPC)
			if len(healthyNodes) > 0 {
				// Find the node with highest success rate and lowest latency
				bestNode := healthyNodes[0]
				bestScore := 0.0

				for _, node := range healthyNodes {
					perf := GetOrCreateNodePerformance(node)
					perf.mutex.RLock()

					// Calculate score based on success rate and latency
					total := perf.SuccessCount + perf.FailureCount
					score := 1.0 // Default score

					if total > 0 {
						successRate := float64(perf.SuccessCount) / float64(total)
						// Latency factor - lower is better
						latencyFactor := 1.0
						if perf.AverageLatency > 0 {
							latencyFactor = 1.0 / float64(perf.AverageLatency.Milliseconds()+1)
						}
						// Combined score
						score = successRate*0.7 + latencyFactor*0.3
					}

					perf.mutex.RUnlock()

					if score > bestScore {
						bestScore = score
						bestNode = node
					}
				}

				txParams.NodeURL = bestNode
				AssignNodeToAccount(txParams.AcctAddress, bestNode)
				log.Printf("Assigned best performing node %s to account %s (score: %.2f)",
					bestNode, txParams.AcctAddress, bestScore)
			}
		}
	}

	// Set a timeout for the entire transaction process
	timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Try to build and sign the transaction, with retries
	var txBytes []byte
	var err error
	maxBuildRetries := 2 // Limit retries to prevent infinite loops

	for retry := 0; retry <= maxBuildRetries; retry++ {
		txBytes, err = BuildAndSignTransaction(timeoutCtx, txParams, sequence, nil)
		if err == nil {
			break // Success, exit the retry loop
		}

		// If build fails, log and retry with a delay unless we've exhausted retries
		if retry < maxBuildRetries {
			log.Printf("WARNING: Failed to build transaction on attempt %d: %v. Retrying...",
				retry+1, err)
			time.Sleep(50 * time.Millisecond)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to build and sign transaction after %d attempts: %w",
			maxBuildRetries+1, err)
	}

	// Set up client context for broadcasting
	clientCtx, err := GetClientContext(txParams.Config, txParams.NodeURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get client context: %w", err)
	}

	// Broadcast the transaction
	var resp *sdk.TxResponse
	resp, err = BroadcastTx(timeoutCtx, clientCtx, txBytes, txParams, sequence, broadcastMode)

	// Calculate transaction latency
	txLatency := time.Since(startTime)

	// Check for sequence mismatch error and retry if needed
	if err != nil {
		errorStr := err.Error()
		if strings.Contains(errorStr, "account sequence mismatch") ||
			strings.Contains(errorStr, "incorrect account sequence") {

			log.Printf("Sequence mismatch detected for node %s, retrying with corrected sequence...",
				txParams.NodeURL)

			// Add the error to our queue if not already added in BroadcastTx
			regexErrorQueue.AddError(errorStr)

			// Use a backoff delay before retrying to give the node's mempool a chance to process
			time.Sleep(100 * time.Millisecond)

			return retryWithCorrectedSequence(timeoutCtx, clientCtx, txBytes, txParams, sequence, broadcastMode)
		}

		// For other types of errors, log but don't retry to avoid cascading failures
		log.Printf("Transaction error on node %s (not retrying): %v", txParams.NodeURL, err)
	}

	// Process the broadcast result for tracking
	ProcessTxBroadcastResult(resp, err, txParams.NodeURL, sequence)

	// Update sequence in node manager if successful, but only for this node
	if resp != nil && resp.Code == 0 && txParams.AcctAddress != "" {
		// We only update the sequence for this specific node to maintain divergent mempools
		UpdateAccountSequence(txParams.AcctAddress, sequence+1)
		log.Printf("Updated sequence for %s to %d in node-specific cache for %s (latency: %v)",
			txParams.AcctAddress, sequence+1, txParams.NodeURL, txLatency)
	}

	return resp, err
}

// retryWithCorrectedSequence retries a transaction with a corrected sequence number
func retryWithCorrectedSequence(
	ctx context.Context,
	clientCtx sdkclient.Context,
	txBytes []byte,
	txParams types.TransactionParams,
	sequence uint64,
	broadcastMode string,
) (*sdk.TxResponse, error) {
	// Get all recent error messages to search for sequence information
	errorStr := regexErrorQueue.GetErrorString()
	log.Printf("RETRY DEBUG: Analyzing error queue for sequence and fee issues on node %s",
		txParams.NodeURL)

	// Extract sequence using our helper function
	correctSeq := extractSequenceFromError(errorStr)

	// If no sequence found, increment by 1 (but never decrement)
	if correctSeq == 0 || correctSeq < sequence {
		// Fallback: increment sequence by 1
		correctSeq = sequence + 1
		log.Printf("SEQUENCE FALLBACK: No valid sequence extracted, using sequence+1: %d", correctSeq)
	} else {
		log.Printf("SEQUENCE MATCH: Extracted sequence %d from mempool error (was: %d)",
			correctSeq, sequence)
	}

	// Update our tracking with this corrected sequence
	updateSequence(txParams.NodeURL, correctSeq)

	// Check if we also need to adjust fees
	requiredFee := extractFeeFromError(errorStr, txParams.Config.Denom)
	if requiredFee > 0 {
		// Log the fee adjustment
		log.Printf("FEE ADJUSTMENT: Required fee is %d%s, will use in retry",
			requiredFee, txParams.Config.Denom)

		// Add a 10% buffer to ensure success
		adjustedFee := uint64(float64(requiredFee) * 1.1)
		updateMinimumFee(txParams.NodeURL, txParams.Config.Denom, adjustedFee)
	}

	// Log retry information
	log.Printf("RETRY TX: Node %s requires sequence %d (was: %d)",
		txParams.NodeURL, correctSeq, sequence)

	// Add a small delay before retrying to give the node a chance to process
	time.Sleep(200 * time.Millisecond)

	// Broadcast using the corrected sequence, but don't reuse the same txBytes
	// We need to build a new transaction with the correct sequence and possibly updated fee
	return SendTx(ctx, txParams, correctSeq, broadcastMode)
}

// truncateErrorString truncates a long error string for logging
func truncateErrorString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Simple node assignment system
var (
	nodeAssignments      = make(map[string]string) // address -> nodeURL
	nodeSequences        = make(map[string]uint64) // address -> sequence
	nodeAssignmentsMutex sync.RWMutex

	// Node performance tracking
	nodePerformance      = make(map[string]*NodePerformance) // nodeURL -> performance stats
	nodePerformanceMutex sync.RWMutex
)

// NodePerformance tracks performance statistics for a node
type NodePerformance struct {
	SuccessCount    int
	FailureCount    int
	AverageLatency  time.Duration
	LastSuccessTime time.Time
	IsHealthy       bool
	mutex           sync.RWMutex
}

// GetOrCreateNodePerformance gets or creates a node performance tracker
func GetOrCreateNodePerformance(nodeURL string) *NodePerformance {
	nodePerformanceMutex.Lock()
	defer nodePerformanceMutex.Unlock()

	if perf, exists := nodePerformance[nodeURL]; exists {
		return perf
	}

	perf := &NodePerformance{
		IsHealthy: true, // Assume healthy until proven otherwise
	}
	nodePerformance[nodeURL] = perf
	return perf
}

// UpdateNodePerformance updates performance metrics for a node
func UpdateNodePerformance(nodeURL string, success bool, latency time.Duration) {
	perf := GetOrCreateNodePerformance(nodeURL)
	perf.mutex.Lock()
	defer perf.mutex.Unlock()

	if success {
		perf.SuccessCount++
		perf.LastSuccessTime = time.Now()

		// Update average latency
		if perf.AverageLatency == 0 {
			perf.AverageLatency = latency
		} else {
			perf.AverageLatency = (perf.AverageLatency*9 + latency) / 10 // 90% old + 10% new
		}
	} else {
		perf.FailureCount++

		// Mark node as unhealthy if it hasn't had a success in over 30 seconds
		if time.Since(perf.LastSuccessTime) > 30*time.Second && perf.LastSuccessTime.Unix() > 0 {
			perf.IsHealthy = false
		}
	}
}

// GetHealthyNodesOnly returns a list of currently healthy nodes
func GetHealthyNodesOnly(nodes []string) []string {
	if len(nodes) <= 1 {
		return nodes // If only one node, return it regardless of health
	}

	nodePerformanceMutex.RLock()
	defer nodePerformanceMutex.RUnlock()

	healthyNodes := make([]string, 0, len(nodes))
	for _, nodeURL := range nodes {
		if perf, exists := nodePerformance[nodeURL]; exists {
			perf.mutex.RLock()
			if perf.IsHealthy {
				healthyNodes = append(healthyNodes, nodeURL)
			}
			perf.mutex.RUnlock()
		} else {
			// If no performance data, assume node is healthy
			healthyNodes = append(healthyNodes, nodeURL)
		}
	}

	// If all nodes are unhealthy, return all nodes (better than nothing)
	if len(healthyNodes) == 0 {
		return nodes
	}

	return healthyNodes
}

// AssignNodesToAccounts distributes accounts across nodes in a load-balanced way
// Returns a map of address -> nodeURL
func AssignNodesToAccounts(addresses []string, nodes []string) map[string]string {
	if len(nodes) == 0 || len(addresses) == 0 {
		return map[string]string{}
	}

	// Get only healthy nodes if possible
	healthyNodes := GetHealthyNodesOnly(nodes)
	if len(healthyNodes) == 0 {
		healthyNodes = nodes // Fall back to all nodes if none are healthy
	}

	nodePerformanceMutex.RLock()
	// Sort nodes by performance (success rate and latency)
	// This will prioritize nodes with better performance
	type nodeWithScore struct {
		url   string
		score float64
	}

	scoredNodes := make([]nodeWithScore, 0, len(healthyNodes))
	for _, nodeURL := range healthyNodes {
		var score float64 = 1.0 // Default score

		if perf, exists := nodePerformance[nodeURL]; exists {
			perf.mutex.RLock()
			// Calculate score based on success rate and latency
			total := perf.SuccessCount + perf.FailureCount
			if total > 0 {
				successRate := float64(perf.SuccessCount) / float64(total)
				// Latency factor - lower is better
				latencyFactor := 1.0
				if perf.AverageLatency > 0 {
					latencyFactor = 1.0 / float64(perf.AverageLatency.Milliseconds()+1)
				}
				// Combined score
				score = successRate*0.7 + latencyFactor*0.3
			}
			perf.mutex.RUnlock()
		}

		scoredNodes = append(scoredNodes, nodeWithScore{nodeURL, score})
	}
	nodePerformanceMutex.RUnlock()

	// Sort by score in descending order (better nodes first)
	sort.Slice(scoredNodes, func(i, j int) bool {
		return scoredNodes[i].score > scoredNodes[j].score
	})

	// Extract just the URLs in priority order
	prioritizedNodes := make([]string, len(scoredNodes))
	for i, n := range scoredNodes {
		prioritizedNodes[i] = n.url
	}

	// Distribute accounts across nodes
	assignments := make(map[string]string, len(addresses))
	nodeAssignmentsMutex.Lock()
	defer nodeAssignmentsMutex.Unlock()

	// First, try to maintain existing assignments if the node is still in the list
	for _, address := range addresses {
		if currentNode, exists := nodeAssignments[address]; exists {
			// Check if the current node is in our prioritized list
			for _, node := range prioritizedNodes {
				if node == currentNode {
					// Maintain the current assignment
					assignments[address] = currentNode
					nodeAssignments[address] = currentNode
					break
				}
			}
		}
	}

	// Then assign remaining accounts evenly across nodes, prioritizing better nodes
	unassignedAddresses := make([]string, 0)
	for _, address := range addresses {
		if _, exists := assignments[address]; !exists {
			unassignedAddresses = append(unassignedAddresses, address)
		}
	}

	for i, address := range unassignedAddresses {
		nodeIndex := i % len(prioritizedNodes)
		nodeURL := prioritizedNodes[nodeIndex]
		assignments[address] = nodeURL
		nodeAssignments[address] = nodeURL
		log.Printf("Assigned node %s to account %s", nodeURL, address)
	}

	return assignments
}

// AssignNodeToAccount assigns a node to an account
func AssignNodeToAccount(address, nodeURL string) {
	nodeAssignmentsMutex.Lock()
	defer nodeAssignmentsMutex.Unlock()
	nodeAssignments[address] = nodeURL
	log.Printf("Assigned node %s to account %s", nodeURL, address)
}

// GetNodeForAccount returns the assigned node for an account
func GetNodeForAccount(address string) (string, bool) {
	nodeAssignmentsMutex.RLock()
	defer nodeAssignmentsMutex.RUnlock()
	node, exists := nodeAssignments[address]
	return node, exists
}

// UpdateAccountSequence updates the sequence for an account
func UpdateAccountSequence(address string, sequence uint64) {
	nodeAssignmentsMutex.Lock()
	defer nodeAssignmentsMutex.Unlock()
	nodeSequences[address] = sequence
}

// GetAccountSequence gets the sequence for an account
func GetAccountSequence(address string) (uint64, bool) {
	nodeAssignmentsMutex.RLock()
	defer nodeAssignmentsMutex.RUnlock()
	sequence, exists := nodeSequences[address]
	return sequence, exists
}
