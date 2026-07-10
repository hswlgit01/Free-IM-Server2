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

package auth

import (
	"context"
	"errors"

	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/cache"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/cache/mcache"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/database/mgo"
	"github.com/openimsdk/open-im-server/v3/pkg/dbbuild"
	"github.com/openimsdk/open-im-server/v3/pkg/rpcli"

	"github.com/openimsdk/open-im-server/v3/pkg/common/config"
	redis2 "github.com/openimsdk/open-im-server/v3/pkg/common/storage/cache/redis"
	"github.com/openimsdk/tools/utils/datautil"
	"github.com/redis/go-redis/v9"

	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"

	"github.com/openimsdk/open-im-server/v3/pkg/authverify"
	"github.com/openimsdk/open-im-server/v3/pkg/common/prommetrics"
	"github.com/openimsdk/open-im-server/v3/pkg/common/servererrs"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/controller"
	pbauth "github.com/openimsdk/open-im-server/v3/protocol/auth"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msggateway"
	"github.com/openimsdk/tools/discovery"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/tokenverify"
	"google.golang.org/grpc"
)

type authServer struct {
	pbauth.UnimplementedAuthServer
	authDatabase   controller.AuthDatabase
	RegisterCenter discovery.Conn
	config         *Config
	userClient     *rpcli.UserClient
	kickGateway    gatewayKickFunc
}

type gatewayKickFunc func(
	ctx context.Context,
	conn grpc.ClientConnInterface,
	req *msggateway.KickUserOfflineReq,
) error

type Config struct {
	RpcConfig   config.Auth
	RedisConfig config.Redis
	MongoConfig config.Mongo
	Share       config.Share
	Discovery   config.Discovery
}

func Start(ctx context.Context, config *Config, client discovery.Conn, server grpc.ServiceRegistrar) error {
	dbb := dbbuild.NewBuilder(&config.MongoConfig, &config.RedisConfig)
	rdb, err := dbb.Redis(ctx)
	if err != nil {
		return err
	}
	var token cache.TokenModel
	if rdb == nil {
		mdb, err := dbb.Mongo(ctx)
		if err != nil {
			return err
		}
		mc, err := mgo.NewCacheMgo(mdb.GetDB())
		if err != nil {
			return err
		}
		token = mcache.NewTokenCacheModel(mc, config.RpcConfig.TokenPolicy.Expire)
	} else {
		token = redis2.NewTokenCacheModel(rdb, config.RpcConfig.TokenPolicy.Expire)
	}
	userConn, err := client.GetConn(ctx, config.Discovery.RpcService.User)
	if err != nil {
		return err
	}
	pbauth.RegisterAuthServer(server, &authServer{
		RegisterCenter: client,
		authDatabase: controller.NewAuthDatabase(
			token,
			config.Share.Secret,
			config.RpcConfig.TokenPolicy.Expire,
			config.Share.MultiLogin,
			config.Share.IMAdminUserID,
		),
		config:     config,
		userClient: rpcli.NewUserClient(userConn),
	})
	return nil
}

func (s *authServer) GetAdminToken(ctx context.Context, req *pbauth.GetAdminTokenReq) (*pbauth.GetAdminTokenResp, error) {
	resp := pbauth.GetAdminTokenResp{}
	if req.Secret != s.config.Share.Secret {
		return nil, errs.ErrNoPermission.WrapMsg("secret invalid")
	}

	if !datautil.Contain(req.UserID, s.config.Share.IMAdminUserID...) {
		return nil, errs.ErrArgs.WrapMsg("userID is error.", "userID", req.UserID, "adminUserID", s.config.Share.IMAdminUserID)

	}

	if err := s.userClient.CheckUser(ctx, []string{req.UserID}); err != nil {
		return nil, err
	}

	token, err := s.authDatabase.CreateToken(ctx, req.UserID, int(constant.AdminPlatformID))
	if err != nil {
		return nil, err
	}

	prommetrics.UserLoginCounter.Inc()

	// 如果提供了RSA公钥，则对token进行加密
	if req.RsaEncryptSecret != "" {
		encryptedToken, err := rsaEncrypt([]byte(token), req.RsaEncryptSecret)
		if err != nil {
			return nil, errs.ErrArgs.WrapMsg("RSA加密失败", "error", err.Error())
		}
		resp.Token = encryptedToken
	} else {
		// 如果没有提供RSA公钥，返回原始token
		resp.Token = token
	}

	resp.ExpireTimeSeconds = s.config.RpcConfig.TokenPolicy.Expire * 24 * 60 * 60
	return &resp, nil
}

