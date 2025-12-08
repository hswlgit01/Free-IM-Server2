package rpcli

import (
	"github.com/openimsdk/open-im-server/v3/protocol/msggateway"
	"google.golang.org/grpc"
)

func NewMsgGatewayClient(cc grpc.ClientConnInterface) *MsgGatewayClient {
	return &MsgGatewayClient{msggateway.NewMsgGatewayClient(cc)}
}

type MsgGatewayClient struct {
	msggateway.MsgGatewayClient
}
