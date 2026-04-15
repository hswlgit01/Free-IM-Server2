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

package crypto

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// 密钥存储的目录和文件名
const (
	KeysDir       = "keys"
	KeysFileName  = "service_keys.json"
	MsgServiceKey = "openim-rpc-msg"
	TransferKey   = "openim-msgtransfer"
)

// 全局变量定义
var (
	// 全局AES密钥，仅存在于内存中
	aesKey string

	// 内存中存储RSA密钥对(PEM格式)
	rsaPrivateKeyPEM string
	rsaPublicKeyPEM  string

	// 互斥锁保护对密钥的并发访问
	keyMutex sync.RWMutex
)

// GenerateRSAKeyPair 生成RSA密钥对并将公钥和私钥都保存在内存中
// 同时将公钥写入文件系统
func GenerateRSAKeyPair(serviceName string) (string, error) {
	// 生成2048位的RSA密钥对
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", fmt.Errorf("生成RSA密钥对失败: %v", err)
	}

	// 转换私钥为PKCS1格式
	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	})
	if privateKeyPEM == nil {
		return "", fmt.Errorf("编码私钥失败")
	}

	// 转换公钥为PKIX格式
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", fmt.Errorf("编码公钥失败: %v", err)
	}
	publicKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	})
	if publicKeyPEM == nil {
		return "", fmt.Errorf("编码公钥PEM失败")
	}

	// 将密钥保存在内存中
	keyMutex.Lock()
	rsaPrivateKeyPEM = string(privateKeyPEM)
	rsaPublicKeyPEM = string(publicKeyPEM)
	keyMutex.Unlock()

	// 将公钥写入文件
	if err := savePublicKeyToFile(string(publicKeyPEM)); err != nil {
		// 如果保存失败，清除内存中的密钥
		keyMutex.Lock()
		rsaPrivateKeyPEM = ""
		rsaPublicKeyPEM = ""
		keyMutex.Unlock()
		return "", fmt.Errorf("保存公钥到文件失败: %v", err)
	}

	return string(publicKeyPEM), nil
}

// GetAESKey 从内存中获取AES密钥
// 如果内存中没有密钥，则尝试从文件中获取加密的AES密钥并解密
func GetAESKey() string {
	keyMutex.RLock()
	currentKey := aesKey
	keyMutex.RUnlock()

	if currentKey != "" {
		return currentKey
	}

	// 尝试从文件中获取并解密密钥
	keysMap, err := LoadKeysFromFile()
	if err != nil {
		log.Printf("加载密钥文件失败: %v", err)
		return ""
	}

	if len(keysMap) == 0 {
		return ""
	}

	keyMutex.RLock()
	privKeyPEM := rsaPrivateKeyPEM
	currentPubKeyPEM := rsaPublicKeyPEM
	keyMutex.RUnlock()

	if privKeyPEM == "" || currentPubKeyPEM == "" {
		log.Printf("RSA密钥对不存在，无法解密AES密钥")
		return ""
	}

	// 解析私钥
	block, _ := pem.Decode([]byte(privKeyPEM))
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		log.Printf("无效的RSA私钥PEM格式")
		return ""
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		log.Printf("解析私钥失败: %v", err)
		return ""
	}

	// 查找当前服务的公钥对应的加密AES密钥
	encryptedAESKey, exists := keysMap[currentPubKeyPEM]
	if !exists || encryptedAESKey == "" {
		return ""
	}

	// 解码并解密AES密钥
	encryptedKeyBytes, err := base64.StdEncoding.DecodeString(encryptedAESKey)
	if err != nil {
		log.Printf("解码加密AES密钥失败: %v", err)
		return ""
	}

	decryptedKeyBytes, err := rsa.DecryptPKCS1v15(rand.Reader, privateKey, encryptedKeyBytes)
	if err != nil {
		log.Printf("解密AES密钥失败: %v", err)
		return ""
	}

	// 解密成功，保存到内存
	keyMutex.Lock()
	aesKey = string(decryptedKeyBytes)
	keyMutex.Unlock()

	return aesKey
}

// savePublicKeyToFile 将公钥保存到文件系统
func savePublicKeyToFile(publicKeyPEM string) error {
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("获取工作目录失败: %v", err)
	}

	keysDir := filepath.Join(workDir, KeysDir)
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return fmt.Errorf("创建密钥目录失败: %v", err)
	}

	keysFile := filepath.Join(keysDir, KeysFileName)

	// 读取现有的密钥文件
	var keysMap map[string]string
	if _, err := os.Stat(keysFile); os.IsNotExist(err) {
		keysMap = make(map[string]string)
	} else {
		fileContent, err := os.ReadFile(keysFile)
		if err != nil {
			return fmt.Errorf("读取密钥文件失败: %v", err)
		}

		if len(fileContent) == 0 {
			keysMap = make(map[string]string)
		} else {
			if err := json.Unmarshal(fileContent, &keysMap); err != nil {
				return fmt.Errorf("解析密钥文件失败: %v", err)
			}
		}
	}

	// 添加新的公钥
	keysMap[publicKeyPEM] = ""

	// 将更新后的映射写回文件
	updatedContent, err := json.MarshalIndent(keysMap, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化密钥信息失败: %v", err)
	}

	if err := os.WriteFile(keysFile, updatedContent, 0600); err != nil {
		return fmt.Errorf("写入密钥文件失败: %v", err)
	}

	return nil
}

// LoadKeysFromFile 从文件中加载密钥信息
func LoadKeysFromFile() (map[string]string, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("获取工作目录失败: %v", err)
	}

	keysFile := filepath.Join(workDir, KeysDir, KeysFileName)

	if _, err := os.Stat(keysFile); os.IsNotExist(err) {
		return make(map[string]string), nil
	}

	fileContent, err := os.ReadFile(keysFile)
	if err != nil {
		return nil, fmt.Errorf("读取密钥文件失败: %v", err)
	}

	if len(fileContent) == 0 {
		return make(map[string]string), nil
	}

	var keysMap map[string]string
	if err := json.Unmarshal(fileContent, &keysMap); err != nil {
		return nil, fmt.Errorf("解析密钥文件失败: %v", err)
	}

	return keysMap, nil
}
