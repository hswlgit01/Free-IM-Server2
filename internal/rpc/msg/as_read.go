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

package msg

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	cbapi "github.com/openimsdk/open-im-server/v3/pkg/callbackstruct"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msg"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/utils/datautil"
	"github.com/redis/go-redis/v9"
)

// 群组读回执限流和缓存
var (
	// 群组读回执缓存锁
	groupReadMutex sync.RWMutex
	// 群组读回执缓存 - map[groupID]map[userID]hasReadSeq
	groupReadCache = make(map[string]map[string]int64)
	// 上次发送时间 - map[groupID]lastSendTime
	lastBatchTime = make(map[string]time.Time)
)

func (m *msgServer) GetConversationsHasReadAndMaxSeq(ctx context.Context, req *msg.GetConversationsHasReadAndMaxSeqReq) (*msg.GetConversationsHasReadAndMaxSeqResp, error) {
	var conversationIDs []string
	if len(req.ConversationIDs) == 0 {
		var err error
		conversationIDs, err = m.ConversationLocalCache.GetConversationIDs(ctx, req.UserID)
		if err != nil {
			return nil, err
		}
	} else {
		conversationIDs = req.ConversationIDs
	}

	hasReadSeqs, err := m.MsgDatabase.GetHasReadSeqs(ctx, req.UserID, conversationIDs)
	if err != nil {
		return nil, err
	}

	conversations, err := m.ConversationLocalCache.GetConversations(ctx, req.UserID, conversationIDs)
	if err != nil {
		return nil, err
	}

	conversationMaxSeqMap := make(map[string]int64)
	for _, conversation := range conversations {
		if conversation.MaxSeq != 0 {
			conversationMaxSeqMap[conversation.ConversationID] = conversation.MaxSeq
		}
	}
	maxSeqs, err := m.MsgDatabase.GetMaxSeqsWithTime(ctx, conversationIDs)
	if err != nil {
		return nil, err
	}
	resp := &msg.GetConversationsHasReadAndMaxSeqResp{Seqs: make(map[string]*msg.Seqs)}
	for conversationID, maxSeq := range maxSeqs {
		resp.Seqs[conversationID] = &msg.Seqs{
			HasReadSeq: hasReadSeqs[conversationID],
			MaxSeq:     maxSeq.Seq,
			MaxSeqTime: maxSeq.Time,
		}
		if v, ok := conversationMaxSeqMap[conversationID]; ok {
			resp.Seqs[conversationID].MaxSeq = v
		}
	}
	return resp, nil
}

func (m *msgServer) SetConversationHasReadSeq(ctx context.Context, req *msg.SetConversationHasReadSeqReq) (*msg.SetConversationHasReadSeqResp, error) {
	maxSeq, err := m.MsgDatabase.GetMaxSeq(ctx, req.ConversationID)
	if err != nil {
		return nil, err
	}
	if req.HasReadSeq > maxSeq {
		return nil, errs.ErrArgs.WrapMsg("hasReadSeq must not be bigger than maxSeq")
	}
	if err := m.MsgDatabase.SetHasReadSeq(ctx, req.UserID, req.ConversationID, req.HasReadSeq); err != nil {
		return nil, err
	}
	m.sendMarkAsReadNotification(ctx, req.ConversationID, constant.SingleChatType, req.UserID, req.UserID, nil, req.HasReadSeq)
	return &msg.SetConversationHasReadSeqResp{}, nil
}

func (m *msgServer) MarkMsgsAsRead(ctx context.Context, req *msg.MarkMsgsAsReadReq) (*msg.MarkMsgsAsReadResp, error) {
	if len(req.Seqs) < 1 {
		return nil, errs.ErrArgs.WrapMsg("seqs must not be empty")
	}
	maxSeq, err := m.MsgDatabase.GetMaxSeq(ctx, req.ConversationID)
	if err != nil {
		return nil, err
	}
	hasReadSeq := req.Seqs[len(req.Seqs)-1]
	if hasReadSeq > maxSeq {
		return nil, errs.ErrArgs.WrapMsg("hasReadSeq must not be bigger than maxSeq")
	}
	conversation, err := m.ConversationLocalCache.GetConversation(ctx, req.UserID, req.ConversationID)
	if err != nil {
		return nil, err
	}
	if err := m.MsgDatabase.MarkSingleChatMsgsAsRead(ctx, req.UserID, req.ConversationID, req.Seqs); err != nil {
		return nil, err
	}
	currentHasReadSeq, err := m.MsgDatabase.GetHasReadSeq(ctx, req.UserID, req.ConversationID)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	if hasReadSeq > currentHasReadSeq {
		err = m.MsgDatabase.SetHasReadSeq(ctx, req.UserID, req.ConversationID, hasReadSeq)
		if err != nil {
			return nil, err
		}
	}

	reqCallback := &cbapi.CallbackSingleMsgReadReq{
		ConversationID: conversation.ConversationID,
		UserID:         req.UserID,
		Seqs:           req.Seqs,
		ContentType:    conversation.ConversationType,
	}
	m.webhookAfterSingleMsgRead(ctx, &m.config.WebhooksConfig.AfterSingleMsgRead, reqCallback)
	m.sendMarkAsReadNotification(ctx, req.ConversationID, conversation.ConversationType, req.UserID,
		m.conversationAndGetRecvID(conversation, req.UserID), req.Seqs, hasReadSeq)
	return &msg.MarkMsgsAsReadResp{}, nil
}

