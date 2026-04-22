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
	"strings"

	"github.com/openimsdk/open-im-server/v3/pkg/authverify"
	"github.com/openimsdk/open-im-server/v3/pkg/msgprocessor"
	"github.com/openimsdk/open-im-server/v3/pkg/util/conversationutil"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msg"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/utils/datautil"
	"github.com/openimsdk/tools/utils/timeutil"
)

// maxTotalMessagesPerPullResponse：
//   - 说明：限制一次 PullMessageBySeqs 响应中「所有会话」加起来最多返回多少条消息。
//   - 目的：防止老账号/多会话场景下，单次返回过多消息导致：
//     1）响应体过大，触发 WebSocket 1009（message too big）；
//     2）网络传输和客户端解包耗时过长，放大超时风险。
const maxTotalMessagesPerPullResponse = 500

// maxSeqRangesPerRequest 单次请求允许的会话数（SeqRange）上限，防止老版本客户端一次带上全部会话导致响应体过大、同步异常或 1009
const maxSeqRangesPerRequest = 100

func (m *msgServer) PullMessageBySeqs(ctx context.Context, req *sdkws.PullMessageBySeqsReq) (*sdkws.PullMessageBySeqsResp, error) {
	resp := &sdkws.PullMessageBySeqsResp{}
	resp.Msgs = make(map[string]*sdkws.PullMsgs)
	resp.NotificationMsgs = make(map[string]*sdkws.PullMsgs)
	var totalMsgCount int
	// 老版本客户端可能一次请求所有会话，导致响应体过大、延迟或 1009；仅处理前 N 个会话，其余需客户端分次拉取或升级后分页
	seqRanges := req.SeqRanges
	if len(seqRanges) > maxSeqRangesPerRequest {
		log.ZWarn(ctx, "PullMessageBySeqs: too many seqRanges, truncating to protect server and avoid message too big", nil,
			"requested", len(req.SeqRanges), "processing", maxSeqRangesPerRequest, "userID", req.UserID)
		seqRanges = seqRanges[:maxSeqRangesPerRequest]
	}
	for _, seq := range seqRanges {
		if totalMsgCount >= maxTotalMessagesPerPullResponse {
			if !msgprocessor.IsNotification(seq.ConversationID) {
				resp.Msgs[seq.ConversationID] = &sdkws.PullMsgs{Msgs: []*sdkws.MsgData{}, IsEnd: false}
			}
			continue
		}
		if !msgprocessor.IsNotification(seq.ConversationID) {
			conversation, err := m.ConversationLocalCache.GetConversation(ctx, req.UserID, seq.ConversationID)
			if err != nil {
				log.ZWarn(ctx, "GetConversation not found or error, return empty", err, "conversationID", seq.ConversationID)
				resp.Msgs[seq.ConversationID] = &sdkws.PullMsgs{Msgs: []*sdkws.MsgData{}, IsEnd: false}
				continue
			}
			minSeq, maxSeq, msgs, err := m.MsgDatabase.GetMsgBySeqsRange(ctx, req.UserID, seq.ConversationID,
				seq.Begin, seq.End, seq.Num, conversation.MaxSeq)
			if err != nil {
				log.ZWarn(ctx, "GetMsgBySeqsRange error", err, "conversationID", seq.ConversationID, "seq", seq)
				resp.Msgs[seq.ConversationID] = &sdkws.PullMsgs{Msgs: []*sdkws.MsgData{}, IsEnd: false}
				continue
			}
			var isEnd bool
			if totalMsgCount+len(msgs) > maxTotalMessagesPerPullResponse {
				limit := maxTotalMessagesPerPullResponse - totalMsgCount
				if limit > 0 {
					msgs = msgs[:limit]
					isEnd = false
				} else {
					msgs = []*sdkws.MsgData{}
					isEnd = false
				}
				totalMsgCount = maxTotalMessagesPerPullResponse
			} else {
				totalMsgCount += len(msgs)
				switch req.Order {
				case sdkws.PullOrder_PullOrderAsc:
					isEnd = maxSeq <= seq.End
				case sdkws.PullOrder_PullOrderDesc:
					isEnd = seq.Begin <= minSeq
				}
			}
			if len(msgs) == 0 {
				log.ZDebug(ctx, "conversation has no msgs in range", "conversationID", seq.ConversationID, "seq", seq)
			}
			resp.Msgs[seq.ConversationID] = &sdkws.PullMsgs{Msgs: msgs, IsEnd: isEnd}
		} else {
			if totalMsgCount >= maxTotalMessagesPerPullResponse {
				resp.NotificationMsgs[seq.ConversationID] = &sdkws.PullMsgs{Msgs: []*sdkws.MsgData{}, IsEnd: false}
				continue
			}
			var seqs []int64
			for i := seq.Begin; i <= seq.End; i++ {
				seqs = append(seqs, i)
			}
			minSeq, maxSeq, notificationMsgs, err := m.MsgDatabase.GetMsgBySeqs(ctx, req.UserID, seq.ConversationID, seqs)
			if err != nil {
				log.ZWarn(ctx, "GetMsgBySeqs error", err, "conversationID", seq.ConversationID, "seq", seq)
				continue
			}
			var isEnd bool
			switch req.Order {
			case sdkws.PullOrder_PullOrderAsc:
				isEnd = maxSeq <= seq.End
			case sdkws.PullOrder_PullOrderDesc:
				isEnd = seq.Begin <= minSeq
			}
			if len(notificationMsgs) == 0 {
				log.ZWarn(ctx, "not have notificationMsgs", nil, "conversationID", seq.ConversationID, "seq", seq)
				continue
			}
			if totalMsgCount+len(notificationMsgs) > maxTotalMessagesPerPullResponse {
				limit := maxTotalMessagesPerPullResponse - totalMsgCount
				if limit > 0 {
					notificationMsgs = notificationMsgs[:limit]
				} else {
					notificationMsgs = []*sdkws.MsgData{}
				}
				isEnd = false
				totalMsgCount = maxTotalMessagesPerPullResponse
			} else {
				totalMsgCount += len(notificationMsgs)
			}
			resp.NotificationMsgs[seq.ConversationID] = &sdkws.PullMsgs{Msgs: notificationMsgs, IsEnd: isEnd}
		}
	}
	return resp, nil
}

