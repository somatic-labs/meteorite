package broadcast

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	cometrpc "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/prometheus/client_golang/prometheus"
)

type Client struct {
	client  *cometrpc.HTTP
	pool    chan *cometrpc.HTTP
	metrics *ClientMetrics
}

var (
	clients    = make(map[string]*Client)
	clientsMux sync.RWMutex
)

const maxPoolSize = 100

func GetClient(rpcEndpoint string) (*Client, error) {
	clientsMux.RLock()
	if client, exists := clients[rpcEndpoint]; exists {
		clientsMux.RUnlock()
		return client, nil
	}
	clientsMux.RUnlock()

	clientsMux.Lock()
	defer clientsMux.Unlock()

	if client, exists := clients[rpcEndpoint]; exists {
		return client, nil
	}

	// Create connection pool
	pool := make(chan *cometrpc.HTTP, maxPoolSize)
	for i := 0; i < maxPoolSize; i++ {
		// Add HTTP client configuration
		config := &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     30 * time.Second,
				DisableKeepAlives:   false,
				ForceAttemptHTTP2:   true,
				MaxConnsPerHost:     200,
			},
			Timeout: 10 * time.Second,
		}

		cmtCli, err := cometrpc.NewWithClient(rpcEndpoint, "/websocket", config)
		if err != nil {
			return nil, err
		}
		pool <- cmtCli
	}

	client := &Client{
		pool: pool,
	}
	clients[rpcEndpoint] = client
	return client, nil
}

type ClientMetrics struct {
	poolGetLatency    prometheus.Histogram
	poolReturnLatency prometheus.Histogram
	rpcLatency        prometheus.Histogram
}

func (b *Client) Transaction(txBytes []byte) (*coretypes.ResultBroadcastTx, error) {
	metrics := &BroadcastMetrics{
		Start:           time.Now(),
		PoolGetStart:    time.Time{},
		RpcCallStart:    time.Time{},
		PoolReturnStart: time.Time{},
		Complete:        time.Time{},
	}
	defer metrics.LogTiming()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get client from pool with timeout
	metrics.PoolGetStart = time.Now()
	select {
	case client := <-b.pool:
		if b.metrics != nil {
			b.metrics.poolGetLatency.Observe(time.Since(metrics.PoolGetStart).Seconds())
		}

		// Execute RPC call
		metrics.RpcCallStart = time.Now()
		result, err := client.BroadcastTxSync(ctx, txBytes)
		if b.metrics != nil {
			b.metrics.rpcLatency.Observe(time.Since(metrics.RpcCallStart).Seconds())
		}

		// Return client to pool with timeout
		metrics.PoolReturnStart = time.Now()
		select {
		case b.pool <- client:
			if b.metrics != nil {
				b.metrics.poolReturnLatency.Observe(time.Since(metrics.PoolReturnStart).Seconds())
			}
		case <-time.After(100 * time.Millisecond):
			log.Printf("Warning: Client pool return timed out after 100ms")
		}

		metrics.Complete = time.Now()
		return result, err

	case <-ctx.Done():
		metrics.Complete = time.Now()
		return nil, fmt.Errorf("timeout waiting for client from pool: %v", ctx.Err())
	}
}

type BroadcastMetrics struct {
	Start           time.Time
	PoolGetStart    time.Time
	RpcCallStart    time.Time
	PoolReturnStart time.Time
	Complete        time.Time
}

func (m *BroadcastMetrics) LogTiming() {
	poolGetTime := m.RpcCallStart.Sub(m.PoolGetStart)
	rpcCallTime := m.PoolReturnStart.Sub(m.RpcCallStart)
	poolReturnTime := m.Complete.Sub(m.PoolReturnStart)
	totalTime := m.Complete.Sub(m.Start)

	log.Printf("Broadcast timing - Pool Get: %v, RPC Call: %v, Pool Return: %v, Total: %v",
		poolGetTime,
		rpcCallTime,
		poolReturnTime,
		totalTime,
	)
}
