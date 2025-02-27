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

func CreateBankSendMsg(config types.Config, fromAddress string, msgParams types.MsgParams) (sdk.Msg, string, error) {
	fromAccAddress, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return nil, "", fmt.Errorf("invalid from address: %w", err)
	}

	var toAddressString string

	// Try to use the specified to address if provided
	if msgParams.ToAddress != "" {
		_, err = sdk.AccAddressFromBech32(msgParams.ToAddress)
		// If the address is valid, use it
		if err == nil {
			toAddressString = msgParams.ToAddress
		} else {
			// If the address is invalid, get a random one with the chain's prefix
			fmt.Println("Invalid to address, using random address from balances.csv")
			addressManager := lib.GetAddressManager()
			// Get the chain's bech32 prefix from the from address
			prefix := config.AccountPrefix
			if prefix == "" {
				// Extract prefix from the from address if not specified in config
				prefix = extractBech32Prefix(fromAddress)
			}

			toAddressString, err = addressManager.GetRandomAddressWithPrefix(prefix)
			if err != nil {
				// Fallback to generating a random account if AddressManager fails
				fmt.Printf("Error getting address from AddressManager: %v, falling back to random generation\n", err)
				randomAddr, err := lib.GenerateRandomAccount()
				if err != nil {
					return nil, "", fmt.Errorf("error generating random account: %w", err)
				}
				bech32Addr, addrErr := sdk.Bech32ifyAddressBytes(prefix, randomAddr)
				if addrErr != nil {
					return nil, "", fmt.Errorf("error converting address to bech32: %w", addrErr)
				}
				toAddressString = bech32Addr
			}
		}
	} else {
		// No to address specified, get one from the AddressManager
		fmt.Println("No to address specified, using random address from balances.csv")
		addressManager := lib.GetAddressManager()
		// Get the chain's bech32 prefix from the from address
		prefix := config.AccountPrefix
		if prefix == "" {
			// Extract prefix from the from address if not specified in config
			prefix = extractBech32Prefix(fromAddress)
		}

		toAddressString, err = addressManager.GetRandomAddressWithPrefix(prefix)
		if err != nil {
			// Fallback to generating a random account if AddressManager fails
			fmt.Printf("Error getting address from AddressManager: %v, falling back to random generation\n", err)
			randomAddr, err := lib.GenerateRandomAccount()
			if err != nil {
				return nil, "", fmt.Errorf("error generating random account: %w", err)
			}
			bech32Addr, addrErr := sdk.Bech32ifyAddressBytes(prefix, randomAddr)
			if addrErr != nil {
				return nil, "", fmt.Errorf("error converting address to bech32: %w", addrErr)
			}
			toAddressString = bech32Addr
		}
	}

	// Convert to AccAddress
	toAccAddress, err := sdk.AccAddressFromBech32(toAddressString)
	if err != nil {
		return nil, "", fmt.Errorf("invalid to address: %w", err)
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

	// Get the chain's bech32 prefix from the from address or config
	prefix := config.AccountPrefix
	if prefix == "" {
		// Extract prefix from the from address if not specified in config
		prefix = extractBech32Prefix(fromAddress)
	}

	addressManager := lib.GetAddressManager()

	// Create outputs for each recipient
	outputs := make([]banktypes.Output, 0, numRecipients)

	for i := 0; i < numRecipients; i++ {
		var toAddressString string

		// Use the specified recipient if provided, otherwise get random addresses
		if msgParams.ToAddress != "" {
			_, err = sdk.AccAddressFromBech32(msgParams.ToAddress)
			// If the address is valid, use it
			if err == nil {
				toAddressString = msgParams.ToAddress
			} else {
				// If the address is invalid, get a random one with the chain's prefix
				fmt.Println("Invalid to address, using random address from balances.csv")
				toAddressString, err = addressManager.GetRandomAddressWithPrefix(prefix)
				if err != nil {
					// Fallback to generating a random account if AddressManager fails
					fmt.Printf("Error getting address from AddressManager: %v, falling back to random generation\n", err)
					randomAddr, err := lib.GenerateRandomAccount()
					if err != nil {
						return nil, "", fmt.Errorf("error generating random account: %w", err)
					}
					bech32Addr, addrErr := sdk.Bech32ifyAddressBytes(prefix, randomAddr)
					if addrErr != nil {
						return nil, "", fmt.Errorf("error converting address to bech32: %w", addrErr)
					}
					toAddressString = bech32Addr
				}
			}
		} else {
			// No to address specified, get one from the AddressManager
			toAddressString, err = addressManager.GetRandomAddressWithPrefix(prefix)
			if err != nil {
				// Fallback to generating a random account if AddressManager fails
				fmt.Printf("Error getting address from AddressManager: %v, falling back to random generation\n", err)
				randomAddr, err := lib.GenerateRandomAccount()
				if err != nil {
					return nil, "", fmt.Errorf("error generating random account: %w", err)
				}
				bech32Addr, addrErr := sdk.Bech32ifyAddressBytes(prefix, randomAddr)
				if addrErr != nil {
					return nil, "", fmt.Errorf("error converting address to bech32: %w", addrErr)
				}
				toAddressString = bech32Addr
			}
		}

		// Add this recipient to the outputs
		outputs = append(outputs, banktypes.Output{
			Address: toAddressString,
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
