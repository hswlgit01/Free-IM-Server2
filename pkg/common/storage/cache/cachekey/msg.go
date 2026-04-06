// Copyright © 2024 OpenIM. All rights reserved.
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

package cachekey

import (
	"strconv"
)

const (
	sendMsgFailedFlag = "SEND_MSG_FAILED_FLAG:"
	messageCache      = "MSG_CACHE:"
	messageIdempotent = "MSG_IDEMPOTENT:" // 新增：消息幂等性键前缀
)

func GetMsgCacheKey(conversationID string, seq int64) string {
	return messageCache + conversationID + ":" + strconv.Itoa(int(seq))
}

func GetSendMsgKey(id string) string {
	return sendMsgFailedFlag + id
}

// 新增：获取消息幂等性检查的键
func GetMsgIdempotentKey(clientMsgID string) string {
	return messageIdempotent + clientMsgID
}
