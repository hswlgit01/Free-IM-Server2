package crypto

import (
	"fmt"
	"log"
	"os"
)

// InitServiceKey 初始化服务密钥
// 生成RSA密钥对并将其保存在内存中，同时将公钥写入文件系统
func InitServiceKey(serviceName string) error {
	// 获取进程ID，便于日志追踪
	pid := os.Getpid()

	log.Printf("[密钥服务] 进程 %s (PID: %d) 正在初始化RSA密钥对\n", serviceName, pid)

	// 生成RSA密钥对
	_, err := GenerateRSAKeyPair(serviceName)
	if err != nil {
		return fmt.Errorf("生成RSA密钥对失败: %v", err)
	}

	log.Printf("[密钥服务] 进程 %s (PID: %d) RSA密钥对已生成，公钥已写入文件\n", serviceName, pid)

	return nil
}
