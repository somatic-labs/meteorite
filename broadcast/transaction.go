package broadcast

import (
	"context"
	"fmt"
	"sync"

	"github.com/cosmos/ibc-go/modules/apps/callbacks/testing/simapp/params"
	"github.com/cosmos/ibc-go/v8/modules/apps/transfer"
	ibc "github.com/cosmos/ibc-go/v8/modules/core"
	meteoritebank "github.com/somatic-labs/meteorite/modules/bank"
	meteoriteibc "github.com/somatic-labs/meteorite/modules/ibc"
	wasm "github.com/somatic-labs/meteorite/modules/wasm"
	"github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/std"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/cosmos/cosmos-sdk/x/bank"
	"github.com/cosmos/cosmos-sdk/x/gov"

	wasmd "github.com/CosmWasm/wasmd/x/wasm"
)

// Initialize codec and register interfaces once at package level
func init() {
	// Register necessary interfaces
	transferModule := transfer.AppModuleBasic{}
	ibcModule := ibc.AppModuleBasic{}
	bankModule := bank.AppModuleBasic{}
	wasmModule := wasmd.AppModuleBasic{}
	govModule := gov.AppModuleBasic{}

	encodingConfig := params.MakeTestEncodingConfig()

	ibcModule.RegisterInterfaces(encodingConfig.InterfaceRegistry)
	transferModule.RegisterInterfaces(encodingConfig.InterfaceRegistry)
	bankModule.RegisterInterfaces(encodingConfig.InterfaceRegistry)
	wasmModule.RegisterInterfaces(encodingConfig.InterfaceRegistry)
	govModule.RegisterInterfaces(encodingConfig.InterfaceRegistry)
	std.RegisterInterfaces(encodingConfig.InterfaceRegistry)

	// Store the encoding config globally
	defaultEncodingConfig = encodingConfig
}

var defaultEncodingConfig params.EncodingConfig

func BuildAndSignTransaction(
	ctx context.Context,
	txParams types.TransactionParams,
	sequence uint64,
	_ params.EncodingConfig, // Keep parameter for compatibility but ignore it
) ([]byte, error) {
	// Use defaultEncodingConfig instead of passed parameter
	txBuilder := defaultEncodingConfig.TxConfig.NewTxBuilder()

	var msg sdk.Msg
	var memo string

	// Construct the message based on the message type
	var err error
	switch txParams.MsgType {
	case "ibc_transfer":
		msg, memo, err = meteoriteibc.CreateIBCTransferMsg(txParams.Config, txParams.AcctAddress, txParams.MsgParams)
	case "bank_send":
		msg, memo, err = meteoritebank.CreateBankSendMsg(txParams.Config, txParams.AcctAddress, txParams.MsgParams)
	case "store_code":
		msg, memo, err = wasm.CreateStoreCodeMsg(txParams.Config, txParams.AcctAddress, txParams.MsgParams)
	case "instantiate_contract":
		msg, memo, err = wasm.CreateInstantiateContractMsg(txParams.Config, txParams.AcctAddress, txParams.MsgParams)
	default:
		return nil, fmt.Errorf("unsupported message type: %s", txParams.MsgType)
	}
	if err != nil {
		return nil, err
	}

	// Set the message and other transaction parameters
	if err := txBuilder.SetMsgs(msg); err != nil {
		return nil, err
	}

	// Estimate gas limit with a buffer
	txSize := len(msg.String())
	baseGas := txParams.Config.BaseGas
	gasPerByte := txParams.Config.GasPerByte

	// Calculate estimated gas
	estimatedGas := uint64(int64(txSize)*gasPerByte + baseGas)

	// Add a buffer (e.g., 20%)
	buffer := uint64(float64(estimatedGas) * 0.2)
	gasLimit := estimatedGas + buffer

	if txParams.Config.Gas.Limit > 0 {
		txBuilder.SetGasLimit(uint64(txParams.Config.Gas.Limit))
	} else {
		txBuilder.SetGasLimit(gasLimit)
	}

	// Calculate fee
	gasPrice := sdk.NewDecCoinFromDec(
		txParams.Config.Denom,
		sdkmath.LegacyNewDecWithPrec(txParams.Config.Gas.Low, txParams.Config.Gas.Precision),
	)
	feeAmount := gasPrice.Amount.MulInt64(int64(gasLimit)).RoundInt()
	feeCoin := sdk.NewCoin(txParams.Config.Denom, feeAmount)
	txBuilder.SetFeeAmount(sdk.NewCoins(feeCoin))

	// Set memo and timeout height
	txBuilder.SetMemo(memo)
	txBuilder.SetTimeoutHeight(0)

	// Set up signature
	sigV2 := signing.SignatureV2{
		PubKey:   txParams.PubKey,
		Sequence: sequence,
		Data: &signing.SingleSignatureData{
			SignMode: signing.SignMode_SIGN_MODE_DIRECT,
		},
	}

	if err := txBuilder.SetSignatures(sigV2); err != nil {
		return nil, err
	}

	signerData := authsigning.SignerData{
		ChainID:       txParams.ChainID,
		AccountNumber: txParams.AccNum,
		Sequence:      sequence,
	}

	fmt.Println("signerData", signerData)

	// Sign the transaction with the private key
	sigV2, err = tx.SignWithPrivKey(
		ctx,
		signing.SignMode_SIGN_MODE_DIRECT,
		signerData,
		txBuilder,
		txParams.PrivKey,
		defaultEncodingConfig.TxConfig,
		sequence,
	)
	if err != nil {
		return nil, err
	}

	// Set the signed signature back to the txBuilder
	if err := txBuilder.SetSignatures(sigV2); err != nil {
		return nil, err
	}

	// Encode the transaction
	txBytes, err := defaultEncodingConfig.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return nil, err
	}

	return txBytes, nil
}

// BatchTransactions batches multiple transactions together and broadcasts them
func BatchTransactions(
	ctx context.Context,
	txParams types.TransactionParams,
	batchSize int,
	sequence uint64,
	_ params.EncodingConfig,
) ([][]byte, error) {
	txBatch := make([][]byte, batchSize)
	errChan := make(chan error, batchSize)
	var wg sync.WaitGroup

	// Create a buffered semaphore to limit concurrent goroutines
	sem := make(chan struct{}, 10)

	for i := 0; i < batchSize; i++ {
		wg.Add(1)
		go func(index int, seq uint64) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Clone txParams and increment sequence
			txParamsClone := txParams
			txParamsClone.Sequence = seq

			txBytes, err := BuildAndSignTransaction(ctx, txParamsClone, seq, defaultEncodingConfig)
			if err != nil {
				errChan <- fmt.Errorf("failed to build transaction %d: %w", index, err)
				return
			}

			txBatch[index] = txBytes
		}(i, sequence+uint64(i))
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		if err != nil {
			return nil, err
		}
	}

	return txBatch, nil
}
