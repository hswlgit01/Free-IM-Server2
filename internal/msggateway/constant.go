// Copyright © 2023 OpenIM. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package msggateway

import "time"

const (
	WsUserID                = "sendID"
	CommonUserID            = "userID"
	PlatformID              = "platformID"
	ConnID                  = "connID"
	Token                   = "token"
	OperationID             = "operationID"
	Compression             = "compression"
	GzipCompressionProtocol = "gzip"
	BackgroundStatus        = "isBackground"
	SendResponse            = "isMsgResp"
	SDKType                 = "sdkType"
)

const (
	GoSDK = "go"
	JsSDK = "js"
)

const (
	WebSocket = iota + 1
)

const (
	// Websocket Protocol.
	WSGetNewestSeq        = 1001
	WSPullMsgBySeqList    = 1002
	WSSendMsg             = 1003
	WSSendSignalMsg       = 1004
	WSPullMsg             = 1005
	WSGetConvMaxReadSeq   = 1006
	WsPullConvLastMessage = 1007
	WSPushMsg             = 2001
	WSKickOnlineMsg       = 2002
	WsLogoutMsg           = 2003
	WsSetBackgroundStatus = 2004
	WsSubUserOnlineStatus = 2005
	WSDataError           = 3001
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 60 * time.Second // 增加到60秒，允许更长的写入时间

	// Time allowed to read the next pong message from the peer.
	pongWait = 120 * time.Second // 增加到120秒，提供更宽松的心跳超时

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = 30 * time.Second // 固定为30秒，更频繁地发送心跳

	// Maximum message size allowed from peer.
	maxMessageSize = 16777216 // 16MB (增加到16MB以支持超大消息)
)
