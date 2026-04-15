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

package api

import (
	"fmt"
	"sort"
	"sync"
	"time"

	pbgroup "github.com/openimsdk/open-im-server/v3/protocol/group"
	"google.golang.org/protobuf/proto"
)

const (
	// batchIncrMemberCacheTTL 同一 userID + 同一批 group 请求的短时缓存时间，降低重复 RPC 频率
	batchIncrMemberCacheTTL = 2 * time.Second
)

type batchIncrMemberEntry struct {
	resp   *pbgroup.BatchGetIncrementalGroupMemberResp
	expire time.Time
}

// batchIncrMemberCache 为 BatchGetIncrementalGroupMember 提供按 key 的短时缓存，并发安全
type batchIncrMemberCache struct {
	mu      sync.RWMutex
	entries map[string]*batchIncrMemberEntry
}

var globalBatchIncrMemberCache = &batchIncrMemberCache{
	entries: make(map[string]*batchIncrMemberEntry),
}

// buildBatchIncrMemberCacheKey 根据请求生成稳定缓存 key：userID + 按 groupID 排序的 (groupID|versionID|version)
func buildBatchIncrMemberCacheKey(req *pbgroup.BatchGetIncrementalGroupMemberReq) string {
	if req == nil || len(req.ReqList) == 0 {
		return ""
	}
	parts := make([]string, 0, len(req.ReqList))
	for _, r := range req.ReqList {
		if r == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s|%s|%d", r.GroupID, r.VersionID, r.Version))
	}
	sort.Strings(parts)
	return req.UserID + ":" + fmt.Sprintf("%v", parts)
}

// getOrLoad 若缓存未过期则返回缓存（克隆后返回）；否则调用 load 并写入缓存
func (c *batchIncrMemberCache) getOrLoad(key string, load func() (*pbgroup.BatchGetIncrementalGroupMemberResp, error)) (*pbgroup.BatchGetIncrementalGroupMemberResp, error) {
	now := time.Now()
	c.mu.RLock()
	ent, ok := c.entries[key]
	if ok && now.Before(ent.expire) {
		// 返回克隆，避免调用方修改缓存内容
		clone := proto.Clone(ent.resp).(*pbgroup.BatchGetIncrementalGroupMemberResp)
		c.mu.RUnlock()
		return clone, nil
	}
	c.mu.RUnlock()

	resp, err := load()
	if err != nil {
		return nil, err
	}
	stored := proto.Clone(resp).(*pbgroup.BatchGetIncrementalGroupMemberResp)
	c.mu.Lock()
	// 惰性清理过期项，避免 map 无限增长
	for k, e := range c.entries {
		if time.Now().After(e.expire) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = &batchIncrMemberEntry{resp: stored, expire: now.Add(batchIncrMemberCacheTTL)}
	c.mu.Unlock()
	return proto.Clone(resp).(*pbgroup.BatchGetIncrementalGroupMemberResp), nil
}
