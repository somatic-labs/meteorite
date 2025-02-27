package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cosmos/go-bip39"
	"github.com/cosmos/ibc-go/modules/apps/callbacks/testing/simapp/params"
	"github.com/somatic-labs/meteorite/broadcast"
	"github.com/somatic-labs/meteorite/lib"
	"github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestExtractExpectedSequence(t *testing.T) {
	tests := []struct {
		name    string
		errMsg  string
		want    uint64
		wantErr bool
	}{
		{
			name:    "valid error message",
			errMsg:  "account sequence mismatch, expected 42, got 41: incorrect account sequence",
			want:    42,
			wantErr: false,
		},
		{
			name:    "missing expected keyword",
			errMsg:  "account sequence mismatch, sequence 42, got 41",
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid sequence number",
			errMsg:  "account sequence mismatch, expected abc, got 41",
			want:    0,
			wantErr: true,
		},
		{
			name:    "empty error message",
			errMsg:  "",
			want:    0,
			wantErr: true,
		},
		{
			name:    "large sequence number",
			errMsg:  "account sequence mismatch, expected 18446744073709551615, got 41",
			want:    18446744073709551615,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lib.ExtractExpectedSequence(tt.errMsg)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractExpectedSequence() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("extractExpectedSequence() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTransferFunds(t *testing.T) {
	// Generate a random mnemonic
	entropy, err := bip39.NewEntropy(256)
	if err != nil {
		t.Fatalf("Failed to generate entropy: %v", err)
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		t.Fatalf("Failed to generate mnemonic: %v", err)
	}

	// Create key from mnemonic
	seed := bip39.NewSeed(mnemonic, "")
	master, ch := hd.ComputeMastersFromSeed(seed)
	path := hd.NewFundraiserParams(0, sdk.CoinType, 0).String()
	privKey, err := hd.DerivePrivateKeyForPath(master, ch, path)
	if err != nil {
		t.Fatalf("Failed to derive private key: %v", err)
	}

	secp256k1PrivKey := &secp256k1.PrivKey{Key: privKey}
	pubKey := secp256k1PrivKey.PubKey()

	tests := []struct {
		name          string
		sender        types.Account
		receiver      string
		amount        sdkmath.Int
		config        types.Config
		expectedError string
	}{
		{
			name: "invalid prefix",
			sender: types.Account{
				PrivKey:  secp256k1PrivKey,
				PubKey:   pubKey,
				Address:  "cosmos1uqrar205hjv4s8832kwj8e6xhwvk4x0eqml043",
				Position: 0,
			},
			receiver: "cosmos1paefpxvjvmmq03gvsfjzwut0zap7z5nq8r99sf",
			amount:   sdkmath.NewInt(3123890412),
			config: types.Config{
				Chain:  "cosmoshub-4",
				Prefix: "cosmos",
				Denom:  "uatom",
				Nodes: types.NodesConfig{
					RPC:  []string{"http://127.0.0.1:26657"},
					API:  "http://localhost:1317",
					GRPC: "localhost:9090",
				},
			},
			expectedError: "failed to get account info",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := TransferFunds(tt.sender, tt.receiver, tt.amount, tt.config)
			if err == nil {
				t.Error("expected error but got none")
				return
			}
			if !strings.Contains(err.Error(), tt.expectedError) {
				t.Errorf("expected error containing %q, got %q", tt.expectedError, err.Error())
			}
		})
	}
}

func TestAdjustBalancesWithSeedPhrase(t *testing.T) {
	// Create a temporary seed phrase file
	mnemonic := []byte("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	tmpfile, err := os.CreateTemp(t.TempDir(), "seedphrase")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write(mnemonic); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatalf("Failed to close temp file: %v", err)
	}

	// Set up test config
	config := types.Config{
		Chain:  "test-chain",
		Prefix: "cosmos",
		Denom:  "uatom",
		Slip44: 118,
		Nodes: types.NodesConfig{
			RPC: []string{"http://localhost:26657"},
			API: "http://localhost:1317",
		},
	}

	// Create test accounts from seed phrase
	var accounts []types.Account
	for i := 0; i < 3; i++ { // Create 3 test accounts
		position := uint32(i)
		privKey, pubKey, acctAddress, err := lib.GetPrivKey(config, mnemonic, position)
		if err != nil {
			t.Fatalf("Failed to generate keys for position %d: %v", position, err)
		}
		accounts = append(accounts, types.Account{
			PrivKey:  privKey,
			PubKey:   pubKey,
			Address:  acctAddress,
			Position: position,
		})
	}

	// Set up mock balances where only the 0th position is funded
	balances := map[string]sdkmath.Int{
		accounts[0].Address: sdkmath.NewInt(1000000),
		accounts[1].Address: sdkmath.ZeroInt(),
		accounts[2].Address: sdkmath.ZeroInt(),
	}

	// Create a test server to mock the API responses
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Mock successful transaction response
		fmt.Fprintln(w, `{"height":"1","txhash":"hash","code":0}`)
	}))
	defer ts.Close()
	config.Nodes.API = ts.URL
	config.Nodes.RPC = []string{ts.URL}

	// Run adjustBalances
	err = adjustBalances(accounts, balances, config)
	if err != nil {
		t.Errorf("adjustBalances() error = %v", err)
	}

	// Verify that balances were attempted to be adjusted
	// Note: In a real scenario, you'd want to verify the actual balance changes,
	// but since we're using a mock server, we're just verifying the function ran without error
}

