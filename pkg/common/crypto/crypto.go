package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// GenerateAESKey 生成一个新的AES密钥
func GenerateAESKey() (string, error) {
	key := make([]byte, 32) // AES-256
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// AESEncrypt 使用AES-256-GCM模式加密
func AESEncrypt(plainText []byte, keyBase64 string) (string, error) {
	// 解码base64密钥
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return "", errors.New(fmt.Sprintf("base64 decode key error: %v", err))
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

	// 加密
	cipherText := aesGCM.Seal(nonce, nonce, plainText, nil)

	// 返回base64编码的密文
	return base64.StdEncoding.EncodeToString(cipherText), nil
}

// AESDecrypt 使用AES-256-GCM模式解密
func AESDecrypt(ciphertextBase64 string, keyBase64 string) ([]byte, error) {
	// 解码base64密钥
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("base64 decode key error: %v", err))
	}

	// 解码base64密文
	cipherText, err := base64.StdEncoding.DecodeString(ciphertextBase64)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("base64 decode cipher text error: %v", err))
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
	if len(cipherText) < aesGCM.NonceSize() {
		return nil, errors.New("cipher text too short")
	}

	// 从密文中提取nonce
	nonce, cipherText := cipherText[:aesGCM.NonceSize()], cipherText[aesGCM.NonceSize():]

	// 解密
	plainText, err := aesGCM.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return nil, err
	}

	return plainText, nil
}
