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

package msggateway

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"errors"
	"io"

	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/tools/mcontext"

	"github.com/openimsdk/open-im-server/v3/pkg/rpcli"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
)

type Encoder interface {
	Encode(data Resp) ([]byte, error)
	Decode(encodeData []byte, decodeData *Req) error
}

type GobEncoder struct{}

func NewGobEncoder() Encoder {
	return GobEncoder{}
}

func (g GobEncoder) Encode(data Resp) ([]byte, error) {
	var buff bytes.Buffer
	enc := gob.NewEncoder(&buff)
	if err := enc.Encode(data); err != nil {
		return nil, errs.WrapMsg(err, "GobEncoder.Encode failed", "action", "encode")
	}
	return buff.Bytes(), nil
}

func (g GobEncoder) Decode(encodeData []byte, decodeData *Req) error {
	buff := bytes.NewBuffer(encodeData)
	dec := gob.NewDecoder(buff)
	if err := dec.Decode(decodeData); err != nil {
		return errs.WrapMsg(err, "GobEncoder.Decode failed", "action", "decode")
	}
	return nil
}

// AESGobEncoder 实现基于AES加密的Gob编码器
type AESGobEncoder struct {
	originalEncoder Encoder           // 保留原始编码器功能
	userClient      *rpcli.UserClient // 用户密钥客户端
	client          *Client           // 用户ID
}

// NewAESGobEncoder 创建一个新的AES加密Gob编码器
func NewAESGobEncoder(userClient *rpcli.UserClient, client *Client) Encoder {
	return &AESGobEncoder{
		originalEncoder: GobEncoder{},
		userClient:      userClient,
		client:          client,
	}
}

// Encode 先使用Gob序列化，然后进行AES加密
func (e *AESGobEncoder) Encode(data Resp) ([]byte, error) {
	ctx := mcontext.WithMustInfoCtx(
		[]string{data.OperationID, e.client.UserID, constant.PlatformIDToName(e.client.PlatformID), e.client.ctx.GetConnID()},
	)
	// 先用原始编码器序列化
	plainData, err := e.originalEncoder.Encode(data)
	if err != nil {
		return nil, errs.WrapMsg(err, "AESGobEncoder.Encode failed", "action", "encode")
	}

	// 加密序列化后的数据
	cipherTextBase64, err := e.encrypt(ctx, plainData, e.client.UserID)
	if err != nil {
		return nil, errs.WrapMsg(err, "AESGobEncoder.Encode failed", "action", "encrypt")
	}

	// 返回base64解码后的结果
	cipherText, err := base64.StdEncoding.DecodeString(cipherTextBase64)
	if err != nil {
		return nil, errs.WrapMsg(err, "AESGobEncoder.Encode failed", "action", "base64 decode")
	}

	return cipherText, nil
}

// Decode 先进行AES解密，然后使用Gob反序列化
func (e *AESGobEncoder) Decode(encodeData []byte, decodeData *Req) error {
	ctx := mcontext.WithMustInfoCtx(
		[]string{decodeData.OperationID, e.client.UserID, constant.PlatformIDToName(e.client.PlatformID), e.client.ctx.GetConnID()},
	)
	// 将密文转为base64编码
	cipherTextBase64 := base64.StdEncoding.EncodeToString(encodeData)

	// 解密数据
	decryptedData, err := e.decrypt(ctx, cipherTextBase64, e.client.UserID)
	if err != nil {
		return errs.WrapMsg(err, "AESGobEncoder.Decode failed", "action", "decrypt")
	}
	// 用原始解码器反序列化
	return e.originalEncoder.Decode(decryptedData, decodeData)
}

// 从UserKeys获取AES密钥
func (e *AESGobEncoder) getUserAESKey(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return getDefaultAESKeyBase64(), nil
	}
	// 检查userKeysClient是否已初始化
	if e.userClient == nil {
		log.ZWarn(ctx, "userKeysClient未初始化，使用默认密钥", nil, "userID", userID)
		return getDefaultAESKeyBase64(), nil
	}
	// 获取用户密钥
	aesKey, err := e.userClient.GetUserAESKey(ctx, userID)
	if err != nil {
		log.ZWarn(ctx, "获取用户密钥失败，使用默认密钥", err, "userID", userID)
		return getDefaultAESKeyBase64(), nil
	}

	// 检查用户是否有自定义的AES密钥
	if aesKey != "" {
		return aesKey, nil
	}

	// 如果用户没有自定义密钥，使用默认密钥
	log.ZWarn(ctx, "用户密钥均为空，使用默认密钥", nil, "userID", userID)
	return getDefaultAESKeyBase64(), nil
}

// encrypt 使用AES-256-GCM加密数据，根据userID获取密钥
func (e *AESGobEncoder) encrypt(ctx context.Context, plainText []byte, userID string) (string, error) {
	// 获取用户的AES密钥
	keyBase64, err := e.getUserAESKey(ctx, userID)
	// log.ZDebug(ctx, "加密处理", "userID", userID, "keyLength", len(keyBase64), "success", err == nil)
	if err != nil {
		return "", errs.WrapMsg(err, "get user AES key error")
	}

	// 解码base64密钥
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return "", errs.WrapMsg(err, "base64 decode key error")
	}

	// 创建AES加密块
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	// 创建GCM模式
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// 创建随机nonce
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	cipherText := aesGCM.Seal(nonce, nonce, plainText, nil)

	// 返回base64编码的密文
	return base64.StdEncoding.EncodeToString(cipherText), nil
}

// decrypt 使用AES-256-GCM解密数据，根据userID获取密钥
func (e *AESGobEncoder) decrypt(ctx context.Context, ciphertextBase64 string, userID string) ([]byte, error) {
	// 获取用户的AES密钥
	keyBase64, err := e.getUserAESKey(ctx, userID)
	//log.ZDebug(ctx, "解密处理", "userID", userID, "keyLength", len(keyBase64), "success", err == nil)
	if err != nil {
		return nil, errs.WrapMsg(err, "get user AES key error")
	}

	// 解码base64密钥
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return nil, errs.WrapMsg(err, "base64 decode key error")
	}

	// 解码base64密文
	cipherText, err := base64.StdEncoding.DecodeString(ciphertextBase64)
	if err != nil {
		return nil, errs.WrapMsg(err, "base64 decode cipher text error")
	}

	// 创建AES加密块
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// 创建GCM模式
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// 检查密文长度是否足够
	nonceSize := aesGCM.NonceSize()
	if len(cipherText) < nonceSize {
		return nil, errors.New("密文太短")
	}

	// 从密文中提取nonce
	nonce, cipherData := cipherText[:nonceSize], cipherText[nonceSize:]

	// 解密
	plaintext, err := aesGCM.Open(nil, nonce, cipherData, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// getDefaultAESKeyBase64 获取默认AES密钥
func getDefaultAESKeyBase64() string {
	// 固定的AES-256密钥用于备用 (base64编码)
	// 注意：实际生产环境中应该使用安全的密钥管理系统
	return "fBk8TQvcfynYJGlhaQ53BfeqaBplANITC7gRMdrs6Gs="
}

type JsonEncoder struct{}

func NewJsonEncoder() Encoder {
	return JsonEncoder{}
}

func (g JsonEncoder) Encode(data Resp) ([]byte, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return nil, errs.New("JsonEncoder.Encode failed", "action", "encode")
	}
	return b, nil
}

func (g JsonEncoder) Decode(encodeData []byte, decodeData *Req) error {
	err := json.Unmarshal(encodeData, decodeData)
	if err != nil {
		return errs.New("JsonEncoder.Decode failed", "action", "decode")
	}
	return nil
}
