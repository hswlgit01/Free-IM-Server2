package rpcli

import (
	"github.com/openimsdk/open-im-server/v3/protocol/rtc"
	"google.golang.org/grpc"
)

func NewRtcServiceClient(cc grpc.ClientConnInterface) *RtcServiceClient {
	return &RtcServiceClient{rtc.NewRtcServiceClient(cc)}
}

type RtcServiceClient struct {
	rtc.RtcServiceClient
}
