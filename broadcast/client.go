package broadcast

import (
	"context"
	"fmt"
	"sync"
	"time"

	cometrpc "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	tmtypes "github.com/cometbft/cometbft/types"
)

type BroadcastClient struct {
	client *cometrpc.HTTP
}

var (
	clients    = make(map[string]*BroadcastClient)
	clientsMux sync.RWMutex
)

func GetClient(rpcEndpoint string) (*BroadcastClient, error) {
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

	client := &BroadcastClient{
		client: cmtCli,
	}
	clients[rpcEndpoint] = client
	return client, nil
}

func (b *BroadcastClient) Transaction(txBytes []byte) (*coretypes.ResultBroadcastTx, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	t := tmtypes.Tx(txBytes)
	res, err := b.client.BroadcastTxSync(ctx, t)
	if err != nil {
		return nil, err
	}

	if res.Code != 0 {
		return res, fmt.Errorf("broadcast error code %d: %s", res.Code, res.Log)
	}

	return res, nil
}
