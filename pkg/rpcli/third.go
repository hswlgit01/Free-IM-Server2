package rpcli

import (
	"github.com/openimsdk/open-im-server/v3/protocol/third"
	"google.golang.org/grpc"
)

func NewThirdClient(cc grpc.ClientConnInterface) *ThirdClient {
	return &ThirdClient{third.NewThirdClient(cc)}
}

type ThirdClient struct {
	third.ThirdClient
}
