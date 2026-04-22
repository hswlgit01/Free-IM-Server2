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

package model

import (
	"time"
)

type User struct {
	UserID           string    `bson:"user_id"`
	Nickname         string    `bson:"nickname"`
	FaceURL          string    `bson:"face_url"`
	Ex               string    `bson:"ex"`
	AppMangerLevel   int32     `bson:"app_manger_level"`
	GlobalRecvMsgOpt int32     `bson:"global_recv_msg_opt"`
	CanSendFreeMsg   int32     `bson:"can_send_free_msg"` // 新增：0=普通用户需好友验证，1=可跳过消息验证
	CreateTime       time.Time `bson:"create_time"`
	OrgId            string    `bson:"org_id"`   //组织ID
	OrgRole          string    `bson:"org_role"` // 五种枚举 - "SuperAdmin", "BackendAdmin", "GroupManager", "TermManager"(团队长), "Normal"。前四种在单聊时可越过好友校验，参见 internal/rpc/msg/verify.go:isPrivilegedOrgRole
	AESKey           string    `bson:"aes_key"`
}

func (u *User) GetNickname() string {
	return u.Nickname
}

func (u *User) GetFaceURL() string {
	return u.FaceURL
}

func (u *User) GetUserID() string {
	return u.UserID
}

func (u *User) GetEx() string {
	return u.Ex
}
