#!/bin/bash

# DEP-IM-SERVER 后端服务构建脚本
# 使用方法: sh build.sh v1.0.0

# 配置
ORG="freechatim"
REPO="freechat-server-end"

# 获取版本号参数
VERSION=$1

# 检查版本号是否提供
if [ -z "$VERSION" ]; then
    echo "❌ 错误：必须指定版本号"
    echo ""
    echo "使用方法："
    echo "  sh build.sh v1.0.0"
    echo "  sh build.sh v2.1.0"
    echo "  sh build.sh latest"
    echo ""
    echo "示例："
    echo "  sh build.sh v1.0.0"
    exit 1
fi

# 完整镜像名
FULL_NAME="$ORG/$REPO:$VERSION"

echo "🚀 开始构建 freechat-server-end 后端镜像..."
echo "📦 镜像名称: $FULL_NAME"
echo ""

echo "🔄 拉取最新代码..."
if git pull; then
    echo "✅ 代码更新成功"
else
    echo "❌ 代码更新失败"
    exit 1
fi

echo ""

echo "🧹 清理之前的构建产物..."
if [ -d "_output" ]; then
    rm -rf _output
    echo "✅ 清理 _output 目录完成"
fi

echo ""

echo "🔍 检查 Go 环境..."

# 跳过 GVM 加载，直接检查常见路径
GO_FOUND=""

# 首先检查当前 PATH 中是否有 go
if command -v go >/dev/null 2>&1; then
    GO_FOUND=$(which go)
    echo "✅ Go 已在 PATH 中: $GO_FOUND"
else
    echo "🔧 在 PATH 中未找到 Go，搜索常见安装位置..."
    
    # 逐个检查常见路径
    if [ -f "/root/.gvm/gos/go1.22.7/bin/go" ]; then
        GO_FOUND="/root/.gvm/gos/go1.22.7/bin/go"
    elif [ -f "$HOME/.gvm/gos/go1.22.7/bin/go" ]; then
        GO_FOUND="$HOME/.gvm/gos/go1.22.7/bin/go"
    elif [ -f "/usr/local/go/bin/go" ]; then
        GO_FOUND="/usr/local/go/bin/go"
    elif [ -f "/opt/go/bin/go" ]; then
        GO_FOUND="/opt/go/bin/go"
    fi
    
    if [ -z "$GO_FOUND" ]; then
        echo "❌ Go 未找到在任何常见路径中"
        echo "💡 请手动设置 Go 路径："
        echo "   export PATH=/path/to/go/bin:\$PATH"
        echo "   或者使用: bash build.sh v1.0.0 (强制使用 bash)"
        exit 1
    else
        echo "✅ 找到 Go: $GO_FOUND"
        # 添加 Go 路径到 PATH
        GO_DIR=$(dirname "$GO_FOUND")
        export PATH="$GO_DIR:$PATH"
    fi
fi

echo "📋 Go 版本: $(go version)"
echo "📍 Go 路径: $(which go)"

echo ""

echo "📦 下载 Go 模块依赖..."
if go mod download; then
    echo "✅ Go 模块下载成功"
else
    echo "❌ Go 模块下载失败"
    exit 1
fi

echo ""

echo "🔨 构建镜像: $FULL_NAME"
echo "📝 使用多阶段构建 (Go + Alpine)..."
if docker build -t $FULL_NAME .; then
    echo "✅ 镜像构建成功"
else
    echo "❌ 镜像构建失败"
    exit 1
fi

echo ""

echo "🔍 检查镜像大小..."
docker images $FULL_NAME --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}"

echo ""

echo "📤 推送镜像: $FULL_NAME"
if docker push $FULL_NAME; then
    echo "✅ 镜像推送成功"
else
    echo "❌ 镜像推送失败"
    exit 1
fi

echo ""
echo "🎉 构建完成！"
echo "📋 镜像信息:"
echo "   组织: $ORG"
echo "   仓库: $REPO"
echo "   版本: $VERSION"
echo "   完整名称: $FULL_NAME"
echo ""
echo "🐳 拉取命令: docker pull $FULL_NAME"
echo "🚀 运行命令: docker run -p 10001:10001 -p 10002:10002 --name freechat-server $FULL_NAME"
echo "🔧 带配置运行: docker run -p 10001:10001 -p 10002:10002 -v ./config:/im-server/config --name freechat-server $FULL_NAME"