// RSAEncrypt 使用RSA公钥加密
func rsaEncrypt(plainText []byte, publicKeyPEM string) (string, error) {
	// 解析公钥
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil || block.Type != "PUBLIC KEY" {
		return "", errors.New("failed to decode PEM block containing public key")
	}

	pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", errors.New(fmt.Sprintf("failed to parse public key: %v", err))
	}

	rsaPublicKey, ok := pubKey.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("not an RSA public key")
	}

	// 加密
	cipherText, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPublicKey, plainText)
	if err != nil {
		return "", err
	}

	// 返回base64编码的密文
	return base64.StdEncoding.EncodeToString(cipherText), nil
}

func (s *authServer) GetUserToken(ctx context.Context, req *pbauth.GetUserTokenReq) (*pbauth.GetUserTokenResp, error) {
	if err := authverify.CheckAdmin(ctx, s.config.Share.IMAdminUserID); err != nil {
		return nil, err
	}

	if req.PlatformID == constant.AdminPlatformID {
		return nil, errs.ErrNoPermission.WrapMsg("platformID invalid. platformID must not be adminPlatformID")
	}

	resp := pbauth.GetUserTokenResp{}

	if authverify.IsManagerUserID(req.UserID, s.config.Share.IMAdminUserID) {
		return nil, errs.ErrNoPermission.WrapMsg("don't get Admin token")
	}
	user, err := s.userClient.GetUserInfo(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	if user.AppMangerLevel >= constant.AppNotificationAdmin {
		return nil, errs.ErrArgs.WrapMsg("app account can`t get token")
	}
	token, err := s.authDatabase.CreateToken(ctx, req.UserID, int(req.PlatformID))
	if err != nil {
		return nil, err
	}
	resp.Token = token
	resp.ExpireTimeSeconds = s.config.RpcConfig.TokenPolicy.Expire * 24 * 60 * 60
	return &resp, nil
}

func (s *authServer) parseToken(ctx context.Context, tokensString string) (claims *tokenverify.Claims, err error) {
	claims, err = tokenverify.GetClaimFromToken(tokensString, authverify.Secret(s.config.Share.Secret))
	if err != nil {
		return nil, err
	}
	isAdmin := authverify.IsManagerUserID(claims.UserID, s.config.Share.IMAdminUserID)
	if isAdmin {
		return claims, nil
	}
	m, err := s.authDatabase.GetTokensWithoutError(ctx, claims.UserID, claims.PlatformID)
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, servererrs.ErrTokenNotExist.Wrap()
	}
	if v, ok := m[tokensString]; ok {
		switch v {
		case constant.NormalToken:
			return claims, nil
		case constant.KickedToken:
			return nil, servererrs.ErrTokenKicked.Wrap()
		default:
			return nil, errs.Wrap(errs.ErrTokenUnknown)
		}
	}
	return nil, servererrs.ErrTokenNotExist.Wrap()
}

func (s *authServer) ParseToken(ctx context.Context, req *pbauth.ParseTokenReq) (resp *pbauth.ParseTokenResp, err error) {
	resp = &pbauth.ParseTokenResp{}
	claims, err := s.parseToken(ctx, req.Token)
	if err != nil {
		return nil, err
	}
	resp.UserID = claims.UserID
	resp.PlatformID = int32(claims.PlatformID)
	resp.ExpireTimeSeconds = claims.ExpiresAt.Unix()
	return resp, nil
}

func (s *authServer) ForceLogout(ctx context.Context, req *pbauth.ForceLogoutReq) (*pbauth.ForceLogoutResp, error) {
	if err := authverify.CheckAdmin(ctx, s.config.Share.IMAdminUserID); err != nil {
		return nil, err
	}
	if err := s.forceKickOff(ctx, req.UserID, req.PlatformID); err != nil {
		return nil, err
	}
	return &pbauth.ForceLogoutResp{}, nil
}

