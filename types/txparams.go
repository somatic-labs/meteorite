package types

import (
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
)

type TransactionParams struct {
	Config      Config
	NodeURL     string
	ChainID     string
	Sequence    uint64 // Optional, can be managed separately
	AccNum      uint64
	PrivKey     cryptotypes.PrivKey
	PubKey      cryptotypes.PubKey
	AcctAddress string
	MsgType     string
	MsgParams   MsgParams
}
