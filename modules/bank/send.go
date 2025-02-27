package bank

import (
	"fmt"

	"github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

func CreateBankSendMsg(config types.Config, fromAddress string, msgParams types.MsgParams) (sdk.Msg, string, error) {
	fromAccAddress, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return nil, "", fmt.Errorf("invalid from address: %w", err)
	}

	toAccAddress, err := sdk.AccAddressFromBech32(msgParams.ToAddress)
	if err != nil {
		fmt.Println("invalid to address, spamming random new accounts")
		toAccAddress, err = lib.GenerateRandomAccount()
		if err != nil {
			return nil, "", fmt.Errorf("error generating random account: %w", err)
		}
	}

	amount := sdk.NewCoins(sdk.NewCoin(config.Denom, sdkmath.NewInt(msgParams.Amount)))

	msg := banktypes.NewMsgSend(fromAccAddress, toAccAddress, amount)

	memo, err := lib.GenerateRandomStringOfLength(256)
	if err != nil {
		return nil, "", fmt.Errorf("error generating random memo: %w", err)
	}

	return msg, memo, nil
}

// CreateBankMultiSendMsg creates a bank multisend message that sends tokens to multiple recipients
// in a single transaction. It uses the num_multisend config parameter to determine how many
// transactions to include in a single multisend.
func CreateBankMultiSendMsg(config types.Config, fromAddress string, msgParams types.MsgParams) (sdk.Msg, string, error) {
	fromAccAddress, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return nil, "", fmt.Errorf("invalid from address: %w", err)
	}

	// Calculate the total amount to send (amount per recipient * number of recipients)
	numRecipients := config.NumMultisend
	if numRecipients <= 0 {
		numRecipients = 1 // Default to 1 if not properly configured
	}

	amountPerRecipient := sdkmath.NewInt(msgParams.Amount)
	totalAmount := amountPerRecipient.MulRaw(int64(numRecipients))

	// Create the input for the multisend (from the sender)
	input := banktypes.Input{
		Address: fromAccAddress.String(),
		Coins:   sdk.NewCoins(sdk.NewCoin(config.Denom, totalAmount)),
	}

	// Create outputs for each recipient
	outputs := make([]banktypes.Output, 0, numRecipients)

	for i := 0; i < numRecipients; i++ {
		var toAccAddress sdk.AccAddress

		// Use the specified recipient if provided, otherwise generate random accounts
		if msgParams.ToAddress != "" {
			toAccAddress, err = sdk.AccAddressFromBech32(msgParams.ToAddress)
			if err != nil {
				fmt.Println("invalid to address, spamming random new accounts")
				toAccAddress, err = lib.GenerateRandomAccount()
				if err != nil {
					return nil, "", fmt.Errorf("error generating random account: %w", err)
				}
			}
		} else {
			// Generate a random account for each recipient
			toAccAddress, err = lib.GenerateRandomAccount()
			if err != nil {
				return nil, "", fmt.Errorf("error generating random account: %w", err)
			}
		}

		// Add this recipient to the outputs
		outputs = append(outputs, banktypes.Output{
			Address: toAccAddress.String(),
			Coins:   sdk.NewCoins(sdk.NewCoin(config.Denom, amountPerRecipient)),
		})
	}

	// Create the multisend message
	msg := &banktypes.MsgMultiSend{
		Inputs:  []banktypes.Input{input},
		Outputs: outputs,
	}

	// Generate a random memo
	memo, err := lib.GenerateRandomStringOfLength(256)
	if err != nil {
		return nil, "", fmt.Errorf("error generating random memo: %w", err)
	}

	return msg, memo, nil
}
