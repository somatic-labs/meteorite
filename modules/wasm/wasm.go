package wasm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
)

func CreateStoreCodeMsg(config types.Config, sender string, msgParams types.MsgParams) (sdk.Msg, string, error) {
	senderAddr, err := sdk.AccAddressFromBech32(sender)
	if err != nil {
		return nil, "", errors.New("invalid sender address: " + err.Error())
	}

	if config.MsgParams.WasmFile == "" {
		return nil, "", errors.New("ConfigWASM file path is empty")
	}

	if msgParams.WasmFile == "" {
		return nil, "", errors.New("WASM file path is empty")
	}

	wasmFile, err := os.ReadFile(config.MsgParams.WasmFile)
	if err != nil {
		return nil, "", errors.New("failed to read WASM file: " + err.Error())
	}

	msg := wasmtypes.MsgStoreCode{
		Sender:       senderAddr.String(),
		WASMByteCode: wasmFile,
	}

	memo, err := lib.GenerateRandomStringOfLength(256)
	if err != nil {
		return nil, "", fmt.Errorf("error generating random memo: %w", err)
	}

	return &msg, memo, nil
}

func CreateInstantiateContractMsg(config types.Config, sender string, msgParams types.MsgParams) (sdk.Msg, string, error) {
	senderAddr, err := sdk.AccAddressFromBech32(sender)
	if err != nil {
		return nil, "", fmt.Errorf("invalid sender address: %w", err)
	}

	initMsg, err := json.Marshal(msgParams.InitMsg)
	if err != nil {
		return nil, "", fmt.Errorf("failed to marshal init message: %w", err)
	}

	funds := sdk.NewCoins(sdk.NewCoin(config.Denom, sdkmath.NewInt(msgParams.Amount)))

	msg := wasmtypes.MsgInstantiateContract{
		Sender: senderAddr.String(),
		Admin:  senderAddr.String(), // Using sender as admin, adjust if needed
		CodeID: msgParams.CodeID,
		Label:  msgParams.Label,
		Msg:    initMsg,
		Funds:  funds,
	}

	memo, err := lib.GenerateRandomStringOfLength(256)
	if err != nil {
		return nil, "", fmt.Errorf("error generating random memo: %w", err)
	}

	return &msg, memo, nil
}

func CreateExecuteContractMsg(config types.Config, sender string, msgParams types.MsgParams) (sdk.Msg, error) {
	senderAddr, err := sdk.AccAddressFromBech32(sender)
	if err != nil {
		return nil, fmt.Errorf("invalid sender address: %w", err)
	}

	contractAddress, err := sdk.AccAddressFromBech32(msgParams.ContractAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid contract address: %w", err)
	}

	execMsg, err := json.Marshal(msgParams.ExecMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal exec message: %w", err)
	}

	funds := sdk.NewCoins(sdk.NewCoin(config.Denom, sdkmath.NewInt(msgParams.Amount)))

	msg := wasmtypes.MsgExecuteContract{
		Sender:   senderAddr.String(),
		Contract: contractAddress.String(),
		Msg:      execMsg,
		Funds:    funds,
	}
	return &msg, nil
}

// Helper function to create a StoreFile message
func CreateStoreFileMsg(data []byte) ([]byte, error) {
	msg := struct {
		StoreFile struct {
			Data []byte `json:"data"`
		} `json:"store_file"`
	}{
		StoreFile: struct {
			Data []byte `json:"data"`
		}{
			Data: data,
		},
	}

	return json.Marshal(msg)
}
