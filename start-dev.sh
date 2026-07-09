#!/bin/bash

# WeKnora 开发环境快速启动脚本

set -e

echo "🚀 启动 WeKnora 开发环境..."

# 检查必要工具
if [ ! -f "$HOME/.local/bin/air" ]; then
    echo "❌ Air 未安装，请先运行: install-air.sh"
    exit 1
fi

if [ ! -f "$HOME/.local/go/bin/go" ]; then
    echo "❌ Go 未安装"
    exit 1
fi

# 添加环境变量
export PATH="$HOME/.local/bin:$HOME/.local/go/bin:$PATH"

# 启动基础设施（如果未运行）
echo "📦 检查 Docker 容器..."
docker ps | grep -q postgres || {
    echo "启动 PostgreSQL..."
    docker run -d --name postgres -p 5432:5432 \
        -e POSTGRES_DB=weknora \
        -e POSTGRES_USER=weknora \
        -e POSTGRES_PASSWORD=weknora123 \
        postgres:15
}

docker ps | grep -q redis || {
    echo "启动 Redis..."
    docker run -d --name redis -p 6379:6379 redis:7
}

docker ps | grep -q minio || {
    echo "启动 MinIO..."
    docker run -d --name minio -p 9000:9000 -p 9001:9001 \
        -e MINIO_ROOT_USER=minioadmin \
        -e MINIO_ROOT_PASSWORD=minioadmin \
        minio/minio server /data --console-address ":9001"
}

# 启动后端（带热重载）
echo "🔧 启动后端服务（Air 热重载模式）..."
cd backend
air &
BACKEND_PID=$!

# 启动前端
echo "🎨 启动前端服务（Vite 热重载）..."
cd ../frontend
npm run dev &
FRONTEND_PID=$!

echo ""
echo "✅ 开发环境已启动！"
echo ""
echo "📍 访问地址："
echo "   前端: http://localhost:5173"
echo "   后端: http://localhost:8080"
echo ""
echo "🔄 热重载已启用："
echo "   - 修改 Go 代码 → 自动重启后端"
echo "   - 修改前端代码 → 浏览器自动刷新"
echo ""
echo "按 Ctrl+C 停止所有服务"

# 等待用户中断
trap "echo '🛑 停止服务...'; kill $BACKEND_PID $FRONTEND_PID 2>/dev/null; exit 0" INT
wait