func (m *msgServer) MarkConversationAsRead(ctx context.Context, req *msg.MarkConversationAsReadReq) (*msg.MarkConversationAsReadResp, error) {
	conversation, err := m.ConversationLocalCache.GetConversation(ctx, req.UserID, req.ConversationID)
	if err != nil {
		return nil, err
	}
	hasReadSeq, err := m.MsgDatabase.GetHasReadSeq(ctx, req.UserID, req.ConversationID)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	var seqs []int64

	log.ZDebug(ctx, "MarkConversationAsRead", "hasReadSeq", hasReadSeq, "req.HasReadSeq", req.HasReadSeq)
	if conversation.ConversationType == constant.SingleChatType {
		for i := hasReadSeq + 1; i <= req.HasReadSeq; i++ {
			seqs = append(seqs, i)
		}
		// avoid client missed call MarkConversationMessageAsRead by order
		for _, val := range req.Seqs {
			if !datautil.Contain(val, seqs...) {
				seqs = append(seqs, val)
			}
		}
		if len(seqs) > 0 {
			log.ZDebug(ctx, "MarkConversationAsRead", "seqs", seqs, "conversationID", req.ConversationID)
			if err = m.MsgDatabase.MarkSingleChatMsgsAsRead(ctx, req.UserID, req.ConversationID, seqs); err != nil {
				return nil, err
			}
		}
		if req.HasReadSeq > hasReadSeq {
			err = m.MsgDatabase.SetHasReadSeq(ctx, req.UserID, req.ConversationID, req.HasReadSeq)
			if err != nil {
				return nil, err
			}
			hasReadSeq = req.HasReadSeq
		}
		m.sendMarkAsReadNotification(ctx, req.ConversationID, conversation.ConversationType, req.UserID,
			m.conversationAndGetRecvID(conversation, req.UserID), seqs, hasReadSeq)
	} else if conversation.ConversationType == constant.ReadGroupChatType ||
		conversation.ConversationType == constant.NotificationChatType {
		if req.HasReadSeq > hasReadSeq {
			err = m.MsgDatabase.SetHasReadSeq(ctx, req.UserID, req.ConversationID, req.HasReadSeq)
			if err != nil {
				return nil, err
			}
			hasReadSeq = req.HasReadSeq
		}
		m.sendMarkAsReadNotification(ctx, req.ConversationID, constant.SingleChatType, req.UserID,
			req.UserID, seqs, hasReadSeq)
	}

	if conversation.ConversationType == constant.SingleChatType {
		reqCall := &cbapi.CallbackSingleMsgReadReq{
			ConversationID: conversation.ConversationID,
			UserID:         conversation.OwnerUserID,
			Seqs:           req.Seqs,
			ContentType:    conversation.ConversationType,
		}
		m.webhookAfterSingleMsgRead(ctx, &m.config.WebhooksConfig.AfterSingleMsgRead, reqCall)
	} else if conversation.ConversationType == constant.ReadGroupChatType {
		reqCall := &cbapi.CallbackGroupMsgReadReq{
			SendID:       conversation.OwnerUserID,
			ReceiveID:    req.UserID,
			UnreadMsgNum: req.HasReadSeq,
			ContentType:  int64(conversation.ConversationType),
		}
		m.webhookAfterGroupMsgRead(ctx, &m.config.WebhooksConfig.AfterGroupMsgRead, reqCall)
	}
	return &msg.MarkConversationAsReadResp{}, nil
}

