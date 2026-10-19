package spot

import (
	"github.com/QuantProcessing/keeluni/sdk/hyperliquid"
)

type WebsocketClient struct {
	*hyperliquid.WebsocketClient
}

func NewWebsocketClient(base *hyperliquid.WebsocketClient) *WebsocketClient {
	return &WebsocketClient{WebsocketClient: base}
}
