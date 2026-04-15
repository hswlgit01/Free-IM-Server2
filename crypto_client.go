package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// AES密钥长度要求（32字节 = 256位）
	AESKeySize = 32
)

// 命令行参数
var (
	setKey   = flag.String("setkey", "D4YzKob9zuadTHXlH2m6c3R2CZYiYglhO8Xa6AEIiGs=", "设置AES密钥")
	keysFile = flag.String("keys-file", "/Users/yaron/fsdownload/service_keys.json", "service_keys.json文件路径")
	genKey   = flag.Bool("genkey", false, "生成随机AES密钥")
	help     = flag.Bool("help", false, "显示帮助信息")
)

func main() {
	// 自定义帮助信息
	flag.Usage = showHelp

	// 解析命令行参数
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		fmt.Printf("\n❌ 参数错误: %v\n", err)
		showHelp()
		os.Exit(1)
	}

	// 检查是否只输入了help命令
	if len(os.Args) == 2 && (os.Args[1] == "help" || os.Args[1] == "-help" || os.Args[1] == "--help") {
		showHelp()
		return
	}

	// 如果没有参数，显示帮助信息
	if len(os.Args) == 1 {
		showHelp()
		return
	}

	// 检查是否使用了help标志
	if *help {
		showHelp()
		return
	}

	// 检查是否有未知参数
	if flag.NArg() > 0 {
		fmt.Printf("\n❌ 未知参数: %v\n", flag.Args())
		showHelp()
		os.Exit(1)
	}

	// 处理其他命令
	if *genKey {
		// 生成随机AES密钥
		key, err := generateRandomAESKey()
		if err != nil {
			fmt.Printf("\n❌ 生成AES密钥失败: %v\n", err)
			showHelp()
			os.Exit(1)
		}
		fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("✅ 生成的AES密钥 (Base64):\n\n%s\n\n", key)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		return
	}

	if *setKey == "" {
		fmt.Println("\n❌ 错误: 未指定AES密钥\n")
		showHelp()
		os.Exit(1)
	}

	// 从service_keys.json读取RSA公钥并加密AES密钥
	err := encryptAndUpdateKeysFile(*setKey, *keysFile)
	if err != nil {
		fmt.Printf("\n❌ 加密并更新密钥文件失败: %v\n", err)
		showHelp()
		os.Exit(1)
	}
	fmt.Println("\n✅ AES密钥已成功加密并更新到密钥文件\n")
}

// showHelp 显示帮助信息
func showHelp() {
	fmt.Println("\n━━━━━━━━━━━━━━ 密钥管理工具 ━━━━━━━━━━━━━━")
	fmt.Println("这是一个用于管理AES密钥的工具，支持以下功能：\n")

	fmt.Println("【1】生成随机AES密钥")
	fmt.Println("    命令: ./crypto_client -genkey")
	fmt.Println("    说明: 生成一个32字节(256位)的随机AES密钥，以Base64格式输出\n")

	fmt.Println("【2】加密并保存AES密钥")
	fmt.Println("    命令: ./crypto_client -setkey <AES密钥> [-keys-file <密钥文件路径>]")
	fmt.Println("    说明: 使用service_keys.json中的RSA公钥加密AES密钥，并更新到文件中")
	fmt.Println("    参数:")
	fmt.Println("      -setkey:    AES密钥（Base64格式）")
	fmt.Println("      -keys-file: service_keys.json文件路径")
	fmt.Println("                  (默认: _output/bin/platforms/darwin/arm64/keys/service_keys.json)\n")

	fmt.Println("【3】帮助信息")
	fmt.Println("    命令: ./crypto_client help")
	fmt.Println("    或:   ./crypto_client -help")
	fmt.Println("    或:   ./crypto_client --help\n")

	fmt.Println("【使用示例】")
	fmt.Println("1. 生成新的AES密钥:")
	fmt.Println("   $ ./crypto_client -genkey\n")

	fmt.Println("2. 使用生成的密钥:")
	fmt.Println("   $ ./crypto_client -setkey <生成的密钥>\n")

	fmt.Println("3. 指定密钥文件路径:")
	fmt.Println("   $ ./crypto_client -setkey <密钥> -keys-file /path/to/service_keys.json\n")

	fmt.Println("【注意事项】")
	fmt.Println("• AES密钥必须是32字节(256位)，以Base64格式编码")
	fmt.Println("• service_keys.json文件必须包含有效的RSA公钥")
	fmt.Println("• 密钥文件路径必须指向有效的service_keys.json文件")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}

