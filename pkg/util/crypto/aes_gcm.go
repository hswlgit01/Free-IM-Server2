package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

// AESKeySize 定义了 AES-256 的密钥长度要求（32字节）
const AESKeySize = 32

// NonceSize 定义了 AES-GCM 的随机数大小（12字节是标准大小）
const NonceSize = 12 // AES-GCM 标准 nonce 大小是 12 字节

// EncryptionTypeAES256GCM 定义了此加密类型的字符串标识符
const EncryptionTypeAES256GCM = "AES-256-GCM"

// GetFixedKey 返回当前设置的密钥
// 为了兼容性保留此函数名，实际调用GetAESKey
func GetFixedKey() []byte {
	key := GetAESKey()
	if key == "" {
		//log.Printf("===== AES 消息加密秘钥未设置 =====")
	}
	return []byte(key)
}

// Encrypt 使用 AES-GCM 算法和提供的密钥对明文进行加密
// 如果密钥为nil或长度不正确，则返回原始内容不加密
func Encrypt(plaintext []byte, key []byte) (string, error) {
	// 如果密钥为nil或密钥长度不正确，表示不进行加密
	if key == nil || len(key) != AESKeySize {
		return string(plaintext), nil
	}

	// 创建 AES 加密块
	blk, err := aes.NewCipher(key)
	if err != nil {
		return "", errors.New("failed to create AES: " + err.Error())
	}

	// 使用 GCM 模式，它提供认证加密
	aesgcm, err := cipher.NewGCM(blk)
	if err != nil {
		return "", errors.New("failed to create GCM: " + err.Error())
	}

	// 生成随机 nonce
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", errors.New("failed to generate nonce: " + err.Error())
	}

	// 加密数据（包含认证标签）
	ciphertext := aesgcm.Seal(nil, nonce, plaintext, nil)
	// 将 nonce 加到密文前面
	nonceAndCiphertext := append(nonce, ciphertext...)

	// 返回 base64 编码的完整加密数据
	return base64.StdEncoding.EncodeToString(nonceAndCiphertext), nil
}

// Decrypt 使用 AES-GCM 算法和提供的密钥解密 base64 编码的 (nonce + 密文) 字符串
// 如果密钥为nil或长度不正确，则返回原始密文
func Decrypt(base64Ciphertext string, key []byte) ([]byte, error) {
	// 如果密钥为nil或密钥长度不正确，表示不进行解密
	if key == nil || len(key) != AESKeySize {
		return []byte(base64Ciphertext), nil
	}

	// 解码 base64 字符串
	nonceAndCiphertext, err := base64.StdEncoding.DecodeString(base64Ciphertext)
	if err != nil {
		return nil, errors.New("base64 decoding failed")
	}

	// 确保数据长度足够包含 nonce
	if len(nonceAndCiphertext) < NonceSize {
		return nil, errors.New("ciphertext too short")
	}

	// 提取 nonce 和实际密文
	nonce := nonceAndCiphertext[:NonceSize]
	ciphertext := nonceAndCiphertext[NonceSize:]

	// 创建 AES 解密块
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("failed to create AES")
	}

	// 使用 GCM 模式进行解密并验证完整性
	aesgcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, errors.New("failed to create GCM")
	}

	// 执行解密，如果认证标签验证失败，会返回错误
	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("decryption failed")
	}

	return plaintext, nil
}
