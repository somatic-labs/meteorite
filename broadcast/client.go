package broadcast

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	cometrpc "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	tmtypes "github.com/cometbft/cometbft/types"
)

type Client struct {
	client *cometrpc.HTTP
}

var (
	clients    = make(map[string]*Client)
	clientsMux sync.RWMutex
)

func GetClient(rpcEndpoint string) (*Client, error) {
	clientsMux.RLock()
	if client, exists := clients[rpcEndpoint]; exists {
		clientsMux.RUnlock()
		return client, nil
	}
	clientsMux.RUnlock()

	// If client doesn't exist, acquire write lock and create it
	clientsMux.Lock()
	defer clientsMux.Unlock()

	// Double-check after acquiring write lock
	if client, exists := clients[rpcEndpoint]; exists {
		return client, nil
	}

	// Create new client
	cmtCli, err := cometrpc.New(rpcEndpoint, "/websocket")
	if err != nil {
		return nil, err
	}

	client := &Client{
		client: cmtCli,
	}
	clients[rpcEndpoint] = client
	return client, nil
}

func (b *Client) Transaction(txBytes []byte) (*coretypes.ResultBroadcastTx, error) {
	// Increase timeout to 15 seconds for large transactions and slow networks
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t := tmtypes.Tx(txBytes)

	// Use BroadcastTxSync as it provides immediate feedback on transaction validity
	res, err := b.client.BroadcastTxSync(ctx, t)
	if err != nil {
		// Check for specific error types
		if strings.Contains(err.Error(), "timed out") {
			return nil, fmt.Errorf("broadcast timed out after 15s (transaction may still be processed): %w", err)
		}
		if strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "dial tcp") ||
			strings.Contains(err.Error(), "connection reset by peer") {
			return nil, fmt.Errorf("node connectivity issue - node may be offline or unreachable: %w", err)
		}
		return nil, err
	}

	// For non-zero response codes, return the response with error details
	if res.Code != 0 {
		return res, fmt.Errorf("broadcast error code %d: %s", res.Code, res.Log)
	}

	return res, nil
}
