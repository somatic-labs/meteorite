package client

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	sdk "github.com/cosmos/cosmos-sdk/types"
	tx "github.com/cosmos/cosmos-sdk/types/tx"
)

type GRPCClient struct {
	conn     *grpc.ClientConn
	txClient tx.ServiceClient
}

func NewGRPCClient(grpcEndpoint string) (*GRPCClient, error) {
	conn, err := grpc.NewClient(grpcEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}

	txClient := tx.NewServiceClient(conn)

	return &GRPCClient{
		conn:     conn,
		txClient: txClient,
	}, nil
}

func (c *GRPCClient) SendTx(ctx context.Context, txBytes []byte) (*sdk.TxResponse, error) {
	grpcRes, err := c.txClient.BroadcastTx(
		ctx,
		&tx.BroadcastTxRequest{
			Mode:    tx.BroadcastMode_BROADCAST_MODE_SYNC,
			TxBytes: txBytes,
		},
	)
	if err != nil {
		return nil, err
	}

	return grpcRes.TxResponse, nil
}

func (c *GRPCClient) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}
