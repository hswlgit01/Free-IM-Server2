#!/bin/bash

# 定义颜色
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

# 项目根目录
PROJECT_ROOT=$(dirname "$(readlink -f "$0")")
DOCKER_FILE="$PROJECT_ROOT/Dockerfile.build"

echo -e "${BLUE}Building Free-IM-Server...${NC}"

# 检查Docker是否安装
if ! command -v docker &> /dev/null; then
    echo -e "${RED}Docker is not installed. Please install Docker first.${NC}"
    exit 1
fi

# 创建输出目录
mkdir -p "$PROJECT_ROOT/_output/bin"

# 构建Docker镜像
echo -e "${BLUE}Building Docker image...${NC}"
docker build -t free-im-server-builder -f "$DOCKER_FILE" "$PROJECT_ROOT"

# 在Docker中编译项目
echo -e "${GREEN}Compiling Free-IM-Server...${NC}"
docker run --rm \
    -v "$PROJECT_ROOT:/build/src" \
    -w /build/src \
    free-im-server-builder \
    bash -c "go build -o _output/bin/openim-server cmd/main.go"

# 检查编译结果
if [ -f "$PROJECT_ROOT/_output/bin/openim-server" ]; then
    echo -e "${GREEN}Build successful!${NC}"
    echo -e "${GREEN}Output: ${PROJECT_ROOT}/_output/bin/openim-server${NC}"
else
    echo -e "${RED}Build failed!${NC}"
    exit 1
fi