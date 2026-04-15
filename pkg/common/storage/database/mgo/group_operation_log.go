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

package mgo

import (
	"context"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/database"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/model"
	"github.com/openimsdk/open-im-server/v3/tools/db/mongoutil"
	"github.com/openimsdk/open-im-server/v3/tools/db/pagination"
	"github.com/openimsdk/tools/errs"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GroupOperationLogMgo 群操作日志MongoDB实现
type GroupOperationLogMgo struct {
	coll *mongo.Collection
}

func NewGroupOperationLogMongo(db *mongo.Database) (database.GroupOperationLog, error) {
	coll := db.Collection(database.GroupOperationLogName)
	if err := createGroupOperationLogIndexes(coll); err != nil {
		return nil, err
	}
	return &GroupOperationLogMgo{coll: coll}, nil
}

func (g *GroupOperationLogMgo) Create(ctx context.Context, log *model.GroupOperationLog) error {
	return mongoutil.InsertMany(ctx, g.coll, []*model.GroupOperationLog{log})
}

func (g *GroupOperationLogMgo) CreateBatch(ctx context.Context, logs []*model.GroupOperationLog) error {
	return mongoutil.InsertMany(ctx, g.coll, logs)
}

func (g *GroupOperationLogMgo) FindByGroupID(ctx context.Context, groupID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	filter := bson.M{"group_id": groupID}
	opts := options.Find().SetSort(bson.D{{Key: "operation_time", Value: -1}})
	return mongoutil.FindPage[*model.GroupOperationLog](ctx, g.coll, filter, pagination, opts)
}

func (g *GroupOperationLogMgo) FindByOperator(ctx context.Context, operatorUserID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	filter := bson.M{"operator_user_id": operatorUserID}
	opts := options.Find().SetSort(bson.D{{Key: "operation_time", Value: -1}})
	return mongoutil.FindPage[*model.GroupOperationLog](ctx, g.coll, filter, pagination, opts)
}

func (g *GroupOperationLogMgo) FindByTargetUser(ctx context.Context, targetUserID string, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	filter := bson.M{"target_user_id": targetUserID}
	opts := options.Find().SetSort(bson.D{{Key: "operation_time", Value: -1}})
	return mongoutil.FindPage[*model.GroupOperationLog](ctx, g.coll, filter, pagination, opts)
}

func (g *GroupOperationLogMgo) FindByOperationType(ctx context.Context, groupID string, operationType int32, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	filter := bson.M{
		"group_id":       groupID,
		"operation_type": operationType,
	}
	opts := options.Find().SetSort(bson.D{{Key: "operation_time", Value: -1}})
	return mongoutil.FindPage[*model.GroupOperationLog](ctx, g.coll, filter, pagination, opts)
}

func (g *GroupOperationLogMgo) FindByTimeRange(ctx context.Context, groupID string, startTime, endTime time.Time, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	filter := bson.M{
		"group_id": groupID,
		"operation_time": bson.M{
			"$gte": startTime,
			"$lte": endTime,
		},
	}
	opts := options.Find().SetSort(bson.D{{Key: "operation_time", Value: -1}})
	return mongoutil.FindPage[*model.GroupOperationLog](ctx, g.coll, filter, pagination, opts)
}

func (g *GroupOperationLogMgo) FindByConditions(ctx context.Context, groupID string, operatorUserID string, targetUserID string, operationType int32, startTime, endTime time.Time, pagination pagination.Pagination) (total int64, logs []*model.GroupOperationLog, err error) {
	filter := buildConditionFilter(groupID, operatorUserID, targetUserID, operationType, startTime, endTime)
	opts := options.Find().SetSort(bson.D{{Key: "operation_time", Value: -1}})
	return mongoutil.FindPage[*model.GroupOperationLog](ctx, g.coll, filter, pagination, opts)
}

// 创建索引
func createGroupOperationLogIndexes(coll *mongo.Collection) error {
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "group_id", Value: 1},
				{Key: "operation_time", Value: -1},
			},
		},
		{
			Keys: bson.D{
				{Key: "operator_user_id", Value: 1},
				{Key: "operation_time", Value: -1},
			},
		},
		{
			Keys: bson.D{
				{Key: "target_user_id", Value: 1},
				{Key: "operation_time", Value: -1},
			},
		},
		{
			Keys: bson.D{
				{Key: "operation_type", Value: 1},
				{Key: "operation_time", Value: -1},
			},
		},
	}

	_, err := coll.Indexes().CreateMany(context.Background(), indexes)
	return errs.Wrap(err)
}

// 构建条件过滤器
func buildConditionFilter(groupID string, operatorUserID string, targetUserID string, operationType int32, startTime, endTime time.Time) bson.M {
	filter := bson.M{}

	if groupID != "" {
		filter["group_id"] = groupID
	}
	if operatorUserID != "" {
		filter["operator_user_id"] = operatorUserID
	}
	if targetUserID != "" {
		filter["target_user_id"] = targetUserID
	}
	if operationType > 0 {
		filter["operation_type"] = operationType
	}
	if !startTime.IsZero() || !endTime.IsZero() {
		timeFilter := bson.M{}
		if !startTime.IsZero() {
			timeFilter["$gte"] = startTime
		}
		if !endTime.IsZero() {
			timeFilter["$lte"] = endTime
		}
		filter["operation_time"] = timeFilter
	}

	return filter
}
