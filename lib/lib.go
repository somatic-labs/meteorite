package lib

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

var httpClient = &http.Client{
	Timeout: 10 * time.Second, // Adjusted timeout to 10 seconds
	Transport: &http.Transport{
		MaxIdleConns:        100,              // Increased maximum idle connections
		MaxIdleConnsPerHost: 10,               // Increased maximum idle connections per host
		IdleConnTimeout:     90 * time.Second, // Increased idle connection timeout
		TLSHandshakeTimeout: 10 * time.Second, // Increased TLS handshake timeout
	},
}

func GetAccountInfo(address string, config types.Config) (seqint uint64, accnum uint64, err error) {
	resp, err := HTTPGet(config.Nodes.API + "/cosmos/auth/v1beta1/accounts/" + address)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get initial sequence: %v", err)
	}

	var accountRes types.AccountResult
	err = json.Unmarshal(resp, &accountRes)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to unmarshal account result: %v", err)
	}

	seqint, err = strconv.ParseUint(accountRes.Account.Sequence, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to convert sequence to int: %v", err)
	}

	accnum, err = strconv.ParseUint(accountRes.Account.AccountNumber, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to convert account number to int: %v", err)
	}

	return uint64(seqint), uint64(accnum), nil
}

func GetChainID(nodeURL string) (string, error) {
	resp, err := HTTPGet(nodeURL + "/status")
	if err != nil {
		log.Printf("Failed to get node status: %v", err)
		return "", err
	}

	var statusRes types.NodeStatusResponse
	err = json.Unmarshal(resp, &statusRes)
	if err != nil {
		log.Printf("Failed to unmarshal node status result: %v", err)
		return "", err
	}

	return statusRes.Result.NodeInfo.Network, nil
}

func HTTPGet(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		netErr, ok := err.(net.Error)
		if ok && netErr.Timeout() {
			log.Printf("Request to %s timed out, continuing...", url)
			return nil, nil
		}
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// This function will load our nodes from nodes.toml.
func LoadNodes() []string {
	var config types.Config
	if _, err := toml.DecodeFile("nodes.toml", &config); err != nil {
		log.Fatalf("Failed to load nodes.toml: %v", err)
	}
	return config.Nodes.RPC
}

func GenerateRandomString(config types.Config) (string, error) {
	// Generate a random size between config.RandMin and config.RandMax
	sizeB, err := rand.Int(rand.Reader, big.NewInt(config.RandMax-config.RandMin+1))
	if err != nil {
		return "", err
	}
	sizeB = sizeB.Add(sizeB, big.NewInt(config.RandMin))

	// Calculate the number of bytes to generate (2 characters per byte in hex encoding)
	nBytes := int(sizeB.Int64()) / 2

	randomBytes := make([]byte, nBytes)
	_, err = rand.Read(randomBytes)
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(randomBytes), nil
}

func GenerateRandomStringOfLength(n int) (string, error) {
	letters := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	b := make([]rune, n)
	for i := range b {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return "", err
		}
		b[i] = letters[num.Int64()]
	}
	return string(b), nil
}

func GenerateRandomAccount() (sdk.AccAddress, error) {
	// Generate 20 random bytes
	randomBytes := make([]byte, 20)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return nil, err
	}

	// Create an AccAddress from the random bytes
	accAddress := sdk.AccAddress(randomBytes)

	return accAddress, nil
}

func GetBalances(accounts []types.Account, config types.Config) (map[string]sdkmath.Int, error) {
	balances := make(map[string]sdkmath.Int)
	for _, account := range accounts {
		balance, err := GetAccountBalance(account.Address, config)
		if err != nil {
			return nil, err
		}
		balances[account.Address] = balance
	}
	return balances, nil
}

func GetAccountBalance(address string, config types.Config) (sdkmath.Int, error) {
	resp, err := HTTPGet(config.Nodes.API + "/cosmos/bank/v1beta1/balances/" + address)
	if err != nil {
		return sdkmath.ZeroInt(), err
	}

	var balanceRes types.BalanceResult
	err = json.Unmarshal(resp, &balanceRes)
	if err != nil {
		return sdkmath.ZeroInt(), err
	}

	for _, coin := range balanceRes.Balances {
		if coin.Denom == config.Denom {
			amount, ok := sdkmath.NewIntFromString(coin.Amount)
			if !ok {
				return sdkmath.ZeroInt(), errors.New("invalid coin amount")
			}
			return amount, nil
		}
	}

	// If no balance found for the denom, return zero balance
	return sdkmath.ZeroInt(), nil
}

func CheckBalancesWithinThreshold(balances map[string]sdkmath.Int, threshold float64) bool {
	var amounts []sdkmath.Int
	for _, amount := range balances {
		amounts = append(amounts, amount)
	}

	if len(amounts) == 0 {
		return true
	}

	maxBalance := amounts[0]
	minBalance := amounts[0]

	for _, amount := range amounts[1:] {
		if amount.GT(maxBalance) {
			maxBalance = amount
		}
		if amount.LT(minBalance) {
			minBalance = amount
		}
	}

	diff := maxBalance.Sub(minBalance)
	avg := maxBalance.Add(minBalance).Quo(sdkmath.NewInt(2))

	percentageDiff := diff.ToLegacyDec().Quo(avg.ToLegacyDec())
	thresholdDec := sdkmath.LegacyNewDec(int64(threshold))
	return percentageDiff.LTE(thresholdDec)
}

// Function to extract the expected sequence number from the error message
func ExtractExpectedSequence(errMsg string) (uint64, error) {
	// Parse the error message to extract the expected sequence number
	// Example error message:
	// "account sequence mismatch, expected 42, got 41: incorrect account sequence"
	index := strings.Index(errMsg, "expected ")
	if index == -1 {
		return 0, errors.New("expected sequence not found in error message")
	}

	start := index + len("expected ")
	rest := errMsg[start:]
	parts := strings.SplitN(rest, ",", 2)
	if len(parts) < 1 {
		return 0, errors.New("failed to split expected sequence from error message")
	}

	expectedSeqStr := strings.TrimSpace(parts[0])
	expectedSeq, err := strconv.ParseUint(expectedSeqStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse expected sequence number: %v", err)
	}

	return expectedSeq, nil
}