func (s *authServer) BatchForceLogout(ctx context.Context, req *pbauth.BatchForceLogoutReq) (*pbauth.BatchForceLogoutResp, error) {
	if err := authverify.CheckAdmin(ctx, s.config.Share.IMAdminUserID); err != nil {
		return nil, err
	}

	// 批量处理强制登出
	var errs []error
	for _, item := range req.Items {
		if err := s.forceKickOff(ctx, item.UserID, item.PlatformID); err != nil {
			log.ZError(ctx, "BatchForceLogout failed for user", err, "userID", item.UserID, "platformID", item.PlatformID)
			errs = append(errs, err)
		}
	}

	// Process every item, then return the aggregate so callers do not mistake a
	// partial gateway/token-cache failure for a successful force logout.
	if len(errs) > 0 {
		log.ZWarn(ctx, "BatchForceLogout completed with some errors", errors.New("partial failures"), "total_items", len(req.Items), "failed_count", len(errs))
		return nil, errors.Join(errs...)
	}

	return &pbauth.BatchForceLogoutResp{}, nil
}

func (s *authServer) forceKickOff(ctx context.Context, userID string, platformID int32) error {
	var forceLogoutErrs []error

	conns, err := s.RegisterCenter.GetConns(ctx, s.config.Discovery.RpcService.MessageGateway)
	if err != nil {
		forceLogoutErrs = append(forceLogoutErrs, fmt.Errorf("get message gateway connections: %w", err))
	} else {
		kickGateway := s.kickGateway
		if kickGateway == nil {
			kickGateway = func(ctx context.Context, conn grpc.ClientConnInterface, req *msggateway.KickUserOfflineReq) error {
				_, err := msggateway.NewMsgGatewayClient(conn).KickUserOffline(ctx, req)
				return err
			}
		}
		kickReq := &msggateway.KickUserOfflineReq{KickUserIDList: []string{userID}, PlatformID: platformID}
		if err := kickUserOfflineOnGateways(ctx, conns, kickReq, kickGateway); err != nil {
			log.ZError(ctx, "forceKickOff gateway broadcast failed", err,
				"userID", userID, "platformID", platformID)
			forceLogoutErrs = append(forceLogoutErrs, err)
		}
	}

	if err := markPlatformTokensKicked(ctx, s.authDatabase, userID, platformID); err != nil {
		log.ZError(ctx, "forceKickOff token invalidation failed", err,
			"userID", userID, "platformID", platformID)
		forceLogoutErrs = append(forceLogoutErrs, err)
	}

	return errors.Join(forceLogoutErrs...)
}

func kickUserOfflineOnGateways(
	ctx context.Context,
	conns []grpc.ClientConnInterface,
	req *msggateway.KickUserOfflineReq,
	kick gatewayKickFunc,
) error {
	var kickErrs []error
	for i, conn := range conns {
		if err := kick(ctx, conn, req); err != nil {
			kickErrs = append(kickErrs, fmt.Errorf("message gateway %d: %w", i, err))
		}
	}
	return errors.Join(kickErrs...)
}

func markPlatformTokensKicked(
	ctx context.Context,
	database controller.AuthDatabase,
	userID string,
	platformID int32,
) error {
	m, err := database.GetTokensWithoutError(ctx, userID, int(platformID))
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return err
	}
	if len(m) == 0 {
		return nil
	}
	for k := range m {
		m[k] = constant.KickedToken
	}
	return database.SetTokenMapByUidPid(ctx, userID, int(platformID), m)
}

func (s *authServer) InvalidateToken(ctx context.Context, req *pbauth.InvalidateTokenReq) (*pbauth.InvalidateTokenResp, error) {
	m, err := s.authDatabase.GetTokensWithoutError(ctx, req.UserID, int(req.PlatformID))
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	if m == nil {
		return nil, errs.New("token map is empty").Wrap()
	}
	log.ZDebug(ctx, "get token from redis", "userID", req.UserID, "platformID",
		req.PlatformID, "tokenMap", m)

	for k := range m {
		if k != req.GetPreservedToken() {
			m[k] = constant.KickedToken
		}
	}
	log.ZDebug(ctx, "set token map is ", "token map", m, "userID",
		req.UserID, "token", req.GetPreservedToken())
	err = s.authDatabase.SetTokenMapByUidPid(ctx, req.UserID, int(req.PlatformID), m)
	if err != nil {
		return nil, err
	}
	return &pbauth.InvalidateTokenResp{}, nil
}

func (s *authServer) KickTokens(ctx context.Context, req *pbauth.KickTokensReq) (*pbauth.KickTokensResp, error) {
	if err := s.authDatabase.BatchSetTokenMapByUidPid(ctx, req.Tokens); err != nil {
		return nil, err
	}
	return &pbauth.KickTokensResp{}, nil
}
