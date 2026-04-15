# Yep-IM-Server

## 环境及组件要求
   对于服务器环境、系统、以及所依赖组件的说明可以参考
   https://doc.rentsoft.cn/zh-hans/guides/gettingstarted/env-comp

## 本地部署 Yep IM Server

### 部署大致流程
1. 启动Im依赖组件: mongo/redis/kafka/minio
2. 启动Im服务

### 开始部署

#### 1.拉取IM代码
```bash
git clone git@github.com:Map-finance/Yep-IM-Server.git && cd Yep-IM-Server
```

#### 2.部署依赖组件 (mongodb/redis/kafka/MinIO)
```bash
docker compose up -d
```

如需启动运维组件(prometheus/alertmanager/grafana)
```bash
docker compose --profile m up -d
```

监控告警使用参考：https://doc.rentsoft.cn/zh-Hans/guides/gettingStarted/admin

#### 3.启动IM服务
##### ①设置miniIO IP
修改 config/minio.yml 中的 externalAddress
```yml
# minio对象存储相关设置
# MinIO 服务器的外部地址，通常用于外部访问。支持 HTTP 和 HTTPS，可以使用域名。
# externalAddress为 http://外网IP:port 或 域名
externalAddress: http://external_ip:10005
```

##### ②执行bootstrap
第一次编译前，linux/mac 平台下执行：
```bash
bash bootstrap.sh
```
windows 执行
```bash
bootstrap.bat
```

##### ③中国境内建议设置go代理
```bash
go env -w GO111MODULE=on
go env -w GOPROXY=https://goproxy.cn,direct
```

##### ④🛠️ 编译(linux/windows/mac 平台均可用)
```bash
mage
```


#####  ⑤🚀 启动/停止/检测(linux/windows/mac 平台均可用)
**前台启动**
```bash
mage start
```
**或 后台启动 收集日志**
```bash
nohup mage start >> _output/logs/openim.log 2>&1 &
```
**停止**
```bash
mage stop
```
**检测**
```bash
mage check
```

## 配置项说明文档
https://doc.rentsoft.cn/zh-hans/restapi/commonconfigs

## Api说明文档
https://doc.rentsoft.cn/zh-Hans/restapi/apis/introduction