// encryptAndUpdateKeysFile 从service_keys.json读取RSA公钥，加密AES密钥并更新文件
func encryptAndUpdateKeysFile(aesKeyBase64 string, keysFilePath string) error {
	// 读取service_keys.json文件
	fileContent, err := os.ReadFile(keysFilePath)
	if err != nil {
		return fmt.Errorf("读取密钥文件失败: %v", err)
	}

	// 解析JSON
	var keysMap map[string]string
	if err := json.Unmarshal(fileContent, &keysMap); err != nil {
		return fmt.Errorf("解析密钥文件失败: %v", err)
	}

	if len(keysMap) == 0 {
		return fmt.Errorf("密钥文件中没有RSA公钥")
	}

	// 解码AES密钥
	aesKey, err := base64.StdEncoding.DecodeString(aesKeyBase64)
	if err != nil {
		return fmt.Errorf("解码AES密钥失败: %v", err)
	}

	if len(aesKey) != 32 {
		return fmt.Errorf("AES密钥长度错误: 期望 32 字节，实际 %d 字节", len(aesKey))
	}

	// 遍历所有RSA公钥并加密AES密钥
	updated := false
	for pubKey, _ := range keysMap {
		// 使用RSA公钥加密AES密钥
		encryptedKey, err := RSAEncrypt(aesKey, pubKey)
		if err != nil {
			fmt.Printf("\n⚠️  警告: 使用公钥加密失败: %v\n", err)
			continue
		}

		// 更新密钥映射
		keysMap[pubKey] = encryptedKey
		updated = true
	}

	if !updated {
		return fmt.Errorf("没有成功加密任何密钥")
	}

	// 将更新后的映射写回文件
	updatedContent, err := json.MarshalIndent(keysMap, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化密钥信息失败: %v", err)
	}

	if err := os.WriteFile(keysFilePath, updatedContent, 0600); err != nil {
		return fmt.Errorf("写入密钥文件失败: %v", err)
	}

	return nil
}

// RSAEncrypt 使用RSA公钥加密
func RSAEncrypt(plainText []byte, publicKeyPEM string) (string, error) {
	var publicKeyBytes []byte

	// 检查公钥是否已经是PEM格式
	if strings.Contains(publicKeyPEM, "-----BEGIN PUBLIC KEY-----") {
		publicKeyBytes = []byte(publicKeyPEM)
	} else {
		// 尝试解码Base64
		var err error
		publicKeyBytes, err = base64.StdEncoding.DecodeString(publicKeyPEM)
		if err != nil {
			return "", fmt.Errorf("解码公钥失败: %v", err)
		}
	}

	// 解析公钥
	block, _ := pem.Decode(publicKeyBytes)
	if block == nil {
		return "", errors.New("无法解析PEM格式的公钥")
	}

	// 解析公钥数据
	var pubKey interface{}
	var err error

	switch block.Type {
	case "PUBLIC KEY":
		pubKey, err = x509.ParsePKIXPublicKey(block.Bytes)
	case "RSA PUBLIC KEY":
		pubKey, err = x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return "", fmt.Errorf("不支持的PEM块类型: %s", block.Type)
	}

	if err != nil {
		return "", fmt.Errorf("解析公钥失败: %v", err)
	}

	// 转换为RSA公钥
	rsaPublicKey, ok := pubKey.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("不是有效的RSA公钥")
	}

	// 加密
	cipherText, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPublicKey, plainText)
	if err != nil {
		return "", fmt.Errorf("RSA加密失败: %v", err)
	}

	// 返回base64编码的密文
	return base64.StdEncoding.EncodeToString(cipherText), nil
}

// generateRandomAESKey 生成随机AES密钥（32字节，返回Base64编码）
func generateRandomAESKey() (string, error) {
	key := make([]byte, 32) // AES-256
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", fmt.Errorf("生成随机密钥失败: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}
