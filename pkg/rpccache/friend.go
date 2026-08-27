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

package rpccache

import (
	"context"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/cache/cachekey"
	"github.com/openimsdk/open-im-server/v3/pkg/rpcli"
	"github.com/openimsdk/open-im-server/v3/protocol/relation"

	"github.com/openimsdk/open-im-server/v3/pkg/common/config"
	"github.com/openimsdk/open-im-server/v3/pkg/localcache"
	"github.com/openimsdk/tools/log"
	"github.com/redis/go-redis/v9"
)

func NewFriendLocalCache(client *rpcli.RelationClient, localCache *config.LocalCache, cli redis.UniversalClient) *FriendLocalCache {
	lc := localCache.Friend
	log.ZDebug(context.Background(), "FriendLocalCache", "topic", lc.Topic, "slotNum", lc.SlotNum, "slotSize", lc.SlotSize, "enable", lc.Enable())
	x := &FriendLocalCache{
		client: client,
		local: localcache.New[[]byte](
			localcache.WithLocalSlotNum(lc.SlotNum),
			localcache.WithLocalSlotSize(lc.SlotSize),
			localcache.WithLinkSlotNum(lc.SlotNum),
			localcache.WithLocalSuccessTTL(lc.Success()),
			localcache.WithLocalFailedTTL(lc.Failed()),
		),
	}
	if lc.Enable() {
		go subscriberRedisDeleteCache(context.Background(), cli, lc.Topic, x.local.DelLocal)
	}
	return x
}

type FriendLocalCache struct {
	client *rpcli.RelationClient
	local  localcache.Cache[[]byte]
}

func (f *FriendLocalCache) IsFriend(ctx context.Context, possibleFriendUserID, userID string) (val bool, err error) {
	res, err := f.isFriend(ctx, possibleFriendUserID, userID)
	if err != nil {
		return false, err
	}
	return res.InUser1Friends, nil
}

// IsMutualFriend 判断两人是否**互为**好友。
//
// IsFriend 只看单边：「possibleFriendUserID 是否在 userID 的好友列表里」。
// 但 DeleteFriend 只删发起方一侧的记录（FriendMgo.Delete 按 owner_user_id 过滤），
// 于是「谁删的好友，谁反而还能继续发消息」：
//
//	A 加了 B，B 也加了 A          -> 两条记录
//	B 删除 A                     -> 只删掉 (owner=B, friend=A)
//	B 发给 A：查「B 在 A 列表里吗」-> A 的列表还留着 B -> 放行  ← 客户所报 BUG
//	A 发给 B：查「A 在 B 列表里吗」-> B 已删掉 A       -> 拦截
//
// 要让「解除好友」对双方都生效，就得两个方向都成立才放行。
func (f *FriendLocalCache) IsMutualFriend(ctx context.Context, possibleFriendUserID, userID string) (val bool, err error) {
	res, err := f.isFriend(ctx, possibleFriendUserID, userID)
	if err != nil {
		return false, err
	}
	return res.InUser1Friends && res.InUser2Friends, nil
}

func (f *FriendLocalCache) isFriend(ctx context.Context, possibleFriendUserID, userID string) (val *relation.IsFriendResp, err error) {
	log.ZDebug(ctx, "FriendLocalCache isFriend req", "possibleFriendUserID", possibleFriendUserID, "userID", userID)
	defer func() {
		if err == nil {
			log.ZDebug(ctx, "FriendLocalCache isFriend return", "possibleFriendUserID", possibleFriendUserID, "userID", userID, "value", val)
		} else {
			log.ZError(ctx, "FriendLocalCache isFriend return", err, "possibleFriendUserID", possibleFriendUserID, "userID", userID)
		}
	}()
	var cache cacheProto[relation.IsFriendResp]
	return cache.Unmarshal(f.local.GetLink(ctx, cachekey.GetIsFriendKey(possibleFriendUserID, userID), func(ctx context.Context) ([]byte, error) {
		log.ZDebug(ctx, "FriendLocalCache isFriend rpc", "possibleFriendUserID", possibleFriendUserID, "userID", userID)
		return cache.Marshal(f.client.FriendClient.IsFriend(ctx, &relation.IsFriendReq{UserID1: userID, UserID2: possibleFriendUserID}))
		// 缓存的是 IsFriendResp，两个方向的标志都在里面，取值同时依赖
		// **双方**的好友列表。原先只挂了 possibleFriendUserID 一侧，
		// 导致 userID 那一侧解除好友时本地缓存不失效，要等 TTL 到期才生效
		// —— 表现就是「刚解除还能再发几句」。两个 key 都挂上。
	}, cachekey.GetFriendIDsKey(possibleFriendUserID), cachekey.GetFriendIDsKey(userID)))
}

// IsBlack possibleBlackUserID selfUserID.
func (f *FriendLocalCache) IsBlack(ctx context.Context, possibleBlackUserID, userID string) (val bool, err error) {
	res, err := f.isBlack(ctx, possibleBlackUserID, userID)
	if err != nil {
		return false, err
	}
	return res.InUser2Blacks, nil
}

// IsBlack possibleBlackUserID selfUserID.
func (f *FriendLocalCache) isBlack(ctx context.Context, possibleBlackUserID, userID string) (val *relation.IsBlackResp, err error) {
	log.ZDebug(ctx, "FriendLocalCache isBlack req", "possibleBlackUserID", possibleBlackUserID, "userID", userID)
	defer func() {
		if err == nil {
			log.ZDebug(ctx, "FriendLocalCache isBlack return", "possibleBlackUserID", possibleBlackUserID, "userID", userID, "value", val)
		} else {
			log.ZError(ctx, "FriendLocalCache isBlack return", err, "possibleBlackUserID", possibleBlackUserID, "userID", userID)
		}
	}()
	var cache cacheProto[relation.IsBlackResp]
	return cache.Unmarshal(f.local.GetLink(ctx, cachekey.GetIsBlackIDsKey(possibleBlackUserID, userID), func(ctx context.Context) ([]byte, error) {
		log.ZDebug(ctx, "FriendLocalCache IsBlack rpc", "possibleBlackUserID", possibleBlackUserID, "userID", userID)
		return cache.Marshal(f.client.FriendClient.IsBlack(ctx, &relation.IsBlackReq{UserID1: possibleBlackUserID, UserID2: userID}))
	}, cachekey.GetBlackIDsKey(userID)))
}
