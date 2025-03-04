package broadcast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	types "github.com/somatic-labs/meteorite/types"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// ValidateTxParams validates the transaction parameters
func ValidateTxParams(txParams *types.TxParams) error {
	if txParams == nil {
		return errors.New("transaction parameters cannot be nil")
	}

	if txParams.ChainID == "" {
		return errors.New("chain ID cannot be empty")
	}

	if txParams.MsgType == "" {
		return errors.New("message type cannot be empty")
	}

	if txParams.PrivKey == nil {
		return errors.New("private key cannot be nil")
	}

	// Check if from address is valid
	fromAddress, ok := txParams.MsgParams["from_address"].(string)
	if !ok || fromAddress == "" {
		return errors.New("from address cannot be empty or invalid")
	}

	_, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return fmt.Errorf("invalid from address: %w", err)
	}

	return nil
}

// GetClientContext returns a client context for interacting with the chain
func GetClientContext(config types.Config, nodeURL string) (client.Context, error) {
	// Create codec and registry
	ir := codectypes.NewInterfaceRegistry()

	// Register necessary interfaces
	cryptocodec.RegisterInterfaces(ir)
	authtypes.RegisterInterfaces(ir)
	banktypes.RegisterInterfaces(ir)

	localCdc := codec.NewProtoCodec(ir)

	// Create the transaction config using the auth/tx module
	txConfig := authtx.NewTxConfig(localCdc, authtx.DefaultSignModes)

	// Use the provided node URL or default to the first RPC node
	rpcEndpoint := nodeURL
	if rpcEndpoint == "" && len(config.Nodes.RPC) > 0 {
		rpcEndpoint = config.Nodes.RPC[0]
	}

	// Create an RPC client
	rpcClient, err := rpchttp.New(rpcEndpoint, "/websocket")
	if err != nil {
		return client.Context{}, fmt.Errorf("failed to create RPC client: %w", err)
	}

	clientCtx := client.Context{
		ChainID:           config.Chain,
		Codec:             localCdc,
		InterfaceRegistry: ir,
		Output:            nil, // No output writer needed for this context
		OutputFormat:      "json",
		BroadcastMode:     "block", // Use block broadcast mode
		TxConfig:          txConfig,
		AccountRetriever:  authtypes.AccountRetriever{},
		NodeURI:           rpcEndpoint,
		Client:            rpcClient,
	}

	return clientCtx, nil
}

// GetAccountInfo retrieves the account number and sequence for an address
func GetAccountInfo(ctx context.Context, clientCtx client.Context, fromAddress string) (uint64, uint64, error) {
	// Parse the address
	address, err := sdk.AccAddressFromBech32(fromAddress)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid address format: %w", err)
	}

	// Create account retriever
	accountRetriever := authtypes.AccountRetriever{}

	// Ensure the node is reachable before proceeding
	rpcClient := clientCtx.Client
	if rpcClient == nil {
		return 0, 0, errors.New("RPC client is not initialized")
	}

	// Try to get latest block info to check connection
	_, err = rpcClient.Status(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("node connection error: %w", err)
	}

	// Get account information from the chain
	accNum, sequence, err := accountRetriever.GetAccountNumberSequence(clientCtx, address)
	if err != nil {
		return 0, 0, fmt.Errorf("error getting account info: %w", err)
	}

	return accNum, sequence, nil
}

// MakeEncodingConfig creates an encoding configuration for transactions
func MakeEncodingConfig() codec.ProtoCodecMarshaler {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	marshaler := codec.NewProtoCodec(interfaceRegistry)

	// Register common interfaces
	authtypes.RegisterInterfaces(interfaceRegistry)
	banktypes.RegisterInterfaces(interfaceRegistry)

	return marshaler
}

// GetHTTPClientContext creates a client context for HTTP transaction handling
// This is the HTTP-specific version of GetClientContext
func GetHTTPClientContext(config types.Config, nodeURL string) (client.Context, error) {
	var clientContext client.Context

	// Create keyring (use in-memory keyring for simplicity)
	kr := keyring.NewInMemory(nil)

	// Create client context
	clientContext = client.Context{
		ChainID:       config.Chain,
		NodeURI:       nodeURL,
		Keyring:       kr,
		BroadcastMode: "sync", // This will be overridden by the broadcast function
		Simulate:      false,
		GenerateOnly:  false,
		Offline:       false,
		SkipConfirm:   true,
	}

	return clientContext, nil
}

// GetKeyringFromBackend gets a keyring from a backend
func GetKeyringFromBackend(backend string) (keyring.Keyring, error) {
	var kr keyring.Keyring
	var err error

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %v", err)
	}

	switch backend {
	case "test":
		kr, err = keyring.New("cosmos", keyring.BackendTest, homeDir, os.Stdin, nil)
	case "file":
		kr, err = keyring.New("cosmos", keyring.BackendFile, homeDir, os.Stdin, nil)
	case "os":
		kr, err = keyring.New("cosmos", keyring.BackendOS, homeDir, os.Stdin, nil)
	case "memory":
		kr = keyring.NewInMemory(nil)
		err = nil
	default:
		kr = keyring.NewInMemory(nil)
		err = nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create keyring: %v", err)
	}

	return kr, nil
}

// IsNodeReachable checks if a node is reachable
func IsNodeReachable(nodeURL string) bool {
	if nodeURL == "" {
		return false
	}

	// Create a new HTTP client with a timeout
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Make a health check request
	healthURL := nodeURL
	if !strings.HasSuffix(nodeURL, "/health") {
		healthURL = strings.TrimSuffix(nodeURL, "/") + "/health"
	}

	req, err := http.NewRequest(http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Read the response body
	_, err = io.ReadAll(resp.Body)
	if err != nil {
		return false
	}

	return resp.StatusCode == http.StatusOK
}

// GetNodeInfo gets information about a node
func GetNodeInfo(nodeURL string) (types.NodeInfo, error) {
	var nodeInfo types.NodeInfo

	if nodeURL == "" {
		return nodeInfo, errors.New("node URL is empty")
	}

	// Create a new HTTP client with a timeout
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Make a status request
	statusURL := nodeURL
	if !strings.HasSuffix(nodeURL, "/status") {
		statusURL = strings.TrimSuffix(nodeURL, "/") + "/status"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return nodeInfo, fmt.Errorf("failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nodeInfo, fmt.Errorf("failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nodeInfo, fmt.Errorf("status request failed with status code: %d", resp.StatusCode)
	}

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nodeInfo, fmt.Errorf("failed to read response body: %v", err)
	}

	// Parse the response
	var statusResp map[string]interface{}
	err = json.Unmarshal(body, &statusResp)
	if err != nil {
		return nodeInfo, fmt.Errorf("failed to parse response: %v", err)
	}

	// Extract the node info
	result, ok := statusResp["result"].(map[string]interface{})
	if !ok {
		return nodeInfo, errors.New("invalid response format")
	}

	nodeInfoData, ok := result["node_info"].(map[string]interface{})
	if !ok {
		return nodeInfo, errors.New("invalid node info format")
	}

	// Get the network value from the node info data
	if network, ok := nodeInfoData["network"].(string); ok {
		nodeInfo.Network = network
	}

	return nodeInfo, nil
}
