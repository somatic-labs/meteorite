package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/ibc-go/modules/apps/callbacks/testing/simapp/params"
	"github.com/somatic-labs/meteorite/broadcast"
	"github.com/somatic-labs/meteorite/lib"
	"github.com/somatic-labs/meteorite/types"
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
			got, err := extractExpectedSequence(tt.errMsg)
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
	tests := []struct {
		name          string
		sender        types.Account
		receiver      string
		amount        sdkmath.Int
		config        types.Config
		expectedError string
	}{
		{
			name: "signature verification failure",
			sender: types.Account{
				PrivKey:  nil, // Mock this with a real private key
				PubKey:   nil, // Mock this with corresponding public key
				Address:  "mantra1uqrar205hjv4s8832kwj8e6xhwvk4x0eqml043",
				Position: 0,
			},
			receiver: "mantra1paefpxvjvmmq03gvsfjzwut0zap7z5nq8r99sf",
			amount:   sdkmath.NewInt(3123890412),
			config: types.Config{
				Chain:  "mantra-canary-net-1",
				Prefix: "mantra",
				Denom:  "uom",
				Nodes: types.NodesConfig{
					RPC:  []string{"http://127.0.0.1:26657"},
					API:  "http://localhost:1317",
					GRPC: "localhost:9090",
				},
			},
			expectedError: "signature verification failed",
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
				{Address: "mantra1test1"},
				{Address: "mantra1test2"},
			},
			balances: map[string]sdkmath.Int{
				"mantra1test1": sdkmath.ZeroInt(),
				"mantra1test2": sdkmath.ZeroInt(),
			},
			config: types.Config{
				Denom: "uom",
			},
			wantErr: true,
		},
		{
			name: "uneven balances need adjustment",
			accounts: []types.Account{
				{Address: "mantra1test1"},
				{Address: "mantra1test2"},
			},
			balances: map[string]sdkmath.Int{
				"mantra1test1": sdkmath.NewInt(1000000),
				"mantra1test2": sdkmath.NewInt(0),
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
					Denom: "uom",
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
					Denom: "uom",
				},
				PrivKey: nil,
			},
			sequence:   0,
			wantErr:    true,
			errorMatch: "private key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
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
			address: "mantra1test1",
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
			address: "mantra1test2",
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
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
