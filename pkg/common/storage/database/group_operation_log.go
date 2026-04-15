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

package database

import (
	"context"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/model"
	"github.com/openimsdk/open-im-server/v3/tools/db/pagination"
)

// GroupOperationLog 群操作日志接口
type GroupOperationLog interface {
	// 创建操作日志
	Create(ctx context.Context, log *model.GroupOperationLog) error
	// 批量创建操作日志
	CreateBatch(ctx context.Context, logs []*model.GroupOperationLog) error
	// 根据群组ID查询操作日志
	FindByGroupID(ctx context.Context, groupID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	// 根据操作者查询操作日志
	FindByOperator(ctx context.Context, operatorUserID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	// 根据目标用户查询操作日志
	FindByTargetUser(ctx context.Context, targetUserID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	// 根据操作类型查询操作日志
	FindByOperationType(ctx context.Context, groupID string, operationType int32, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	// 根据时间范围查询操作日志
	FindByTimeRange(ctx context.Context, groupID string, startTime, endTime time.Time, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
	// 综合条件查询操作日志
	FindByConditions(ctx context.Context, groupID string, operatorUserID string, targetUserID string, operationType int32, startTime, endTime time.Time, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error)
}
