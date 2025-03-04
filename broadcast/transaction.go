package broadcast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	signing "github.com/cosmos/cosmos-sdk/types/tx/signing"
	xauthsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
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

	// Error patterns
	insufficientFeesPattern = "insufficient fees; got: \\d+\\w+ required: (\\d+)\\w+"
	sequenceMismatchPattern = "account sequence mismatch: expected (\\d+), got \\d+"
)

// multiSendDistributor defines the interface for creating MultiSend messages
type multiSendDistributor interface {
	CreateDistributedMultiSendMsg(fromAddress string, msgParams types.MsgParams, seed int64) (sdk.Msg, string, error)
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

	toAddrStr, ok := txParams.MsgParams["to_address"].(string)
	if !ok || toAddrStr == "" {
		return nil, errors.New("to_address is required for bank_send")
	}

	// Parse addresses
	fromAddr, err := sdk.AccAddressFromBech32(fromAddrStr)
	if err != nil {
		return nil, fmt.Errorf("invalid from address: %w", err)
	}

	toAddr, err := sdk.AccAddressFromBech32(toAddrStr)
	if err != nil {
		return nil, fmt.Errorf("invalid to address: %w", err)
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
	return &banktypes.MsgSend{
		FromAddress: fromAddr.String(),
		ToAddress:   toAddr.String(),
		Amount:      coins,
	}, nil
}

// createMultiSendMsg creates a bank multisend message from the provided parameters
func createMultiSendMsg(txParams *types.TxParams) (sdk.Msg, error) {
	// Implementation not needed as we're using the distributor
	return nil, errors.New("basic multisend not implemented - use distributor instead")
}

// createIbcTransferMsg creates an IBC transfer message
func createIbcTransferMsg(txParams *types.TxParams) (sdk.Msg, error) {
	// Just a stub for now
	return nil, errors.New("ibc_transfer not implemented")
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
	clientCtx, err := P2PGetClientContext(txParams.Config, txParams.NodeURL)
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
		return nil, fmt.Errorf("failed to get account info: %w", err)
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
		signing.SignMode_SIGN_MODE_DIRECT,
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
	// First, check if we have more up-to-date sequence info for this node from previous errors
	nodeSettings := getNodeSettings(txParams.NodeURL)
	nodeSettings.mutex.RLock()
	if nodeSettings.LastSequence > sequence {
		// Log that we're using the cached sequence from previous error responses
		fmt.Printf("Using cached sequence %d instead of provided %d for node %s (from previous tx errors)\n",
			nodeSettings.LastSequence, sequence, txParams.NodeURL)
		sequence = nodeSettings.LastSequence
	}
	nodeSettings.mutex.RUnlock()

	// We need to ensure the passed-in sequence is used
	txp := &types.TxParams{
		Config:    txParams.Config,
		NodeURL:   txParams.NodeURL,
		ChainID:   txParams.ChainID,
		PrivKey:   txParams.PrivKey,
		MsgType:   txParams.MsgType,
		MsgParams: txParams.MsgParams,
	}

	// Pass distributor through MsgParams for multisend operations
	if txParams.Distributor != nil && txParams.MsgType == "bank_multisend" {
		if txp.MsgParams == nil {
			txp.MsgParams = make(map[string]interface{})
		}
		txp.MsgParams["distributor"] = txParams.Distributor
	}

	// Get client context
	clientCtx, err := P2PGetClientContext(txParams.Config, txParams.NodeURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get client context: %w", err)
	}

	// Build and sign the transaction
	msg, err := createMessage(txp)
	if err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	// Create a new TxBuilder
	txBuilder := clientCtx.TxConfig.NewTxBuilder()

	// Set the message and other transaction parameters
	if err := txBuilder.SetMsgs(msg); err != nil {
		return nil, fmt.Errorf("failed to set messages: %w", err)
	}

	// Check if there's a pre-calculated gas amount for multisend transactions
	var gasLimit uint64
	if calculatedGas, ok := txp.MsgParams["calculated_gas_amount"].(uint64); ok && calculatedGas > 0 {
		// Use the pre-calculated gas amount for multisend
		gasLimit = calculatedGas
		fmt.Printf("Using pre-calculated gas amount for multisend: %d\n", gasLimit)
	} else {
		// Estimate gas through simulation
		simulatedGas, err := DetermineOptimalGas(ctx, clientCtx, tx.Factory{}, 1.3, msg)
		if err != nil {
			// Use default if simulation fails
			gasLimit = uint64(txParams.Config.BaseGas)
			fmt.Printf("Gas simulation failed, using default gas: %d\n", gasLimit)
		} else {
			gasLimit = simulatedGas
		}
	}

	txBuilder.SetGasLimit(gasLimit)

	// Set fee - get the gas price from config
	gasPrice := txParams.Config.Gas.Low

	// For zero gas price, check if we should use adaptive gas pricing
	if gasPrice == 0 {
		gsm := GetGasStrategyManager()
		caps := gsm.GetNodeCapabilities(txParams.NodeURL)
		if !caps.AcceptsZeroGas {
			// Use low non-zero gas price if node doesn't support zero gas
			gasPrice = 1
			fmt.Printf("Node %s may not support zero gas, using gas price: %d\n",
				txParams.NodeURL, gasPrice)
		}
	}

	// Calculate initial fee amount
	feeAmount := calculateInitialFee(gasLimit, gasPrice)

	// Important: Check if we have a stored minimum fee for this node and use it if higher
	nodeSettings.mutex.RLock()
	if minFee, exists := nodeSettings.MinimumFees[txParams.Config.Denom]; exists {
		if uint64(feeAmount) < minFee {
			fmt.Printf("Using node-specific minimum fee %d instead of calculated %d for %s\n",
				minFee, feeAmount, txParams.NodeURL)
			feeAmount = int64(minFee)
		}
	}
	nodeSettings.mutex.RUnlock()

	// Apply a more aggressive minimum fee strategy to prevent "insufficient fees" errors
	// For nodes that have previously returned fee errors, add a buffer
	baseFeeThreshold := int64(200)

	// For large multisend transactions, ensure the fee is proportionally higher
	if txParams.MsgType == "bank_multisend" && gasLimit > 100000 {
		// Much higher minimum fee for large multisends - use gas-based calculation
		minFee := int64(gasLimit) / 5000 // More aggressive scaling (changed from 10000)
		if feeAmount < minFee {
			feeAmount = minFee
			fmt.Printf("Increasing multisend fee to %d based on gas usage\n", feeAmount)
		}
	} else if gasPrice > 0 && feeAmount < baseFeeThreshold {
		// For regular transactions, use higher minimum fee
		feeAmount = baseFeeThreshold
		fmt.Printf("Using minimum fee threshold of %d\n", feeAmount)
	}

	// Apply additional node-specific fee buffer based on historical errors
	// This helps prevent fee errors on chains that are sensitive to fee amounts
	feeAmount = applyNodeFeeBuffer(txParams.NodeURL, txParams.Config.Denom, feeAmount)

	feeCoin := fmt.Sprintf("%d%s", feeAmount, txParams.Config.Denom)
	fee, err := sdk.ParseCoinsNormalized(feeCoin)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fee: %w", err)
	}

	fmt.Printf("Setting transaction fee: %s (gas limit: %d, gas price: %d) for node %s\n",
		fee.String(), gasLimit, gasPrice, txParams.NodeURL)
	txBuilder.SetFeeAmount(fee)

	// Set memo if provided
	txBuilder.SetMemo("")

	// Get account number - this is still needed, but we won't use its sequence
	accNum := txParams.AccNum
	fromAddress, _ := txParams.MsgParams["from_address"].(string)

	// We only need account number from GetAccountInfo, NOT the sequence
	// The sequence we use should be from our tracking system, which considers mempool state
	fetchedAccNum, stateSequence, err := GetAccountInfo(ctx, clientCtx, fromAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get account info: %w", err)
	}

	// Use the fetched account number
	accNum = fetchedAccNum

	// Only log the state sequence for debugging, but don't use it directly
	// This helps us understand discrepancies between state and our tracked sequences
	fmt.Printf("Node %s reports state sequence %d, using tracked sequence %d\n",
		txParams.NodeURL, stateSequence, sequence)

	// If we have no better information (first tx to this node), then use state sequence
	if sequence == 0 {
		sequence = stateSequence
		fmt.Printf("First transaction to node %s, using state sequence: %d\n", txParams.NodeURL, sequence)
		updateSequence(txParams.NodeURL, sequence)
	}

	// Set up signature
	sigV2 := signing.SignatureV2{
		PubKey:   txParams.PubKey,
		Sequence: sequence,
		Data: &signing.SingleSignatureData{
			SignMode: signing.SignMode_SIGN_MODE_DIRECT,
		},
	}

	if err := txBuilder.SetSignatures(sigV2); err != nil {
		return nil, fmt.Errorf("failed to set signatures: %w", err)
	}

	signerData := xauthsigning.SignerData{
		ChainID:       txParams.ChainID,
		AccountNumber: accNum,
		Sequence:      sequence,
	}

	// Sign the transaction with the private key
	sigV2, err = tx.SignWithPrivKey(
		ctx,
		signing.SignMode_SIGN_MODE_DIRECT,
		signerData,
		txBuilder,
		txParams.PrivKey,
		clientCtx.TxConfig,
		sequence,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Set the signed signature
	if err := txBuilder.SetSignatures(sigV2); err != nil {
		return nil, fmt.Errorf("failed to set signatures: %w", err)
	}

	// Encode the transaction
	txBytes, err := clientCtx.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return nil, fmt.Errorf("failed to encode transaction: %w", err)
	}

	// If successful, update our node sequence cache preemptively
	// This helps avoid sequence errors on subsequent transactions to the same node
	updateSequence(txParams.NodeURL, sequence+1)

	return txBytes, nil
}

