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
	"net"

	"github.com/gin-gonic/gin"
	"github.com/openimsdk/open-im-server/v3/protocol/auth"
	"github.com/openimsdk/tools/a2r"
)

type AuthApi struct {
	Client auth.AuthClient
}

func NewAuthApi(client auth.AuthClient) AuthApi {
	return AuthApi{client}
}

// isLocalRequest 检查请求是否来自内网（严格验证，不依赖可伪造的HTTP头）
func isLocalRequest(c *gin.Context) bool {
	// 只使用 RemoteAddr，这个不能被客户端伪造
	// RemoteAddr 是操作系统网络层提供的真实连接地址
	clientIP := getRealClientIP(c)

	// 检查是否为内网IP（包括loopback和私有网络地址）
	return isLocalIP(clientIP)
}

// getRealClientIP 获取真实的客户端IP地址（不信任HTTP头部）
func getRealClientIP(c *gin.Context) string {
	// 只使用 RemoteAddr，忽略所有可以被伪造的HTTP头部
	// 这样确保了安全性，防止IP伪造攻击
	ip, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		// 如果解析失败，直接返回RemoteAddr
		return c.Request.RemoteAddr
	}
	return ip
}

// isLocalIP 检查IP是否为内网地址（包括loopback和私有网络地址）
func isLocalIP(ip string) bool {
	if ip == "127.0.0.1" || ip == "::1" {
		return true
	}

	// 解析IP地址
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}

	// 检查是否为loopback地址
	if parsedIP.IsLoopback() {
		return true
	}

	// 检查是否为私有网络地址 (RFC 1918)
	// 10.0.0.0/8 (10.0.0.0 - 10.255.255.255)
	// 172.16.0.0/12 (172.16.0.0 - 172.31.255.255)
	// 192.168.0.0/16 (192.168.0.0 - 192.168.255.255)

	// 定义私有网络CIDR范围
	privateNetworks := []string{
		"10.0.0.0/8",     // Class A private network
		"172.16.0.0/12",  // Class B private network
		"192.168.0.0/16", // Class C private network
	}

	for _, network := range privateNetworks {
		_, cidr, err := net.ParseCIDR(network)
		if err != nil {
			continue
		}
		if cidr.Contains(parsedIP) {
			return true
		}
	}

	return false
}

func (o *AuthApi) GetAdminToken(c *gin.Context) {
	// 验证请求是否来自内网（包括Docker容器环境的私有网络）
	//if !isLocalRequest(c) {
	//	clientIP := getRealClientIP(c)
	//	apiresp.GinError(c, errs.ErrNoPermission.WrapMsg("GetAdminToken接口只允许内网调用", "clientIP", clientIP))
	//	return
	//}

	a2r.Call(c, auth.AuthClient.GetAdminToken, o.Client)
}

func (o *AuthApi) GetUserToken(c *gin.Context) {
	a2r.Call(c, auth.AuthClient.GetUserToken, o.Client)
}

func (o *AuthApi) ParseToken(c *gin.Context) {
	a2r.Call(c, auth.AuthClient.ParseToken, o.Client)
}

func (o *AuthApi) ForceLogout(c *gin.Context) {
	a2r.Call(c, auth.AuthClient.ForceLogout, o.Client)
}

func (o *AuthApi) BatchForceLogout(c *gin.Context) {
	a2r.Call(c, auth.AuthClient.BatchForceLogout, o.Client)
}