func (m *msgServer) sendMarkAsReadNotification(ctx context.Context, conversationID string, sessionType int32, sendID, recvID string, seqs []int64, hasReadSeq int64) {
	// 对群聊类型的已读回执进行限流和批处理
	if sessionType == constant.ReadGroupChatType && recvID != sendID {
		groupID := conversationID

		// 检查群组大小以确定是否需要抽样
		var groupMemberCount int
		memberIDs, err := m.GroupLocalCache.GetGroupMemberIDs(ctx, groupID)
		if err != nil {
			log.ZWarn(ctx, "获取群组成员数量失败", err, "groupID", groupID)
		} else {
			groupMemberCount = len(memberIDs)
		}

		// 基于群组大小确定抽样策略
		// 成员越多，发送频率越低
		var minInterval time.Duration
		var maxBatchSize int
		var samplingRate float64 = 1.0 // 默认发送所有消息

		switch {
		case groupMemberCount >= 5000:
			// 大型群组：每30秒最多发送一次，或当累积50个用户时发送
			// 额外应用1/10的抽样率
			minInterval = 30 * time.Second
			maxBatchSize = 50
			samplingRate = 0.1 // 只有约10%的消息会被发送
		case groupMemberCount >= 2000:
			// 中大型群组：每15秒最多发送一次，或当累积30个用户时发送
			// 额外应用1/5的抽样率
			minInterval = 15 * time.Second
			maxBatchSize = 30
			samplingRate = 0.2 // 只有约20%的消息会被发送
		case groupMemberCount >= 1000:
			// 中型群组：每10秒最多发送一次，或当累积20个用户时发送
			// 额外应用1/3的抽样率
			minInterval = 10 * time.Second
			maxBatchSize = 20
			samplingRate = 0.3 // 只有约33%的消息会被发送
		case groupMemberCount >= 500:
			// 中小型群组：每5秒最多发送一次，或当累积15个用户时发送
			// 额外应用1/2的抽样率
			minInterval = 5 * time.Second
			maxBatchSize = 15
			samplingRate = 0.5 // 只有约50%的消息会被发送
		case groupMemberCount >= 100:
			// 小型群组：每5秒最多发送一次，或当累积10个用户时发送
			minInterval = 5 * time.Second
			maxBatchSize = 10
			samplingRate = 0.8 // 80%的消息会被发送
		default:
			// 默认策略（小群）：每5秒最多发送一次，或当累积5个用户时发送
			minInterval = 5 * time.Second
			maxBatchSize = 5
		}

		// 随机抽样决定是否处理这条消息
		if samplingRate < 1.0 && rand.Float64() > samplingRate {
			// 被抽样过滤掉，不处理
			return
		}

		// 更新缓存
		groupReadMutex.Lock()

		// 初始化缓存结构（如果需要）
		if _, ok := groupReadCache[groupID]; !ok {
			groupReadCache[groupID] = make(map[string]int64)
		}

		// 存储最新读取状态
		groupReadCache[groupID][sendID] = hasReadSeq

		// 检查是否应该发送：根据群组大小确定的时间间隔和批次大小
		shouldSend := false
		now := time.Now()
		lastTime, hasTime := lastBatchTime[groupID]

		if !hasTime || now.Sub(lastTime) > minInterval || len(groupReadCache[groupID]) >= maxBatchSize {
			shouldSend = true
			lastBatchTime[groupID] = now

			// 记录有多少用户的读回执被批处理了
			userCount := len(groupReadCache[groupID])

			// 清空缓存（我们已经记录了要发送的用户）
			delete(groupReadCache, groupID)

			groupReadMutex.Unlock()

			if shouldSend {
				log.ZInfo(ctx, "批量处理群组读回执", "groupID", groupID, "userCount", userCount, "groupSize", groupMemberCount)

				// 使用简化格式发送，频率降低且不包含序列号列表
				tips := &sdkws.MarkAsReadTips{
					MarkAsReadUserID: sendID,
					ConversationID:   conversationID,
					Seqs:             nil,        // 省略序列号数组，大幅减小消息大小
					HasReadSeq:       hasReadSeq, // 只传递最高已读序列号
				}
				// 使用专用的读回执发送方法
				m.notificationSender.SendReadReceipt(ctx, sendID, recvID, constant.HasReadReceipt, sessionType, tips)
			}

			return
		}

		groupReadMutex.Unlock()
		// 不发送，等待批处理
		return
	}

	// 对于非群聊或自己发给自己的消息，也使用专用队列处理
	tips := &sdkws.MarkAsReadTips{
		MarkAsReadUserID: sendID,
		ConversationID:   conversationID,
		Seqs:             seqs,
		HasReadSeq:       hasReadSeq,
	}
	// 使用专用的读回执发送方法
	m.notificationSender.SendReadReceipt(ctx, sendID, recvID, constant.HasReadReceipt, sessionType, tips)
}
