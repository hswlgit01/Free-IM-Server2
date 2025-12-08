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

package model

import (
	"time"
)

// GroupOperationLog 群操作日志表
type GroupOperationLog struct {
	ID             string    `bson:"_id"`              // 日志ID
	GroupID        string    `bson:"group_id"`         // 群组ID
	OperatorUserID string    `bson:"operator_user_id"` // 操作者用户ID
	TargetUserID   string    `bson:"target_user_id"`   // 被操作者用户ID (可为空，如群禁言操作)
	OperationType  int32     `bson:"operation_type"`   // 操作类型 (见常量定义)
	OperationTime  time.Time `bson:"operation_time"`   // 操作时间
	Details        string    `bson:"details"`          // 操作详情 (JSON格式存储具体参数)
	Ex             string    `bson:"ex"`               // 扩展字段
}

// 群操作类型常量
const (
	GroupOpTypeCreateGroup      = 1001 // 创建群组
	GroupOpTypeKickMember       = 1002 // 踢出群成员
	GroupOpTypeDismissGroup     = 1003 // 解散群组
	GroupOpTypeTransferOwner    = 1004 // 转移群主
	GroupOpTypeMuteMember       = 1005 // 禁言群成员
	GroupOpTypeCancelMuteMember = 1006 // 取消禁言群成员
	GroupOpTypeMuteGroup        = 1007 // 禁言整个群
	GroupOpTypeCancelMuteGroup  = 1008 // 取消群禁言
)