func TestAdjustBalances(t *testing.T) {
	tests := []struct {
		name     string
		accounts []types.Account
		balances map[string]sdkmath.Int
		config   types.Config
		wantErr  bool
	}{
		{
			name:     "empty accounts list",
			accounts: []types.Account{},
			balances: map[string]sdkmath.Int{},
			config: types.Config{
				Denom: "uom",
			},
			wantErr: true,
		},
		{
			name: "zero total balance",
			accounts: []types.Account{
				{Address: "cosmos1test1"},
				{Address: "cosmos1test2"},
			},
			balances: map[string]sdkmath.Int{
				"cosmos1test1": sdkmath.ZeroInt(),
				"cosmos1test2": sdkmath.ZeroInt(),
			},
			config: types.Config{
				Denom: "uom",
			},
			wantErr: true,
		},
		{
			name: "uneven balances need adjustment",
			accounts: []types.Account{
				{Address: "cosmos1test1"},
				{Address: "cosmos1test2"},
			},
			balances: map[string]sdkmath.Int{
				"cosmos1test1": sdkmath.NewInt(1000000),
				"cosmos1test2": sdkmath.NewInt(0),
			},
			config: types.Config{
				Denom: "uom",
				Nodes: types.NodesConfig{
					RPC: []string{"http://localhost:26657"},
					API: "http://localhost:1317",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adjustBalances(tt.accounts, tt.balances, tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("adjustBalances() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildAndSignTransaction(t *testing.T) {
	tests := []struct {
		name       string
		txParams   types.TransactionParams
		sequence   uint64
		wantErr    bool
		errorMatch string
	}{
		{
			name: "invalid message type",
			txParams: types.TransactionParams{
				MsgType: "invalid_type",
				Config: types.Config{
					Denom: "uatom",
				},
			},
			sequence:   0,
			wantErr:    true,
			errorMatch: "unsupported message type",
		},
		{
			name: "missing private key",
			txParams: types.TransactionParams{
				MsgType: "bank_send",
				Config: types.Config{
					Denom: "uatom",
				},
				PrivKey: nil,
			},
			sequence:   0,
			wantErr:    true,
			errorMatch: "invalid from address: empty address string is not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			encodingConfig := params.MakeTestEncodingConfig()
			_, err := broadcast.BuildAndSignTransaction(ctx, tt.txParams, tt.sequence, encodingConfig)
			if (err != nil) != tt.wantErr {
				t.Errorf("BuildAndSignTransaction() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && !strings.Contains(err.Error(), tt.errorMatch) {
				t.Errorf("BuildAndSignTransaction() error = %v, want error containing %v", err, tt.errorMatch)
			}
		})
	}
}

func TestGetAccountInfo(t *testing.T) {
	// Create a test server to mock the API responses
	tests := []struct {
		name       string
		address    string
		mockResp   string
		wantSeq    uint64
		wantAccNum uint64
		wantErr    bool
	}{
		{
			name:    "valid response",
			address: "cosmos1test1",
			mockResp: `{
                "account": {
                    "sequence": "42",
                    "account_number": "26"
                }
            }`,
			wantSeq:    42,
			wantAccNum: 26,
			wantErr:    false,
		},
		{
			name:    "invalid sequence",
			address: "cosmos1test2",
			mockResp: `{
                "account": {
                    "sequence": "invalid",
                    "account_number": "26"
                }
            }`,
			wantSeq:    0,
			wantAccNum: 0,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintln(w, tt.mockResp)
			}))
			defer ts.Close()

			config := types.Config{
				Nodes: types.NodesConfig{
					API: ts.URL,
				},
			}

			seq, accNum, err := lib.GetAccountInfo(tt.address, config)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetAccountInfo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if seq != tt.wantSeq || accNum != tt.wantAccNum {
				t.Errorf("GetAccountInfo() = (%v, %v), want (%v, %v)",
					seq, accNum, tt.wantSeq, tt.wantAccNum)
			}
		})
	}
}
