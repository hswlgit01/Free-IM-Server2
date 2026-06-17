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

package convert

import (
	"context"
	"fmt"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/model"
	"github.com/openimsdk/open-im-server/v3/protocol/relation"

	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/utils/datautil"
	"github.com/openimsdk/tools/utils/timeutil"
)

func FriendPb2DB(friend *sdkws.FriendInfo) *model.Friend {
	dbFriend := &model.Friend{}
	err := datautil.CopyStructFields(dbFriend, friend)
	if err != nil {
		return nil
	}
	dbFriend.FriendUserID = friend.FriendUser.UserID
	dbFriend.CreateTime = timeutil.UnixSecondToTime(friend.CreateTime)
	return dbFriend
}

func FriendDB2Pb(ctx context.Context, friendDB *model.Friend, getUsers func(ctx context.Context, userIDs []string) (map[string]*sdkws.UserInfo, error)) (*sdkws.FriendInfo, error) {
	users, err := getUsers(ctx, []string{friendDB.FriendUserID})
	if err != nil {
		return nil, err
	}
	user, ok := users[friendDB.FriendUserID]
	// dawn 2026-06-17 修复单个好友资料转换崩溃：历史好友关系可能指向已缺失用户资料。
	if !ok || user == nil {
		return nil, fmt.Errorf("user not found: %s", friendDB.FriendUserID)
	}

	friendPb := &sdkws.FriendInfo{FriendUser: &sdkws.UserInfo{}}
	err = datautil.CopyStructFields(friendPb, friendDB)
	if err != nil {
		return nil, err
	}

	friendPb.FriendUser.UserID = user.UserID
	friendPb.FriendUser.Nickname = user.Nickname
	friendPb.FriendUser.FaceURL = user.FaceURL
	friendPb.FriendUser.Ex = user.Ex
	friendPb.CreateTime = friendDB.CreateTime.Unix()
	friendPb.IsPinned = friendDB.IsPinned

	return friendPb, nil
}

func FriendsDB2Pb(ctx context.Context, friendsDB []*model.Friend, getUsers func(ctx context.Context, userIDs []string) (map[string]*sdkws.UserInfo, error)) (friendsPb []*sdkws.FriendInfo, err error) {
	if len(friendsDB) == 0 {
		return nil, nil
	}
	var userID []string
	for _, friendDB := range friendsDB {
		userID = append(userID, friendDB.FriendUserID)
	}

	users, err := getUsers(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, friend := range friendsDB {
		user, ok := users[friend.FriendUserID]
		// dawn 2026-06-17 修复好友列表崩溃：历史好友关系可能指向已缺失用户资料，跳过脏关系避免空指针导致整页失败。
		if !ok || user == nil {
			continue
		}
		friendPb := &sdkws.FriendInfo{FriendUser: &sdkws.UserInfo{}}
		err := datautil.CopyStructFields(friendPb, friend)
		if err != nil {
			return nil, err
		}

		friendPb.FriendUser.UserID = user.UserID
		friendPb.FriendUser.Nickname = user.Nickname
		friendPb.FriendUser.FaceURL = user.FaceURL
		friendPb.FriendUser.Ex = user.Ex
		friendPb.CreateTime = friend.CreateTime.Unix()
		friendPb.IsPinned = friend.IsPinned
		friendsPb = append(friendsPb, friendPb)
	}
	return friendsPb, nil
}

func FriendOnlyDB2PbOnly(friendsDB []*model.Friend) []*relation.FriendInfoOnly {
	return datautil.Slice(friendsDB, func(f *model.Friend) *relation.FriendInfoOnly {
		return &relation.FriendInfoOnly{
			OwnerUserID:    f.OwnerUserID,
			FriendUserID:   f.FriendUserID,
			Remark:         f.Remark,
			CreateTime:     f.CreateTime.UnixMilli(),
			AddSource:      f.AddSource,
			OperatorUserID: f.OperatorUserID,
			Ex:             f.Ex,
			IsPinned:       f.IsPinned,
		}
	})
}

func FriendRequestDB2Pb(ctx context.Context, friendRequests []*model.FriendRequest, getUsers func(ctx context.Context, userIDs []string) (map[string]*sdkws.UserInfo, error)) ([]*sdkws.FriendRequest, error) {
	if len(friendRequests) == 0 {
		return nil, nil
	}
	userIDMap := make(map[string]struct{})
	for _, friendRequest := range friendRequests {
		userIDMap[friendRequest.ToUserID] = struct{}{}
		userIDMap[friendRequest.FromUserID] = struct{}{}
	}
	users, err := getUsers(ctx, datautil.Keys(userIDMap))
	if err != nil {
		return nil, err
	}
	res := make([]*sdkws.FriendRequest, 0, len(friendRequests))
	for _, friendRequest := range friendRequests {
		toUser := users[friendRequest.ToUserID]
		fromUser := users[friendRequest.FromUserID]
		// dawn 2026-06-17 修复好友申请列表崩溃：历史申请引用的用户资料缺失时跳过脏记录。
		if toUser == nil || fromUser == nil {
			continue
		}
		res = append(res, &sdkws.FriendRequest{
			FromUserID:    friendRequest.FromUserID,
			FromNickname:  fromUser.Nickname,
			FromFaceURL:   fromUser.FaceURL,
			ToUserID:      friendRequest.ToUserID,
			ToNickname:    toUser.Nickname,
			ToFaceURL:     toUser.FaceURL,
			HandleResult:  friendRequest.HandleResult,
			ReqMsg:        friendRequest.ReqMsg,
			CreateTime:    friendRequest.CreateTime.UnixMilli(),
			HandlerUserID: friendRequest.HandlerUserID,
			HandleMsg:     friendRequest.HandleMsg,
			HandleTime:    friendRequest.HandleTime.UnixMilli(),
			Ex:            friendRequest.Ex,
		})
	}
	return res, nil
}

// FriendPb2DBMap converts a FriendInfo protobuf object to a map suitable for database operations.
// It only includes non-zero or non-empty fields in the map.
func FriendPb2DBMap(friend *sdkws.FriendInfo) map[string]any {
	if friend == nil {
		return nil
	}

	val := make(map[string]any)

	// Assuming FriendInfo has similar fields to those in Friend.
	// Add or remove fields based on your actual FriendInfo and Friend structures.
	if friend.FriendUser != nil {
		if friend.FriendUser.UserID != "" {
			val["friend_user_id"] = friend.FriendUser.UserID
		}
		if friend.FriendUser.Nickname != "" {
			val["nickname"] = friend.FriendUser.Nickname
		}
		if friend.FriendUser.FaceURL != "" {
			val["face_url"] = friend.FriendUser.FaceURL
		}
		if friend.FriendUser.Ex != "" {
			val["ex"] = friend.FriendUser.Ex
		}
	}
	if friend.CreateTime != 0 {
		val["create_time"] = friend.CreateTime // You might need to convert this to a proper time format.
	}

	// Include other fields from FriendInfo as needed, similar to the above pattern.

	return val
}
