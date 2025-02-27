package lib

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cosmos/go-bip39"
	types "github.com/somatic-labs/meteorite/types"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// GetPrivKey derives a private key from a mnemonic at a specific position.
// It now includes proper mnemonic validation and error handling.
func GetPrivKey(config types.Config, mnemonic []byte, position uint32) (privKey cryptotypes.PrivKey, pubKey cryptotypes.PubKey, address string, err error) {
	// Clean and validate mnemonic
	mnemonicStr := strings.TrimSpace(string(mnemonic))

	// Normalize spaces between words
	mnemonicStr = strings.Join(strings.Fields(mnemonicStr), " ")

	// Validate mnemonic
	if !bip39.IsMnemonicValid(mnemonicStr) {
		return nil, nil, "", errors.New("invalid mnemonic: please provide a valid BIP39 mnemonic")
	}

	algo := hd.Secp256k1
	hdPath := fmt.Sprintf("m/44'/%d'/0'/0/%d", config.Slip44, position)

	derivedPriv, err := algo.Derive()(mnemonicStr, "", hdPath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to derive private key: %w", err)
	}

	privKey = algo.Generate()(derivedPriv)
	pubKey = privKey.PubKey()

	addressBytes := sdk.AccAddress(pubKey.Address().Bytes())
	address, err = sdk.Bech32ifyAddressBytes(config.Prefix, addressBytes)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to generate bech32 address: %w", err)
	}

	fmt.Printf("Derived Address at position %d: %s\n", position, address)

	return privKey, pubKey, address, nil
}
