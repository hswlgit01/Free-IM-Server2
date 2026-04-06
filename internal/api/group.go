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
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openimsdk/open-im-server/v3/protocol/group"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/a2r"
	"github.com/openimsdk/tools/apiresp"
	"github.com/openimsdk/tools/errs"
)

// setGroupInfoHTTPReq 兼容两种请求格式：标准 groupInfoForSet 或平铺的 groupID/groupName（如 Flutter SDK）
type setGroupInfoHTTPReq struct {
	GroupInfoForSet *sdkws.GroupInfoForSet `json:"groupInfoForSet"`
	GroupID         string                 `json:"groupID"`
	GroupName       string                 `json:"groupName"`
	Notification    string                 `json:"notification"`
	Introduction    string                 `json:"introduction"`
	FaceURL         string                 `json:"faceURL"`
}

type GroupApi struct {
	Client group.GroupClient
}

func NewGroupApi(client group.GroupClient) GroupApi {
	return GroupApi{client}
}

func (o *GroupApi) CreateGroup(c *gin.Context) {
	a2r.Call(c, group.GroupClient.CreateGroup, o.Client)
}

func (o *GroupApi) SetGroupInfo(c *gin.Context) {
	var body setGroupInfoHTTPReq
	if err := c.ShouldBindJSON(&body); err != nil {
		apiresp.GinError(c, errs.ErrArgs.WithDetail(err.Error()).Wrap())
		return
	}
	// 兼容：若客户端发送平铺的 groupID/groupName（如部分 SDK），则组装为 groupInfoForSet
	infoForSet := body.GroupInfoForSet
	if infoForSet == nil || infoForSet.GroupID == "" {
		if body.GroupID == "" {
			apiresp.GinError(c, errs.ErrArgs.WrapMsg("groupID is required"))
			return
		}
		infoForSet = &sdkws.GroupInfoForSet{
			GroupID:      body.GroupID,
			GroupName:    strings.TrimSpace(body.GroupName),
			Notification: body.Notification,
			Introduction: body.Introduction,
			FaceURL:      body.FaceURL,
		}
	} else if infoForSet.GroupName != "" {
		infoForSet.GroupName = strings.TrimSpace(infoForSet.GroupName)
	}
	req := &group.SetGroupInfoReq{GroupInfoForSet: infoForSet}
	// NOTE: use gin.Context as context.Context so rpc client interceptor can read operationID from it.
	resp, err := o.Client.SetGroupInfo(c, req)
	if err != nil {
		apiresp.GinError(c, err)
		return
	}
	apiresp.GinSuccess(c, resp)
}

func (o *GroupApi) SetGroupInfoEx(c *gin.Context) {
	a2r.Call(c, group.GroupClient.SetGroupInfoEx, o.Client)
}

func (o *GroupApi) JoinGroup(c *gin.Context) {
	a2r.Call(c, group.GroupClient.JoinGroup, o.Client)
}

func (o *GroupApi) QuitGroup(c *gin.Context) {
	a2r.Call(c, group.GroupClient.QuitGroup, o.Client)
}

func (o *GroupApi) ApplicationGroupResponse(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GroupApplicationResponse, o.Client)
}

func (o *GroupApi) TransferGroupOwner(c *gin.Context) {
	a2r.Call(c, group.GroupClient.TransferGroupOwner, o.Client)
}

func (o *GroupApi) GetRecvGroupApplicationList(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetGroupApplicationList, o.Client)
}

func (o *GroupApi) GetUserReqGroupApplicationList(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetUserReqApplicationList, o.Client)
}

func (o *GroupApi) GetGroupUsersReqApplicationList(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetGroupUsersReqApplicationList, o.Client)
}

func (o *GroupApi) GetSpecifiedUserGroupRequestInfo(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetSpecifiedUserGroupRequestInfo, o.Client)
}

func (o *GroupApi) GetGroupsInfo(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetGroupsInfo, o.Client)
	//a2r.Call(c, group.GroupClient.GetGroupsInfo, o.Client, c, a2r.NewNilReplaceOption(group.GroupClient.GetGroupsInfo))
}

func (o *GroupApi) KickGroupMember(c *gin.Context) {
	a2r.Call(c, group.GroupClient.KickGroupMember, o.Client)
}

func (o *GroupApi) GetGroupMembersInfo(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetGroupMembersInfo, o.Client)
	//a2r.Call(c, group.GroupClient.GetGroupMembersInfo, o.Client, c, a2r.NewNilReplaceOption(group.GroupClient.GetGroupMembersInfo))
}

func (o *GroupApi) GetGroupMemberList(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetGroupMemberList, o.Client)
}

func (o *GroupApi) InviteUserToGroup(c *gin.Context) {
	a2r.Call(c, group.GroupClient.InviteUserToGroup, o.Client)
}

func (o *GroupApi) GetJoinedGroupList(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetJoinedGroupList, o.Client)
}

func (o *GroupApi) DismissGroup(c *gin.Context) {
	a2r.Call(c, group.GroupClient.DismissGroup, o.Client)
}

func (o *GroupApi) MuteGroupMember(c *gin.Context) {
	a2r.Call(c, group.GroupClient.MuteGroupMember, o.Client)
}

func (o *GroupApi) CancelMuteGroupMember(c *gin.Context) {
	a2r.Call(c, group.GroupClient.CancelMuteGroupMember, o.Client)
}

func (o *GroupApi) MuteGroup(c *gin.Context) {
	a2r.Call(c, group.GroupClient.MuteGroup, o.Client)
}

func (o *GroupApi) CancelMuteGroup(c *gin.Context) {
	a2r.Call(c, group.GroupClient.CancelMuteGroup, o.Client)
}

func (o *GroupApi) SetGroupMemberInfo(c *gin.Context) {
	a2r.Call(c, group.GroupClient.SetGroupMemberInfo, o.Client)
}

func (o *GroupApi) GetGroupAbstractInfo(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetGroupAbstractInfo, o.Client)
}

// func (g *Group) SetGroupMemberNickname(c *gin.Context) {
//	a2r.Call(c, group.GroupClient.SetGroupMemberNickname, g.userClient)
//}
//
// func (g *Group) GetGroupAllMemberList(c *gin.Context) {
//	a2r.Call(c, group.GroupClient.GetGroupAllMember, g.userClient)
//}

func (o *GroupApi) GroupCreateCount(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GroupCreateCount, o.Client)
}

func (o *GroupApi) GetGroups(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetGroups, o.Client)
}

func (o *GroupApi) GetGroupMemberUserIDs(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetGroupMemberUserIDs, o.Client)
}

func (o *GroupApi) GetIncrementalJoinGroup(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetIncrementalJoinGroup, o.Client)
}

func (o *GroupApi) GetIncrementalGroupMember(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetIncrementalGroupMember, o.Client)
}

func (o *GroupApi) GetIncrementalGroupMemberBatch(c *gin.Context) {
	a2r.Call(c, group.GroupClient.BatchGetIncrementalGroupMember, o.Client)
}

func (o *GroupApi) GetFullGroupMemberUserIDs(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetFullGroupMemberUserIDs, o.Client)
}

func (o *GroupApi) GetFullJoinGroupIDs(c *gin.Context) {
	a2r.Call(c, group.GroupClient.GetFullJoinGroupIDs, o.Client)
}

// CreateGroupOperationLog 创建群操作日志 (仅admin可调用)
func (o *GroupApi) CreateGroupOperationLog(c *gin.Context) {
	a2r.Call(c, group.GroupClient.CreateGroupOperationLog, o.Client)
}
