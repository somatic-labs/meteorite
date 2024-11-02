package bank

import (
	"fmt"
	"os"

	"github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"
	"golang.org/x/exp/rand"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

func CreateBankSendMsg(config types.Config, fromAddress string, msgParams types.MsgParams) (sdk.Msg, string, error) {
	fromAccAddress, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		config.Logger.Error("invalid from address: %w", err)
		return nil, "", fmt.Errorf("invalid from address: %w", err)
	}

	var toAccAddress sdk.AccAddress
	var toAddress string

	// Handle greed mode
	if config.Greed {
		mnemonic, err := os.ReadFile("seedphrase")
		if err != nil {
			config.Logger.Error("error reading seedphrase: %w", err)
			return nil, "", fmt.Errorf("error reading seedphrase: %w", err)
		}

		// Keep track of tried positions to avoid infinite loop
		triedPositions := make(map[uint32]bool)
		maxAttempts := int(config.Positions) // Try all possible positions if needed

		for attempt := 0; attempt < maxAttempts; attempt++ {
			position := uint32(rand.Intn(int(config.Positions)))

			// Skip if we've already tried this position
			if triedPositions[position] {
				continue
			}
			triedPositions[position] = true

			_, _, randomAddress := lib.GetPrivKey(config, mnemonic, position)

			// Don't send to self
			if randomAddress == fromAddress {
				continue
			}

			toAccAddress, err = sdk.AccAddressFromBech32(randomAddress)
			if err == nil {
				toAddress = randomAddress
				config.Logger.Info("Selected recipient in greed mode",
					"address", toAddress,
					"position", position)
				break
			}
		}

		if toAddress == "" {
			return nil, "", fmt.Errorf("could not find valid recipient after trying all positions")
		}
	} else {
		// Original non-greed mode logic
		if msgParams.ToAddress == "" {
			toAccAddress, err = lib.GenerateRandomAccount()
			if err != nil {
				config.Logger.Error("error generating random account: %w", err)
				return nil, "", fmt.Errorf("error generating random account: %w", err)
			}
			toAddress = toAccAddress.String()
		} else {
			toAccAddress, err = sdk.AccAddressFromBech32(msgParams.ToAddress)
			if err != nil {
				config.Logger.Error("invalid to address: %w", err)
				return nil, "", fmt.Errorf("invalid to address: %w", err)
			}
			toAddress = msgParams.ToAddress
		}
	}

	amount := sdk.NewCoins(sdk.NewCoin(config.Denom, sdkmath.NewInt(msgParams.Amount)))
	msg := banktypes.NewMsgSend(fromAccAddress, toAccAddress, amount)

	config.Logger.Info("Creating bank send message",
		"from", fromAddress,
		"to", toAddress,
		"amount", amount.String())

	return msg, "", nil
}
