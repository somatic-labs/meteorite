package types

import (
	"cosmossdk.io/log"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
)

type Header struct {
	Height string `json:"height"`
}

type Data struct {
	Txs []string `json:"txs"`
}

type TransmissionStats struct {
	Successful    int
	Failed        int
	ResponseCodes map[uint32]int
}

type Block struct {
	Header Header `json:"header"`
	Data   Data   `json:"data"`
}

type ResultBlock struct {
	Block Block `json:"block"`
}

type BlockResult struct {
	Result ResultBlock `json:"result"`
}

type MempoolResult struct {
	Result Result `json:"result"`
}

type BroadcastRequest struct {
	Jsonrpc                string `json:"jsonrpc"`
	ID                     string `json:"id"`
	Method                 string `json:"method"`
	BroadcastRequestParams `json:"params"`
}

type BroadcastRequestParams struct {
	Tx string `json:"tx"`
}

type BroadcastResponse struct {
	Jsonrpc         string `json:"jsonrpc"`
	ID              string `json:"id"`
	BroadcastResult `json:"result"`
}

type BroadcastResult struct {
	Code      int    `json:"code"`
	Data      string `json:"data"`
	Log       string `json:"log"`
	Codespace string `json:"codespace"`
	Hash      string `json:"hash"`
}

type Result struct {
	NTxs       string      `json:"n_txs"`
	Total      string      `json:"total"`
	TotalBytes string      `json:"total_bytes"`
	Txs        interface{} `json:"txs"` // Assuming txs can be null or an array, interface{} will accommodate both
}

type AccountInfo struct {
	Sequence      string `json:"sequence"`
	AccountNumber string `json:"account_number"`
}

type AccountResult struct {
	Account AccountInfo `json:"account"`
}

type Transaction struct {
	Body       Body     `json:"body"`
	AuthInfo   AuthInfo `json:"auth_info"`
	Signatures []string `json:"signatures"`
}

type Body struct {
	Messages                    []Message `json:"messages"`
	Memo                        string    `json:"memo"`
	TimeoutHeight               string    `json:"timeout_height"`
	ExtensionOptions            []string  `json:"extension_options"`
	NonCriticalExtensionOptions []string  `json:"non_critical_extension_options"`
}

type Message struct {
	Type             string        `json:"@type"`
	SourcePort       string        `json:"source_port"`
	SourceChannel    string        `json:"source_channel"`
	Token            Token         `json:"token"`
	Sender           string        `json:"sender"`
	Receiver         string        `json:"receiver"`
	TimeoutHeight    TimeoutHeight `json:"timeout_height"`
	TimeoutTimestamp string        `json:"timeout_timestamp"`
	Memo             string        `json:"memo"`
}

type Token struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

type TimeoutHeight struct {
	RevisionNumber string `json:"revision_number"`
	RevisionHeight string `json:"revision_height"`
}

type AuthInfo struct {
	SignerInfos []interface{} `json:"signer_infos"`
	Fee         Fee           `json:"fee"`
}

type Fee struct {
	Amount   []Token `json:"amount"`
	GasLimit string  `json:"gas_limit"`
	Payer    string  `json:"payer"`
	Granter  string  `json:"granter"`
}

type Config struct {
	Logger         log.Logger  `toml:"logger"`
	Bytes          int64       `toml:"bytes"`
	Chain          string      `toml:"chain"`
	Channel        string      `toml:"channel"`
	Denom          string      `toml:"denom"`
	Prefix         string      `toml:"prefix"`
	GasPerByte     int64       `toml:"gas_per_byte"`
	BaseGas        int64       `toml:"base_gas"`
	IbCMemo        string      `toml:"ibc_memo"`
	Memo           string      `toml:"memo"`
	IbCMemoRepeat  int         `toml:"ibc_memo_repeat"`
	RandMin        int64       `toml:"rand_min"`
	RandMax        int64       `toml:"rand_max"`
	RevisionNumber int64       `toml:"revision_number"`
	TimeoutHeight  int64       `toml:"timeout_height"`
	Slip44         int         `toml:"slip44"`
	MsgType        string      `toml:"msg_type"`
	MsgParams      MsgParams   `toml:"msg_params"`
	Greed          bool        `toml:"greed"`
	Gas            GasConfig   `toml:"gas"`
	Nodes          NodesConfig `toml:"nodes"`
	BroadcastModes []string    `toml:"broadcast_modes"`
	Positions      uint        `toml:"positions"`
}

type MsgParams struct {
	FromAddress  string `toml:"from_address"`
	Amount       int64  `toml:"amount"`
	Denom        string `toml:"denom"`
	Receiver     string `toml:"receiver"`
	ToAddress    string `toml:"to_address"`
	WasmFile     string `toml:"wasm_file"`
	CodeID       uint64 `toml:"code_id"`
	InitMsg      string `toml:"init_msg"`
	ContractAddr string `toml:"contract_addr"`
	ExecMsg      string `toml:"exec_msg"`
	Label        string `toml:"label"`

	// Add other message-specific parameters as needed
}

type GasConfig struct {
	Zero      int64 `toml:"zero"`
	Low       int64 `toml:"low"`
	Medium    int64 `toml:"medium"`
	High      int64 `toml:"high"`
	Precision int64 `toml:"precision"`
	Limit     int64 `toml:"limit"` // to be set entirely by software
}

type NodesConfig struct {
	RPC  []string `toml:"rpc"`
	API  string   `toml:"api"`
	GRPC string   `toml:"grpc"`
}

type NodeInfo struct {
	Network string `json:"network"`
}

type NodeStatusResult struct {
	NodeInfo NodeInfo `json:"node_info"`
}

type NodeStatusResponse struct {
	Result NodeStatusResult `json:"result"`
}

type Account struct {
	PrivKey  cryptotypes.PrivKey
	PubKey   cryptotypes.PubKey
	Address  string
	Position uint32
}

// BalanceResult represents the response from the bank balances query.
type BalanceResult struct {
	Balances   []Coin     `json:"balances"`
	Pagination Pagination `json:"pagination"`
}

// Coin represents a token balance.
type Coin struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

// Pagination holds pagination information.
type Pagination struct {
	NextKey string `json:"next_key"`
	Total   string `json:"total"`
}
