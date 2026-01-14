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
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/common/prommetrics"
	"github.com/openimsdk/open-im-server/v3/pkg/msgprocessor"
	"github.com/openimsdk/open-im-server/v3/pkg/util/conversationutil"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	pbconv "github.com/openimsdk/open-im-server/v3/protocol/conversation"
	pbmsg "github.com/openimsdk/open-im-server/v3/protocol/msg"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/open-im-server/v3/protocol/wrapperspb"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/mcontext"
	"github.com/openimsdk/tools/utils/datautil"
	"google.golang.org/protobuf/proto"
)

func (m *msgServer) SendMsg(ctx context.Context, req *pbmsg.SendMsgReq) (*pbmsg.SendMsgResp, error) {
	if req.MsgData == nil {
		return nil, errs.ErrArgs.WrapMsg("msgData is nil")
	}
	before := new(*sdkws.MsgData)
	resp, err := m.sendMsg(ctx, req, before)
	if err != nil {
		return nil, err
	}
	if *before != nil && proto.Equal(*before, req.MsgData) == false {
		resp.Modify = req.MsgData
	}
	return resp, nil
}

func (m *msgServer) sendMsg(ctx context.Context, req *pbmsg.SendMsgReq, before **sdkws.MsgData) (*pbmsg.SendMsgResp, error) {
	m.encapsulateMsgData(req.MsgData)
	if req.MsgData.ContentType == constant.Stream {
		if err := m.handlerStreamMsg(ctx, req.MsgData); err != nil {
			return nil, err
		}
	}

	// 返回确认消息所需的基本信息
	resp := &pbmsg.SendMsgResp{
		SendTime:    req.MsgData.SendTime,
		ServerMsgID: req.MsgData.ServerMsgID,
		ClientMsgID: req.MsgData.ClientMsgID,
	}

	// 异步处理消息，允许快速确认
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.ZPanic(ctx, "sendMsg panic", errs.ErrPanic(r))
			}
		}()

		// 创建新上下文以防原始上下文超时
		ctxTimeout, cancel := context.WithTimeout(context.Background(), time.Minute*5)
		defer cancel()
		// 复制操作ID到新上下文
		asyncCtx := mcontext.SetOperationID(ctxTimeout, mcontext.GetOperationID(ctx))

		var err error
		switch req.MsgData.SessionType {
		case constant.SingleChatType:
			_, err = m.processSingleChatMsg(asyncCtx, req, before)
		case constant.NotificationChatType:
			_, err = m.sendMsgNotification(asyncCtx, req, before)
		case constant.ReadGroupChatType:
			_, err = m.processGroupChatMsg(asyncCtx, req, before)
		default:
			log.ZError(asyncCtx, "unknown sessionType", nil, "sessionType", req.MsgData.SessionType)
		}

		if err != nil {
			log.ZError(asyncCtx, "async message processing failed", err, "clientMsgID", req.MsgData.ClientMsgID)
		}
	}()

	// 立即返回确认
	return resp, nil
}

func (m *msgServer) processGroupChatMsg(ctx context.Context, req *pbmsg.SendMsgReq, before **sdkws.MsgData) (resp *pbmsg.SendMsgResp, err error) {
	if err = m.messageVerification(ctx, req); err != nil {
		prommetrics.GroupChatMsgProcessFailedCounter.Inc()
		return nil, err
	}

	if err = m.webhookBeforeSendGroupMsg(ctx, &m.config.WebhooksConfig.BeforeSendGroupMsg, req); err != nil {
		return nil, err
	}
	if err := m.webhookBeforeMsgModify(ctx, &m.config.WebhooksConfig.BeforeMsgModify, req, before); err != nil {
		return nil, err
	}
	err = m.MsgDatabase.MsgToMQ(ctx, conversationutil.GenConversationUniqueKeyForGroup(req.MsgData.GroupID), req.MsgData)
	if err != nil {
		return nil, err
	}
	if req.MsgData.ContentType == constant.AtText {
		go m.setConversationAtInfo(ctx, req.MsgData)
	}

	m.webhookAfterSendGroupMsg(ctx, &m.config.WebhooksConfig.AfterSendGroupMsg, req)
	prommetrics.GroupChatMsgProcessSuccessCounter.Inc()
	resp = &pbmsg.SendMsgResp{}
	resp.SendTime = req.MsgData.SendTime
	resp.ServerMsgID = req.MsgData.ServerMsgID
	resp.ClientMsgID = req.MsgData.ClientMsgID
	return resp, nil
}

// 保留原方法供兼容使用
func (m *msgServer) sendMsgGroupChat(ctx context.Context, req *pbmsg.SendMsgReq, before **sdkws.MsgData) (resp *pbmsg.SendMsgResp, err error) {
	return m.processGroupChatMsg(ctx, req, before)
}

