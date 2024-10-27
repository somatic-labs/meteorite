package lib

import (
	"fmt"

	types "github.com/somatic-labs/meteorite/types"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func GetPrivKey(config types.Config, mnemonic []byte, position uint32) (cryptotypes.PrivKey, cryptotypes.PubKey, string) {
	sdkConfig := sdk.GetConfig()
	sdkConfig.SetBech32PrefixForAccount(config.Prefix, config.Prefix+"pub")
	sdkConfig.SetBech32PrefixForValidator(config.Prefix+"valoper", config.Prefix+"valoperpub")
	sdkConfig.SetBech32PrefixForConsensusNode(config.Prefix+"valcons", config.Prefix+"valconspub")
	sdkConfig.Seal()

	algo := hd.Secp256k1

	hdPath := fmt.Sprintf("m/44'/%d'/0'/0/%d", config.Slip44, position)
	derivedPriv, err := algo.Derive()(string(mnemonic), "", hdPath)
	if err != nil {
		panic(err)
	}

	privKey := algo.Generate()(derivedPriv)
	pubKey := privKey.PubKey()

	addressbytes := sdk.AccAddress(pubKey.Address().Bytes())
	address, err := sdk.Bech32ifyAddressBytes(config.Prefix, addressbytes)
	if err != nil {
		panic(err)
	}

	fmt.Println("Derived Address at position", position, ":", address)

	return privKey, pubKey, address
}
