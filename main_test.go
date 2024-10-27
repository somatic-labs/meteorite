package main

import (
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
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
