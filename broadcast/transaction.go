package broadcast

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/client"
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
	minGasBankMultiSend = 100000
	minGasIbcTransfer   = 150000
	minGasWasmExecute   = 300000
	minGasDefault       = 100000
)

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
			txParams.Config,
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
			gsm.RecordTransactionResult(txParams.NodeURL, true, 0)
		}()
	}

	return txBytes, nil
}

// createMessage creates a message based on the message type and parameters
func createMessage(txParams *types.TxParams) (sdk.Msg, error) {
	switch txParams.MsgType {
	case "bank_send":
		// Extract parameters for bank send message
		toAddress, ok := txParams.MsgParams["to_address"].(string)
		if !ok {
			return nil, errors.New("missing or invalid to_address for bank_send")
		}

		amountVal, ok := txParams.MsgParams["amount"]
		if !ok {
			return nil, errors.New("missing amount for bank_send")
		}

		// Handle different types for amount
		var amount int64
		switch v := amountVal.(type) {
		case int64:
			amount = v
		case int:
			amount = int64(v)
		case float64:
			amount = int64(v)
		case string:
			// Try to parse string as int64
			var err error
			amount, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid amount format: %w", err)
			}
		default:
			return nil, errors.New("amount is in unsupported format")
		}

		denomVal, ok := txParams.MsgParams["denom"]
		if !ok {
			// Use default denom from config if not specified
			denomVal = txParams.Config.Denom
		}

		denom, ok := denomVal.(string)
		if !ok {
			return nil, errors.New("invalid denom format")
		}

		fromAddress, ok := txParams.MsgParams["from_address"].(string)
		if !ok {
			return nil, errors.New("missing from_address for bank_send")
		}

		// Convert addresses from bech32 strings to AccAddress objects
		fromAddr, err := sdk.AccAddressFromBech32(fromAddress)
		if err != nil {
			return nil, fmt.Errorf("invalid from_address: %w", err)
		}

		toAddr, err := sdk.AccAddressFromBech32(toAddress)
		if err != nil {
			return nil, fmt.Errorf("invalid to_address: %w", err)
		}

		// Create bank send message
		sdkAmount := sdkmath.NewInt(amount)
		coin := sdk.NewCoin(denom, sdkAmount)
		msg := banktypes.NewMsgSend(
			fromAddr,
			toAddr,
			sdk.NewCoins(coin),
		)
		return msg, nil

	// Add more message types here as needed
	default:
		return nil, fmt.Errorf("unsupported message type: %s", txParams.MsgType)
	}
}

// SimulateGas simulates the transaction to determine the actual gas needed
func SimulateGas(
	_ context.Context,
	clientCtx client.Context,
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
func DetermineOptimalGas(ctx context.Context, clientCtx client.Context, _ tx.Factory, buffer float64, msgs ...sdk.Msg) (uint64, error) {
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

// BuildAndSignTransaction builds and signs a transaction from the provided parameters
func BuildAndSignTransaction(
	ctx context.Context,
	txParams types.TransactionParams,
	sequence uint64,
	_ interface{}, // encodingConfig is not used, as we create our own client context
) ([]byte, error) {
	// We need to ensure the passed-in sequence is used
	txp := &types.TxParams{
		Config:    txParams.Config,
		NodeURL:   txParams.NodeURL,
		ChainID:   txParams.ChainID,
		PrivKey:   txParams.PrivKey,
		MsgType:   txParams.MsgType,
		MsgParams: txParams.MsgParams,
	}

	// Use ClientContext with correct sequence
	clientCtx, err := GetClientContext(txParams.Config, txParams.NodeURL)
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

	// Estimate gas
	gasLimit, err := DetermineOptimalGas(ctx, clientCtx, tx.Factory{}, 1.3, msg)
	if err != nil {
		// Use default if simulation fails
		gasLimit = uint64(txParams.Config.BaseGas)
	}
	txBuilder.SetGasLimit(gasLimit)

	// Set fee
	gasPrice := txParams.Config.Gas.Low
	// Calculate fee as gasPrice (minimum fee required by the chain)
	// Most chains require a minimum fee regardless of gas calculation
	feeAmount := gasPrice

	// Ensure fee is at least the minimum required (usually matching gasPrice)
	if feeAmount < 200 {
		// Many chains require at least 200 tokens as minimum fee
		feeAmount = 200
	}

	feeCoin := fmt.Sprintf("%d%s", feeAmount, txParams.Config.Denom)
	fee, err := sdk.ParseCoinsNormalized(feeCoin)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fee: %w", err)
	}

	fmt.Printf("Setting transaction fee: %s (gas limit: %d, gas price: %d)\n",
		fee.String(), gasLimit, gasPrice)
	txBuilder.SetFeeAmount(fee)

	// Set memo if provided
	txBuilder.SetMemo("")

	// Get account number
	accNum := txParams.AccNum

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

	return txBytes, nil
}
