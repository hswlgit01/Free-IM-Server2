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

package controller

import (
	"context"
	"encoding/json"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/database"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/model"
	"github.com/openimsdk/open-im-server/v3/tools/db/pagination"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// 操作详情结构体定义
type MuteOperationDetails struct {
	MuteDuration int64     `json:"mute_duration"` // 禁言时长(秒)
	MuteEndTime  time.Time `json:"mute_end_time"` // 禁言结束时间
}

type KickMemberDetails struct {
	Reason string `json:"reason"` // 踢人原因
}

type TransferOwnerDetails struct {
	OldOwnerUserID string `json:"old_owner_user_id"` // 原群主ID
	NewOwnerUserID string `json:"new_owner_user_id"` // 新群主ID
}

type CreateGroupDetails struct {
	GroupName   string   `json:"group_name"`   // 群组名称
	GroupType   int32    `json:"group_type"`   // 群组类型
	MemberCount int32    `json:"member_count"` // 初始成员数量
	InitMembers []string `json:"init_members"` // 初始成员ID列表
}

type MuteGroupDetails struct {
	MuteDuration int64     `json:"mute_duration"` // 群禁言时长(秒，0表示永久)
	MuteEndTime  time.Time `json:"mute_end_time"` // 群禁言结束时间
}

// 群操作日志控制器接口
type GroupOperationLogController interface {
	// 基础操作
	CreateGroupOperationLog(ctx context.Context, log *model.GroupOperationLog) error
	CreateGroupOperationLogBatch(ctx context.Context, logs []*model.GroupOperationLog) error

	// 记录具体操作方法
	RecordCreateGroupOperation(ctx context.Context, groupID, operatorUserID string, details *CreateGroupDetails) error
	RecordKickMemberOperation(ctx context.Context, groupID, operatorUserID, targetUserID string, reason string) error
	RecordDismissGroupOperation(ctx context.Context, groupID, operatorUserID string) error
	RecordTransferOwnerOperation(ctx context.Context, groupID, operatorUserID, newOwnerUserID, oldOwnerUserID string) error
	RecordMuteMemberOperation(ctx context.Context, groupID, operatorUserID, targetUserID string, muteDuration int64, muteEndTime time.Time) error
	RecordCancelMuteMemberOperation(ctx context.Context, groupID, operatorUserID, targetUserID string) error
	RecordMuteGroupOperation(ctx context.Context, groupID, operatorUserID string, muteDuration int64, muteEndTime time.Time) error
	RecordCancelMuteGroupOperation(ctx context.Context, groupID, operatorUserID string) error

	// 查询接口
	FindByGroupID(ctx context.Context, groupID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	FindByOperator(ctx context.Context, operatorUserID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	FindByTargetUser(ctx context.Context, targetUserID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	FindByOperationType(ctx context.Context, groupID string, operationType int32, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	FindByTimeRange(ctx context.Context, groupID string, startTime, endTime time.Time, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	FindByConditions(ctx context.Context, groupID string, operatorUserID string, targetUserID string, operationType int32, startTime, endTime time.Time, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
}

// 群操作日志控制器实现
type groupOperationLogController struct {
	database database.GroupOperationLog
}

func NewGroupOperationLogController(database database.GroupOperationLog) GroupOperationLogController {
	return &groupOperationLogController{
		database: database,
	}
}

func (g *groupOperationLogController) CreateGroupOperationLog(ctx context.Context, log *model.GroupOperationLog) error {
	if log.ID == "" {
		log.ID = primitive.NewObjectID().Hex()
	}
	if log.OperationTime.IsZero() {
		log.OperationTime = time.Now()
	}
	return g.database.Create(ctx, log)
}

func (g *groupOperationLogController) CreateGroupOperationLogBatch(ctx context.Context, logs []*model.GroupOperationLog) error {
	now := time.Now()
	for _, log := range logs {
		if log.ID == "" {
			log.ID = primitive.NewObjectID().Hex()
		}
		if log.OperationTime.IsZero() {
			log.OperationTime = now
		}
	}
	return g.database.CreateBatch(ctx, logs)
}

func (g *groupOperationLogController) RecordCreateGroupOperation(ctx context.Context, groupID, operatorUserID string, details *CreateGroupDetails) error {
	detailsBytes, _ := json.Marshal(details)

	log := &model.GroupOperationLog{
		ID:             primitive.NewObjectID().Hex(),
		GroupID:        groupID,
		OperatorUserID: operatorUserID,
		TargetUserID:   "", // 创建群组没有目标用户
		OperationType:  model.GroupOpTypeCreateGroup,
		OperationTime:  time.Now(),
		Details:        string(detailsBytes),
	}

	return g.CreateGroupOperationLog(ctx, log)
}

func (g *groupOperationLogController) RecordKickMemberOperation(ctx context.Context, groupID, operatorUserID, targetUserID string, reason string) error {
	details := &KickMemberDetails{
		Reason: reason,
	}
	detailsBytes, _ := json.Marshal(details)

	log := &model.GroupOperationLog{
		ID:             primitive.NewObjectID().Hex(),
		GroupID:        groupID,
		OperatorUserID: operatorUserID,
		TargetUserID:   targetUserID,
		OperationType:  model.GroupOpTypeKickMember,
		OperationTime:  time.Now(),
		Details:        string(detailsBytes),
	}

	return g.CreateGroupOperationLog(ctx, log)
}

func (g *groupOperationLogController) RecordDismissGroupOperation(ctx context.Context, groupID, operatorUserID string) error {
	log := &model.GroupOperationLog{
		ID:             primitive.NewObjectID().Hex(),
		GroupID:        groupID,
		OperatorUserID: operatorUserID,
		TargetUserID:   "", // 解散群组没有目标用户
		OperationType:  model.GroupOpTypeDismissGroup,
		OperationTime:  time.Now(),
		Details:        "",
	}

	return g.CreateGroupOperationLog(ctx, log)
}

func (g *groupOperationLogController) RecordTransferOwnerOperation(ctx context.Context, groupID, operatorUserID, newOwnerUserID, oldOwnerUserID string) error {
	details := &TransferOwnerDetails{
		OldOwnerUserID: oldOwnerUserID,
		NewOwnerUserID: newOwnerUserID,
	}
	detailsBytes, _ := json.Marshal(details)

	log := &model.GroupOperationLog{
		ID:             primitive.NewObjectID().Hex(),
		GroupID:        groupID,
		OperatorUserID: operatorUserID,
		TargetUserID:   newOwnerUserID,
		OperationType:  model.GroupOpTypeTransferOwner,
		OperationTime:  time.Now(),
		Details:        string(detailsBytes),
	}

	return g.CreateGroupOperationLog(ctx, log)
}

func (g *groupOperationLogController) RecordMuteMemberOperation(ctx context.Context, groupID, operatorUserID, targetUserID string, muteDuration int64, muteEndTime time.Time) error {
	details := &MuteOperationDetails{
		MuteDuration: muteDuration,
		MuteEndTime:  muteEndTime,
	}
	detailsBytes, _ := json.Marshal(details)

	log := &model.GroupOperationLog{
		ID:             primitive.NewObjectID().Hex(),
		GroupID:        groupID,
		OperatorUserID: operatorUserID,
		TargetUserID:   targetUserID,
		OperationType:  model.GroupOpTypeMuteMember,
		OperationTime:  time.Now(),
		Details:        string(detailsBytes),
	}

	return g.CreateGroupOperationLog(ctx, log)
}

func (g *groupOperationLogController) RecordCancelMuteMemberOperation(ctx context.Context, groupID, operatorUserID, targetUserID string) error {
	log := &model.GroupOperationLog{
		ID:             primitive.NewObjectID().Hex(),
		GroupID:        groupID,
		OperatorUserID: operatorUserID,
		TargetUserID:   targetUserID,
		OperationType:  model.GroupOpTypeCancelMuteMember,
		OperationTime:  time.Now(),
		Details:        "",
	}

	return g.CreateGroupOperationLog(ctx, log)
}

func (g *groupOperationLogController) RecordMuteGroupOperation(ctx context.Context, groupID, operatorUserID string, muteDuration int64, muteEndTime time.Time) error {
	details := &MuteGroupDetails{
		MuteDuration: muteDuration,
		MuteEndTime:  muteEndTime,
	}
	detailsBytes, _ := json.Marshal(details)

	log := &model.GroupOperationLog{
		ID:             primitive.NewObjectID().Hex(),
		GroupID:        groupID,
		OperatorUserID: operatorUserID,
		TargetUserID:   "", // 群禁言没有目标用户
		OperationType:  model.GroupOpTypeMuteGroup,
		OperationTime:  time.Now(),
		Details:        string(detailsBytes),
	}

	return g.CreateGroupOperationLog(ctx, log)
}

func (g *groupOperationLogController) RecordCancelMuteGroupOperation(ctx context.Context, groupID, operatorUserID string) error {
	log := &model.GroupOperationLog{
		ID:             primitive.NewObjectID().Hex(),
		GroupID:        groupID,
		OperatorUserID: operatorUserID,
		TargetUserID:   "", // 取消群禁言没有目标用户
		OperationType:  model.GroupOpTypeCancelMuteGroup,
		OperationTime:  time.Now(),
		Details:        "",
	}

	return g.CreateGroupOperationLog(ctx, log)
}

func (g *groupOperationLogController) FindByGroupID(ctx context.Context, groupID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	return g.database.FindByGroupID(ctx, groupID, pagination)
}

func (g *groupOperationLogController) FindByOperator(ctx context.Context, operatorUserID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	return g.database.FindByOperator(ctx, operatorUserID, pagination)
}

func (g *groupOperationLogController) FindByTargetUser(ctx context.Context, targetUserID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	return g.database.FindByTargetUser(ctx, targetUserID, pagination)
}

func (g *groupOperationLogController) FindByOperationType(ctx context.Context, groupID string, operationType int32, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	return g.database.FindByOperationType(ctx, groupID, operationType, pagination)
}

func (g *groupOperationLogController) FindByTimeRange(ctx context.Context, groupID string, startTime, endTime time.Time, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	return g.database.FindByTimeRange(ctx, groupID, startTime, endTime, pagination)
}

func (g *groupOperationLogController) FindByConditions(ctx context.Context, groupID string, operatorUserID string, targetUserID string, operationType int32, startTime, endTime time.Time, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	return g.database.FindByConditions(ctx, groupID, operatorUserID, targetUserID, operationType, startTime, endTime, pagination)
}
