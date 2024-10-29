package broadcast

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/ibc-go/modules/apps/callbacks/testing/simapp/params"
	"github.com/somatic-labs/meteorite/types"
)

func SendTransactionViaAPI(txParams types.TransactionParams, sequence uint64) (*sdk.TxResponse, string, error) {
	encodingConfig := params.MakeTestEncodingConfig()
	encodingConfig.Codec = cdc

	// Build and sign the transaction
	txBytes, err := BuildAndSignTransaction(context.Background(), txParams, sequence, encodingConfig)
	if err != nil {
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
		return nil, "", err
	}

	// Send the request
	apiURL := fmt.Sprintf("%s/cosmos/tx/v1beta1/txs", txParams.Config.Nodes.API)
	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	// Parse the response
	var respBody tx.BroadcastTxResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return nil, "", err
	}

	return respBody.TxResponse, string(txBytes), nil
}
