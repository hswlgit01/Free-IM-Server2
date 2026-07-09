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

package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/openimsdk/open-im-server/v3/pkg/msgprocessor"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/field"
)

const (
	DefaultFolderPath = "../config/"
)

// return absolude path join ../config/, this is k8s container config path.
func GetDefaultConfigPath() (string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", errs.WrapMsg(err, "failed to get executable path")
	}

	configPath, err := field.OutDir(filepath.Join(filepath.Dir(executablePath), "../config/"))
	if err != nil {
		return "", errs.WrapMsg(err, "failed to get output directory", "outDir", filepath.Join(filepath.Dir(executablePath), "../config/"))
	}
	return configPath, nil
}

// getProjectRoot returns the absolute path of the project root directory.
func GetProjectRoot() (string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", errs.Wrap(err)
	}
	projectRoot, err := field.OutDir(filepath.Join(filepath.Dir(executablePath), "../../../../.."))
	if err != nil {
		return "", errs.Wrap(err)
	}
	return projectRoot, nil
}

func GetOptionsByNotification(cfg NotificationConfig, sendMessage *bool) msgprocessor.Options {
	opts := msgprocessor.NewOptions()

	if sendMessage != nil {
		cfg.IsSendMsg = *sendMessage
	}
	if cfg.IsSendMsg {
		// UnreadCount is deprecated and fixed as false for all notifications
		opts = msgprocessor.WithOptions(opts, msgprocessor.WithUnreadCount(cfg.UnreadCount))
	}
	if cfg.OfflinePush.Enable {
		opts = msgprocessor.WithOptions(opts, msgprocessor.WithOfflinePush(true))
	}
	switch cfg.ReliabilityLevel {
	case constant.UnreliableNotification:
	case constant.ReliableNotificationNoMsg:
		opts = msgprocessor.WithOptions(opts, msgprocessor.WithHistory(true), msgprocessor.WithPersistent())
	case constant.ReliableNotificationMsg:
		// dawn 2026-04-27 修撤回不下发：原版 switch 漏掉这条 case，导致 ReliabilityLevel=3
		// 的通知（撤回 MsgRevokeNotification）拿不到 WithHistory/WithPersistent，
		// 在 categorizeMessageLists 里被分到 notStorageNotificationList（不存历史、
		// 不持久化），接收方 SDK 拿不到该撤回通知，对端永远看不到 "xxx 撤回了一条消息"。
		// 行为上至少要和 NoMsg 一致 —— 存历史 + 持久化，让接收方 sync 时能拿到。
		opts = msgprocessor.WithOptions(opts, msgprocessor.WithHistory(true), msgprocessor.WithPersistent())
	}
	// dawn 2026-07-09 修"建群/拉人进群后客户端不显示会话，要发一条消息会话才出现"(#14)：
	// isSendMsg=true 语义是"这条通知要作为消息出现在会话里"；但 reliabilityLevel 被上游固定为 1
	// (UnreliableNotification)，上面的 switch 命中空分支 → 拿不到 WithHistory/WithPersistent →
	// 通知不存历史、不持久化 → 不落 msg、不分配 seq → 接收端 sync 不到任何消息，也就无从建会话，
	// 于是新群一直到有人发第一条真实消息(分配 seq=1)才出现在会话列表里。
	// 实测佐证：新建群 sg_<gid> 的 seq 为 null、msg 文档数为 0。
	// "要作为消息展示"就必须存储，否则接收端拿不到。故 isSendMsg=true 时补上 WithHistory+WithPersistent，
	// 与 2026-04-27 修"撤回通知不下发"是同一类问题、同一处修法。
	if cfg.IsSendMsg {
		opts = msgprocessor.WithOptions(opts, msgprocessor.WithHistory(true), msgprocessor.WithPersistent())
	}
	opts = msgprocessor.WithOptions(opts, msgprocessor.WithSendMsg(cfg.IsSendMsg))

	return opts
}

// initConfig loads configuration from a specified path into the provided config structure.
// If the specified config file does not exist, it attempts to load from the project's default "config" directory.
// It logs informative messages regarding the configuration path being used.
func initConfig(config any, configName, configFolderPath string) error {
	configFolderPath = filepath.Join(configFolderPath, configName)
	_, err := os.Stat(configFolderPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return errs.WrapMsg(err, "stat config path error", "config Folder Path", configFolderPath)
		}
		path, err := GetProjectRoot()
		if err != nil {
			return err
		}
		configFolderPath = filepath.Join(path, "config", configName)
	}
	data, err := os.ReadFile(configFolderPath)
	if err != nil {
		return errs.WrapMsg(err, "read file error", "config Folder Path", configFolderPath)
	}
	if err = yaml.Unmarshal(data, config); err != nil {
		return errs.WrapMsg(err, "unmarshal yaml error", "config Folder Path", configFolderPath)
	}

	return nil
}
