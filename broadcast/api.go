package broadcast

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/somatic-labs/meteorite/types"

	"cosmossdk.io/simapp/params"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx"
)

func SendTransactionViaAPI(txParams types.TransactionParams, sequence uint64) (*sdk.TxResponse, string, error) {
	encodingConfig := params.MakeTestEncodingConfig()
	encodingConfig.Codec = cdc

	logger := txParams.Config.Logger

	// Build and sign the transaction
	txBytes, err := BuildAndSignTransaction(context.Background(), txParams, sequence, encodingConfig)
	if err != nil {
		logger.Error("Failed to build and sign transaction", "error", err)
		return nil, "", err
	}

	// Prepare the REST API request
	txBase64 := base64.StdEncoding.EncodeToString(txBytes)
	reqBody := map[string]interface{}{
		"tx_bytes": txBase64,
		"mode":     "BROADCAST_MODE_SYNC",
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		logger.Error("Failed to marshal request body", "error", err)
		return nil, "", err
	}

	// Send the request
	apiURL := fmt.Sprintf("%s/cosmos/tx/v1beta1/txs", txParams.Config.Nodes.API)
	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		logger.Error("Failed to create request", "error", err)
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Error("Failed to send request", "error", err)
		return nil, "", err
	}
	defer resp.Body.Close()

	// Parse the response
	var respBody tx.BroadcastTxResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		logger.Error("Failed to decode response", "error", err)
		return nil, "", err
	}

	return respBody.TxResponse, string(txBytes), nil
}