// Helper function to apply additional fee buffer based on node history
func applyNodeFeeBuffer(nodeURL, denom string, baseFee int64) int64 {
	// Get node settings
	nodeSettings := getNodeSettings(nodeURL)
	nodeSettings.mutex.RLock()
	defer nodeSettings.mutex.RUnlock()

	// Default buffer is 10%
	buffer := 1.1

	// If we've had fee errors from this node before, use a more aggressive buffer
	if minFee, exists := nodeSettings.MinimumFees[denom]; exists && minFee > 0 {
		// Apply a 20% buffer over the known minimum
		buffer = 1.2

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

// ProcessBroadcastResponse processes the response from a transaction broadcast
// and updates node-specific settings based on errors
func ProcessBroadcastResponse(nodeURL, denom string, sequence uint64, respBytes []byte) {
	// Check if there's an error response to parse
	if len(respBytes) == 0 {
		return
	}

	respStr := string(respBytes)

	// Check for common error patterns in the response

	// 1. Check for sequence mismatch errors
	sequenceRegex := regexp.MustCompile(sequenceMismatchPattern)
	if matches := sequenceRegex.FindStringSubmatch(respStr); len(matches) > 1 {
		correctSeq, _ := strconv.ParseUint(matches[1], 10, 64)

		// Update our sequence tracking with the correct mempool sequence
		updateSequence(nodeURL, correctSeq)
		fmt.Printf("MEMPOOL SYNC - Updated sequence for %s: %d (was: %d)\n",
			nodeURL, correctSeq, sequence)

		// Return early since this is a critical error to fix
		return
	}

	// 2. Check for insufficient fees errors
	insufficientFeesRegex := regexp.MustCompile(insufficientFeesPattern)
	if matches := insufficientFeesRegex.FindStringSubmatch(respStr); len(matches) > 1 {
		requiredFee, _ := strconv.ParseUint(matches[1], 10, 64)

		// Add a 20% buffer to the required fee to avoid borderline cases
		bufferedFee := uint64(float64(requiredFee) * 1.2)

		// Update minimum fee for this node
		updateMinimumFee(nodeURL, denom, bufferedFee)
		fmt.Printf("FEE ADJUSTMENT - Node %s requires minimum %d %s, storing %d with buffer\n",
			nodeURL, requiredFee, denom, bufferedFee)

		return
	}

	// If we got here and there's an "out of gas" error, update gas requirements
	if strings.Contains(respStr, "out of gas") {
		// This error handling would update gas strategy, but that's handled elsewhere
		fmt.Printf("GAS ERROR detected for node %s - Consider increasing gas limits\n", nodeURL)
		return
	}

	// If no errors found, this was likely a successful transaction
	// We can simply rely on the preemptive sequence update in BuildAndSignTransaction
}

// BroadcastTxSync is a wrapper around the standard broadcast that includes error processing
func BroadcastTxSync(ctx context.Context, clientCtx sdkclient.Context, txBytes []byte, nodeURL, denom string, sequence uint64) (*sdk.TxResponse, error) {
	resp, err := clientCtx.BroadcastTxSync(txBytes)

	// Process broadcast response for errors to update our node-specific tracking
	// We do this regardless of whether the broadcast itself returned an error
	if resp != nil {
		// Marshal response to bytes for processing
		respBytes, _ := json.Marshal(resp)
		ProcessBroadcastResponse(nodeURL, denom, sequence, respBytes)
	} else if err != nil {
		// If we have an error but no response, process the error string
		ProcessBroadcastResponse(nodeURL, denom, sequence, []byte(err.Error()))
	}

	return resp, err
}

// BroadcastTxAsync is a wrapper around the standard broadcast that includes error processing
func BroadcastTxAsync(ctx context.Context, clientCtx sdkclient.Context, txBytes []byte, nodeURL, denom string, sequence uint64) (*sdk.TxResponse, error) {
	resp, err := clientCtx.BroadcastTxAsync(txBytes)

	// Process broadcast response for errors to update our node-specific tracking
	if resp != nil {
		respBytes, _ := json.Marshal(resp)
		ProcessBroadcastResponse(nodeURL, denom, sequence, respBytes)
	} else if err != nil {
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

// ProcessTxBroadcastResult processes a transaction broadcast result and returns a custom response
func ProcessTxBroadcastResult(txResponse *sdk.TxResponse, err error, nodeURL string, sequence uint64) {
	// Check if we have a response code indicating error
	if txResponse != nil && txResponse.Code != 0 {
		// Process the error log to update our node tracking
		if txResponse.RawLog != "" {
			// Handle specific error cases
			ProcessBroadcastResponse(nodeURL, "", sequence, []byte(txResponse.RawLog))
		}
	}
}

// SendTx is a high-level function that builds, signs, and broadcasts a transaction
func SendTx(
	ctx context.Context,
	txParams types.TransactionParams,
	sequence uint64,
	broadcastMode string,
) (*sdk.TxResponse, error) {
	// Build and sign the transaction
	txBytes, err := BuildAndSignTransaction(ctx, txParams, sequence, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build and sign transaction: %w", err)
	}

	// Get client context for broadcast
	clientCtx, err := P2PGetClientContext(txParams.Config, txParams.NodeURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get client context: %w", err)
	}

	// Broadcast using our wrapper functions to ensure sequence & fee tracking
	resp, err := BroadcastTx(ctx, clientCtx, txBytes, txParams, sequence, broadcastMode)
	if err != nil {
		// Check if it's a sequence or fee error that we can recover from
		if strings.Contains(err.Error(), "account sequence mismatch") ||
			strings.Contains(err.Error(), "insufficient fees") {
			// Log retry attempt
			fmt.Printf("Retrying transaction due to recoverable error: %v\n", err)

			// Get updated sequence from error if possible
			// This is a fallback - our ProcessBroadcastResponse should already have updated the sequence
			correctSeq := sequence
			seqRegex := regexp.MustCompile(sequenceMismatchPattern)
			if matches := seqRegex.FindStringSubmatch(err.Error()); len(matches) > 1 {
				correctSeq, _ = strconv.ParseUint(matches[1], 10, 64)
			} else {
				// If we couldn't extract sequence from error, use our cached sequence
				nodeSettings := getNodeSettings(txParams.NodeURL)
				nodeSettings.mutex.RLock()
				if nodeSettings.LastSequence > sequence {
					correctSeq = nodeSettings.LastSequence
				}
				nodeSettings.mutex.RUnlock()
			}

			// Try one more time with corrected sequence
			return SendTx(ctx, txParams, correctSeq, broadcastMode)
		}

		// Not a recoverable error, return it
		return resp, err
	}

	return resp, nil
}