func (m *msgServer) setConversationAtInfo(nctx context.Context, msg *sdkws.MsgData) {

	log.ZDebug(nctx, "setConversationAtInfo", "msg", msg)

	defer func() {
		if r := recover(); r != nil {
			log.ZPanic(nctx, "setConversationAtInfo Panic", errs.ErrPanic(r))
		}
	}()

	ctx := mcontext.NewCtx("@@@" + mcontext.GetOperationID(nctx))

	var atUserID []string

	conversation := &pbconv.ConversationReq{
		ConversationID:   msgprocessor.GetConversationIDByMsg(msg),
		ConversationType: msg.SessionType,
		GroupID:          msg.GroupID,
	}
	memberUserIDList, err := m.GroupLocalCache.GetGroupMemberIDs(ctx, msg.GroupID)
	if err != nil {
		log.ZWarn(ctx, "GetGroupMemberIDs", err)
		return
	}

	tagAll := datautil.Contain(constant.AtAllString, msg.AtUserIDList...)
	if tagAll {

		memberUserIDList = datautil.DeleteElems(memberUserIDList, msg.SendID)

		atUserID = datautil.Single([]string{constant.AtAllString}, msg.AtUserIDList)

		if len(atUserID) == 0 { // just @everyone
			conversation.GroupAtType = &wrapperspb.Int32Value{Value: constant.AtAll}
		} else { // @Everyone and @other people
			conversation.GroupAtType = &wrapperspb.Int32Value{Value: constant.AtAllAtMe}
			atUserID = datautil.SliceIntersectFuncs(atUserID, memberUserIDList, func(a string) string { return a }, func(b string) string {
				return b
			})
			if err := m.conversationClient.SetConversations(ctx, atUserID, conversation); err != nil {
				log.ZWarn(ctx, "SetConversations", err, "userID", atUserID, "conversation", conversation)
			}
			memberUserIDList = datautil.Single(atUserID, memberUserIDList)
		}

		conversation.GroupAtType = &wrapperspb.Int32Value{Value: constant.AtAll}
		if err := m.conversationClient.SetConversations(ctx, memberUserIDList, conversation); err != nil {
			log.ZWarn(ctx, "SetConversations", err, "userID", memberUserIDList, "conversation", conversation)
		}

		return
	}
	atUserID = datautil.SliceIntersectFuncs(msg.AtUserIDList, memberUserIDList, func(a string) string { return a }, func(b string) string {
		return b
	})
	conversation.GroupAtType = &wrapperspb.Int32Value{Value: constant.AtMe}

	if err := m.conversationClient.SetConversations(ctx, atUserID, conversation); err != nil {
		log.ZWarn(ctx, "SetConversations", err, atUserID, conversation)
	}
}

func (m *msgServer) sendMsgNotification(ctx context.Context, req *pbmsg.SendMsgReq, before **sdkws.MsgData) (resp *pbmsg.SendMsgResp, err error) {
	if err := m.MsgDatabase.MsgToMQ(ctx, conversationutil.GenConversationUniqueKeyForSingle(req.MsgData.SendID, req.MsgData.RecvID), req.MsgData); err != nil {
		return nil, err
	}
	resp = &pbmsg.SendMsgResp{
		ServerMsgID: req.MsgData.ServerMsgID,
		ClientMsgID: req.MsgData.ClientMsgID,
		SendTime:    req.MsgData.SendTime,
	}
	return resp, nil
}

func (m *msgServer) processSingleChatMsg(ctx context.Context, req *pbmsg.SendMsgReq, before **sdkws.MsgData) (resp *pbmsg.SendMsgResp, err error) {
	if err := m.messageVerification(ctx, req); err != nil {
		return nil, err
	}
	isSend := true
	isNotification := msgprocessor.IsNotificationByMsg(req.MsgData)
	// 如果是自发自收消息(如语音通话的状态通知),跳过接收选项检查
	// 避免查询不存在的自我会话(si_userID_userID)
	if !isNotification && req.MsgData.SendID != req.MsgData.RecvID {
		isSend, err = m.modifyMessageByUserMessageReceiveOpt(
			ctx,
			req.MsgData.RecvID,
			conversationutil.GenConversationIDForSingle(req.MsgData.SendID, req.MsgData.RecvID),
			constant.SingleChatType,
			req,
		)
		if err != nil {
			return nil, err
		}
	}
	if !isSend {
		prommetrics.SingleChatMsgProcessFailedCounter.Inc()
		return nil, errs.ErrArgs.WrapMsg("message is not sent")
	} else {
		if err := m.webhookBeforeMsgModify(ctx, &m.config.WebhooksConfig.BeforeMsgModify, req, before); err != nil {
			return nil, err
		}
		conversationKey := conversationutil.GenConversationUniqueKeyForSingle(req.MsgData.SendID, req.MsgData.RecvID)
		if err := m.MsgDatabase.MsgToMQ(ctx, conversationKey, req.MsgData); err != nil {
			prommetrics.SingleChatMsgProcessFailedCounter.Inc()
			return nil, err
		}
		m.webhookAfterSendSingleMsg(ctx, &m.config.WebhooksConfig.AfterSendSingleMsg, req)
		prommetrics.SingleChatMsgProcessSuccessCounter.Inc()
		return &pbmsg.SendMsgResp{
			ServerMsgID: req.MsgData.ServerMsgID,
			ClientMsgID: req.MsgData.ClientMsgID,
			SendTime:    req.MsgData.SendTime,
		}, nil
	}
}

// 保留原方法供兼容使用
func (m *msgServer) sendMsgSingleChat(ctx context.Context, req *pbmsg.SendMsgReq, before **sdkws.MsgData) (resp *pbmsg.SendMsgResp, err error) {
	return m.processSingleChatMsg(ctx, req, before)
}
