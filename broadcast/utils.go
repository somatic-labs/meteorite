package broadcast

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	types "github.com/somatic-labs/meteorite/types"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
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

	// First strip any broadcaster or node identification suffixes from the full URL
	// Examples:
	// - "http://example.com:1234 (broadcaster 1)" -> "http://example.com:1234"
	// - "http://example.com:1234 (node 2)" -> "http://example.com:1234"
	baseURL := rpcEndpoint
	broadcasterSuffix := regexp.MustCompile(`\s+\(broadcaster\s+\d+\)`)
	nodeSuffix := regexp.MustCompile(`\s+\(node\s+\d+\)`)

	if broadcasterSuffix.MatchString(rpcEndpoint) {
		baseURL = broadcasterSuffix.ReplaceAllString(rpcEndpoint, "")
		log.Printf("Stripped broadcaster suffix from URL: %s -> %s", rpcEndpoint, baseURL)
	} else if nodeSuffix.MatchString(rpcEndpoint) {
		baseURL = nodeSuffix.ReplaceAllString(rpcEndpoint, "")
		log.Printf("Stripped node suffix from URL: %s -> %s", rpcEndpoint, baseURL)
	}

	// Now parse the cleaned URL to validate it
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		log.Printf("ERROR: Could not parse node URL %s: %v", baseURL, err)
		return client.Context{}, fmt.Errorf("invalid node URL format: %w", err)
	}

	// Additional URL validation
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		log.Printf("ERROR: URL missing scheme or host: %s (scheme=%s, host=%s)",
			baseURL, parsedURL.Scheme, parsedURL.Host)
		return client.Context{}, fmt.Errorf("invalid node URL: missing scheme or host")
	}

	// Create an RPC client with the base URL
	rpcClient, err := rpchttp.New(baseURL, "/websocket")
	if err != nil {
		log.Printf("ERROR: Failed to create RPC client for %s: %v", baseURL, err)
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
		NodeURI:           baseURL, // Use cleaned URL consistently
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
	nodeURI := clientCtx.NodeURI
	_, err = rpcClient.Status(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("node connection error for %s: %w", nodeURI, err)
	}

	// Get account information from the chain
	accNum, sequence, err := accountRetriever.GetAccountNumberSequence(clientCtx, address)
	if err != nil {
		return 0, 0, fmt.Errorf("error getting account info from %s: %w", nodeURI, err)
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