func (m *msgServer) GetSeqMessage(ctx context.Context, req *msg.GetSeqMessageReq) (*msg.GetSeqMessageResp, error) {
	resp := &msg.GetSeqMessageResp{
		Msgs:             make(map[string]*sdkws.PullMsgs),
		NotificationMsgs: make(map[string]*sdkws.PullMsgs),
	}
	for _, conv := range req.Conversations {
		isEnd, endSeq, msgs, err := m.MsgDatabase.GetMessagesBySeqWithBounds(ctx, req.UserID, conv.ConversationID, conv.Seqs, req.GetOrder())
		if err != nil {
			return nil, err
		}
		var pullMsgs *sdkws.PullMsgs
		if ok := false; conversationutil.IsNotificationConversationID(conv.ConversationID) {
			pullMsgs, ok = resp.NotificationMsgs[conv.ConversationID]
			if !ok {
				pullMsgs = &sdkws.PullMsgs{}
				resp.NotificationMsgs[conv.ConversationID] = pullMsgs
			}
		} else {
			pullMsgs, ok = resp.Msgs[conv.ConversationID]
			if !ok {
				pullMsgs = &sdkws.PullMsgs{}
				resp.Msgs[conv.ConversationID] = pullMsgs
			}
		}
		pullMsgs.Msgs = append(pullMsgs.Msgs, msgs...)
		pullMsgs.IsEnd = isEnd
		pullMsgs.EndSeq = endSeq
	}
	return resp, nil
}

