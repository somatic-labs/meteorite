package types

import (
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
)

// TransactionParams contains parameters needed for building a transaction
type TransactionParams struct {
	Config      Config
	NodeURL     string
	ChainID     string
	Sequence    uint64
	AccNum      uint64
	PrivKey     cryptotypes.PrivKey
	PubKey      cryptotypes.PubKey
	AcctAddress string
	MsgType     string
	MsgParams   map[string]interface{}
	Distributor interface{} // Used for MultiSendDistributor to avoid circular imports
}

// GasSettings contains gas-related settings for transaction building
type GasSettings struct {
	// Whether to use simulation to determine gas
	UseSimulation bool
	// Safety buffer to apply to simulated gas (e.g., 1.3 = 30% buffer)
	SafetyBuffer float64
	// Force zero gas (if node accepts it)
	ForceZeroGas bool
	// Allow zero gas if the node supports it
	AllowZeroGas bool
}

// TxParams contains parameters needed for transaction building with additional options
type TxParams struct {
	// Basic transaction parameters
	Config    Config
	NodeURL   string
	ChainID   string
	PrivKey   cryptotypes.PrivKey
	MsgType   string
	MsgParams map[string]interface{}

	// Optional transaction parameters
	Memo          string
	TimeoutHeight uint64
	Gas           *GasSettings
}

// ConvertMsgParamsToMap converts MsgParams struct to map[string]interface{}
func ConvertMsgParamsToMap(params MsgParams) map[string]interface{} {
	result := make(map[string]interface{})
	result["from_address"] = params.FromAddress
	result["to_address"] = params.ToAddress
	result["amount"] = params.Amount
	result["denom"] = params.Denom
	result["receiver"] = params.Receiver
	result["wasm_file"] = params.WasmFile
	result["code_id"] = params.CodeID
	result["init_msg"] = params.InitMsg
	result["contract_addr"] = params.ContractAddr
	result["exec_msg"] = params.ExecMsg
	result["label"] = params.Label
	result["msg_type"] = params.MsgType
	return result
}
