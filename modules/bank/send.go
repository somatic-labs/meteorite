package bank

import (
	"fmt"
	"strings"

	"github.com/somatic-labs/meteorite/lib"
	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// extractBech32Prefix extracts the prefix from a bech32 address
func extractBech32Prefix(address string) string {
	parts := strings.Split(address, "1")
	if len(parts) > 0 {
		return parts[0]
	}
	return "cosmos" // Default fallback
}

// getRecipientAddress returns a valid recipient address from the provided address or generates a random one
func getRecipientAddress(specifiedAddress string, config types.Config, fromAddress string) (string, error) {
	// Try to use the specified address if provided
	if specifiedAddress != "" {
		_, err := sdk.AccAddressFromBech32(specifiedAddress)
		// If the address is valid, use it directly
		if err == nil {
			return specifiedAddress, nil
		}
		fmt.Println("Invalid to address, using random address from balances.csv")
	} else {
		fmt.Println("No to address specified, using random address from balances.csv")
	}

	// When we reach here, we need to get a random address
	addressManager := lib.GetAddressManager()

	// Get the chain's bech32 prefix from the from address or config
	prefix := config.AccountPrefix
	if prefix == "" {
		// Extract prefix from the from address if not specified in config
		prefix = extractBech32Prefix(fromAddress)
	}

	// Try to get address from AddressManager
	toAddressString, err := addressManager.GetRandomAddressWithPrefix(prefix)
	if err == nil {
		return toAddressString, nil
	}

	// Fallback to generating a random account if AddressManager fails
	fmt.Printf("Error getting address from AddressManager: %v, falling back to random generation\n", err)
	return generateRandomAddressWithPrefix(prefix)
}

// generateRandomAddressWithPrefix generates a random address with the given prefix
func generateRandomAddressWithPrefix(prefix string) (string, error) {
	randomAddr, err := lib.GenerateRandomAccount()
	if err != nil {
		return "", fmt.Errorf("error generating random account: %w", err)
	}

	bech32Addr, addrErr := sdk.Bech32ifyAddressBytes(prefix, randomAddr)
	if addrErr != nil {
		return "", fmt.Errorf("error converting address to bech32: %w", addrErr)
	}

	return bech32Addr, nil
}

// createSendMsg creates a MsgSend message from the given parameters
func createSendMsg(msgParams SendMsgParams, fromAddress string, config types.Config) (sdk.Msg, string, error) {
	// Validate the from address
	fromAccAddress, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return nil, "", fmt.Errorf("invalid from address: %w", err)
	}

	// Get the recipient address
	toAddressString, err := getRecipientAddress(msgParams.ToAddress, config, fromAddress)
	if err != nil {
		return nil, "", err
	}

	// Convert to AccAddress
	toAccAddress, err := sdk.AccAddressFromBech32(toAddressString)
	if err != nil {
		return nil, "", fmt.Errorf("invalid to address: %w", err)
	}

	amount := sdk.NewCoins(sdk.NewCoin(config.Denom, sdkmath.NewInt(msgParams.Amount)))

	msg := banktypes.NewMsgSend(fromAccAddress, toAccAddress, amount)

	// Use provided memo or generate a random one
	memo := msgParams.Memo
	if memo == "" {
		var err error
		memo, err = lib.GenerateRandomStringOfLength(256)
		if err != nil {
			return nil, "", fmt.Errorf("error generating random memo: %w", err)
		}
	}

	return msg, memo, nil
}

// SendMsgParams are the parameters for bank send messages
type SendMsgParams struct {
	ToAddress string
	Amount    int64
	Denom     string
	Memo      string
}

// CreateBankSendMsg creates a bank send message
func CreateBankSendMsg(config types.Config, fromAddress string, msgParams types.MsgParams) (sdk.Msg, string, error) {
	// Convert the generic MsgParams to our specific SendMsgParams
	params := SendMsgParams{
		ToAddress: msgParams.ToAddress,
		Amount:    msgParams.Amount,
		Denom:     msgParams.Denom,
		Memo:      config.Memo,
	}

	return createSendMsg(params, fromAddress, config)
}

// CreateBankMultiSendMsg creates a bank multi-send message with multiple recipients
func CreateBankMultiSendMsg(config types.Config, fromAddress string, msgParams types.MsgParams) (sdk.Msg, string, error) {
	// Convert the generic MsgParams to our specific SendMsgParams
	params := SendMsgParams{
		ToAddress: msgParams.ToAddress,
		Amount:    msgParams.Amount,
		Denom:     msgParams.Denom,
		Memo:      config.Memo,
	}

	return createMultiSendMsg(params, fromAddress, config, config.NumMultisend)
}

// createMultiSendMsg creates a MsgMultiSend message from the given parameters
func createMultiSendMsg(msgParams SendMsgParams, fromAddress string, config types.Config, numRecipients int) (sdk.Msg, string, error) {
	// Validate the from address
	fromAccAddress, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return nil, "", fmt.Errorf("invalid from address: %w", err)
	}

	// Parse amount
	amt := sdkmath.NewInt(msgParams.Amount)

	// Calculate the amount for each recipient
	recipientAmount := amt.Quo(sdkmath.NewInt(int64(numRecipients)))
	if recipientAmount.IsZero() {
		return nil, "", fmt.Errorf("amount too small to split among %d recipients", numRecipients)
	}

	denom := msgParams.Denom

	// Use provided memo or generate a random one
	memo := msgParams.Memo
	if memo == "" {
		var err error
		memo, err = lib.GenerateRandomStringOfLength(256)
		if err != nil {
			return nil, "", fmt.Errorf("error generating random memo: %w", err)
		}
	}

	// Create input
	input := banktypes.Input{
		Address: fromAccAddress.String(),
		Coins:   sdk.NewCoins(sdk.NewCoin(denom, amt)),
	}

	outputs := make([]banktypes.Output, 0, numRecipients)

	for i := 0; i < numRecipients; i++ {
		// Get the recipient address
		toAddressString, err := getRecipientAddress(msgParams.ToAddress, config, fromAddress)
		if err != nil {
			return nil, "", err
		}

		// Convert to AccAddress
		toAccAddress, err := sdk.AccAddressFromBech32(toAddressString)
		if err != nil {
			return nil, "", fmt.Errorf("invalid to address: %w", err)
		}

		// Add to outputs
		outputs = append(outputs, banktypes.Output{
			Address: toAccAddress.String(),
			Coins:   sdk.NewCoins(sdk.NewCoin(denom, recipientAmount)),
		})
	}

	// Create the message
	msg := &banktypes.MsgMultiSend{
		Inputs:  []banktypes.Input{input},
		Outputs: outputs,
	}

	return msg, memo, nil
}