func (m *msgServer) GetMaxSeq(ctx context.Context, req *sdkws.GetMaxSeqReq) (*sdkws.GetMaxSeqResp, error) {
	if err := authverify.CheckAccessV3(ctx, req.UserID, m.config.Share.IMAdminUserID); err != nil {
		return nil, err
	}
	conversationIDs, err := m.ConversationLocalCache.GetConversationIDs(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	// Drop dismissed-group conversations so a client clearing its cache and re-syncing
	// cannot re-hydrate history from a group that was disbanded.
	conversationIDs = m.filterDismissedGroupConversationIDs(ctx, conversationIDs)
	for _, conversationID := range conversationIDs {
		conversationIDs = append(conversationIDs, conversationutil.GetNotificationConversationIDByConversationID(conversationID))
	}
	conversationIDs = append(conversationIDs, conversationutil.GetSelfNotificationConversationID(req.UserID))
	log.ZDebug(ctx, "GetMaxSeq", "conversationIDs", conversationIDs)
	maxSeqs, err := m.MsgDatabase.GetMaxSeqs(ctx, conversationIDs)
	if err != nil {
		log.ZWarn(ctx, "GetMaxSeqs error", err, "conversationIDs", conversationIDs, "maxSeqs", maxSeqs)
		return nil, err
	}
	// avoid pulling messages from sessions with a large number of max seq values of 0
	for conversationID, seq := range maxSeqs {
		if seq == 0 {
			delete(maxSeqs, conversationID)
		}
	}
	resp := new(sdkws.GetMaxSeqResp)
	resp.MaxSeqs = maxSeqs
	return resp, nil
}

// filterDismissedGroupConversationIDs removes `sg_<groupID>` entries whose group has been
// dismissed. Non-group conversation IDs pass through untouched. Errors fetching group info
// fail open (we keep the ID) to avoid hiding conversations on a transient cache miss.
func (m *msgServer) filterDismissedGroupConversationIDs(ctx context.Context, conversationIDs []string) []string {
	groupIDs := make([]string, 0)
	for _, cid := range conversationIDs {
		if strings.HasPrefix(cid, "sg_") {
			groupIDs = append(groupIDs, strings.TrimPrefix(cid, "sg_"))
		}
	}
	if len(groupIDs) == 0 {
		return conversationIDs
	}
	groupInfos, err := m.GroupLocalCache.GetGroupInfos(ctx, groupIDs)
	if err != nil {
		log.ZWarn(ctx, "GetGroupInfos failed while filtering dismissed groups; falling back to unfiltered list", err, "groupIDs", groupIDs)
		return conversationIDs
	}
	dismissed := make(map[string]struct{}, len(groupInfos))
	for _, g := range groupInfos {
		if g != nil && g.Status == constant.GroupStatusDismissed {
			dismissed[g.GroupID] = struct{}{}
		}
	}
	if len(dismissed) == 0 {
		return conversationIDs
	}
	filtered := make([]string, 0, len(conversationIDs))
	for _, cid := range conversationIDs {
		if strings.HasPrefix(cid, "sg_") {
			if _, ok := dismissed[strings.TrimPrefix(cid, "sg_")]; ok {
				continue
			}
		}
		filtered = append(filtered, cid)
	}
	return filtered
}

func (m *msgServer) SearchMessage(ctx context.Context, req *msg.SearchMessageReq) (resp *msg.SearchMessageResp, err error) {
	// var chatLogs []*sdkws.MsgData
	var chatLogs []*msg.SearchedMsgData
	var total int64
	resp = &msg.SearchMessageResp{}
	if total, chatLogs, err = m.MsgDatabase.SearchMessage(ctx, req); err != nil {
		return nil, err
	}

	var (
		sendIDs  []string
		recvIDs  []string
		groupIDs []string
		sendMap  = make(map[string]string)
		recvMap  = make(map[string]string)
		groupMap = make(map[string]*sdkws.GroupInfo)
	)

	for _, chatLog := range chatLogs {
		if chatLog.MsgData.SenderNickname == "" {
			sendIDs = append(sendIDs, chatLog.MsgData.SendID)
		}
		switch chatLog.MsgData.SessionType {
		case constant.SingleChatType, constant.NotificationChatType:
			recvIDs = append(recvIDs, chatLog.MsgData.RecvID)
		case constant.WriteGroupChatType, constant.ReadGroupChatType:
			groupIDs = append(groupIDs, chatLog.MsgData.GroupID)
		}
	}

	// Retrieve sender and receiver information
	if len(sendIDs) != 0 {
		sendInfos, err := m.UserLocalCache.GetUsersInfo(ctx, sendIDs)
		if err != nil {
			return nil, err
		}
		for _, sendInfo := range sendInfos {
			sendMap[sendInfo.UserID] = sendInfo.Nickname
		}
	}

	if len(recvIDs) != 0 {
		recvInfos, err := m.UserLocalCache.GetUsersInfo(ctx, recvIDs)
		if err != nil {
			return nil, err
		}
		for _, recvInfo := range recvInfos {
			recvMap[recvInfo.UserID] = recvInfo.Nickname
		}
	}

	// Retrieve group information including member counts
	if len(groupIDs) != 0 {
		groupInfos, err := m.GroupLocalCache.GetGroupInfos(ctx, groupIDs)
		if err != nil {
			return nil, err
		}
		for _, groupInfo := range groupInfos {
			groupMap[groupInfo.GroupID] = groupInfo
			// Get actual member count
			memberIDs, err := m.GroupLocalCache.GetGroupMemberIDs(ctx, groupInfo.GroupID)
			if err == nil {
				groupInfo.MemberCount = uint32(len(memberIDs)) // Update the member count with actual number
			}
		}
	}

	// Construct response with updated information
	for _, chatLog := range chatLogs {
		pbchatLog := &msg.ChatLog{}
		datautil.CopyStructFields(pbchatLog, chatLog.MsgData)
		pbchatLog.SendTime = chatLog.MsgData.SendTime
		pbchatLog.CreateTime = chatLog.MsgData.CreateTime
		if chatLog.MsgData.SenderNickname == "" {
			pbchatLog.SenderNickname = sendMap[chatLog.MsgData.SendID]
		}
		switch chatLog.MsgData.SessionType {
		case constant.SingleChatType, constant.NotificationChatType:
			pbchatLog.RecvNickname = recvMap[chatLog.MsgData.RecvID]
		case constant.ReadGroupChatType:
			groupInfo := groupMap[chatLog.MsgData.GroupID]
			pbchatLog.SenderFaceURL = groupInfo.FaceURL
			pbchatLog.GroupMemberCount = groupInfo.MemberCount // Reflects actual member count
			pbchatLog.RecvID = groupInfo.GroupID
			pbchatLog.GroupName = groupInfo.GroupName
			pbchatLog.GroupOwner = groupInfo.OwnerUserID
			pbchatLog.GroupType = groupInfo.GroupType
		}
		searchChatLog := &msg.SearchChatLog{ChatLog: pbchatLog, IsRevoked: chatLog.IsRevoked}

		resp.ChatLogs = append(resp.ChatLogs, searchChatLog)
	}
	resp.ChatLogsNum = int32(total)
	return resp, nil
}

func (m *msgServer) GetServerTime(ctx context.Context, _ *msg.GetServerTimeReq) (*msg.GetServerTimeResp, error) {
	return &msg.GetServerTimeResp{ServerTime: timeutil.GetCurrentTimestampByMill()}, nil
}

func (m *msgServer) GetLastMessage(ctx context.Context, req *msg.GetLastMessageReq) (*msg.GetLastMessageResp, error) {
	msgs, err := m.MsgDatabase.GetLastMessage(ctx, req.ConversationIDs, req.UserID)
	if err != nil {
		return nil, err
	}
	return &msg.GetLastMessageResp{Msgs: msgs}, nil
}
