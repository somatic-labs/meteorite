package ibc

import (
	"encoding/json"
	"fmt"
	"strings"

	transfertypes "github.com/cosmos/ibc-go/v7/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v7/modules/core/02-client/types"
	lib "github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func CreateIBCTransferMsg(config types.Config, fromAddress string, msgParams types.MsgParams) (sdk.Msg, string, error) {
	token := sdk.NewCoin(config.Denom, sdkmath.NewInt(msgParams.Amount))
	memoStruct := NewMemo(config)
	jsonMemo, err := memoStruct.ToJSON()
	if err != nil {
		return nil, "", fmt.Errorf("error converting memo to JSON: %w", err)
	}
	ibcaddr, err := lib.GenerateRandomString(config)
	if err != nil {
		return nil, "", err
	}
	ibcaddr = sdk.MustBech32ifyAddressBytes(config.Prefix, []byte(ibcaddr))
	msg := transfertypes.NewMsgTransfer(
		"transfer",
		config.Channel,
		token,
		fromAddress,
		ibcaddr,
		clienttypes.NewHeight(uint64(config.RevisionNumber), uint64(config.TimeoutHeight)),
		uint64(0),
		jsonMemo,
	)
	return msg, jsonMemo, nil
}

// NewMemo creates a new Memo struct with default values
func NewMemo(config types.Config) *Memo {
	return &Memo{
		Forward: Forward{
			Receiver: strings.Repeat(config.IbCMemo, config.IbCMemoRepeat), // Note: This is an invalid bech32 address
			Port:     "transfer",
			Channel:  "channel-569",
			Timeout:  "12h",
			Retries:  10,
		},
	}
}

// ToJSON converts the Memo struct to a JSON string
func (m *Memo) ToJSON() (string, error) {
	bytes, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// Memo represents the structure of the IBC memo field in the transaction
type Memo struct {
	Forward Forward `json:"forward"`
}

// Forward contains details about the forwarding information
type Forward struct {
	Receiver string   `json:"receiver"`
	Port     string   `json:"port"`
	Channel  string   `json:"channel"`
	Timeout  string   `json:"timeout"`
	Retries  int      `json:"retries"`
	Next     *Forward `json:"next,omitempty"`
}
