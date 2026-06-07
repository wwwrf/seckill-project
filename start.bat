@echo off
chcp 65001 >nul
echo ================================================
echo 启动秒杀系统
echo ================================================
echo.

cd /d %~dp0

echo [1/3] 检查配置文件...
if not exist "config\local.yaml" (
    echo [错误] 配置文件 config\local.yaml 不存在
    pause
    exit /b 1
)
echo [√] 配置文件检查完成

echo.
echo [2/3] 下载依赖...
go mod download
if %errorlevel% neq 0 (
    echo [错误] 依赖下载失败
    pause
    exit /b 1
)
echo [√] 依赖下载完成

echo.
echo [3/3] 启动服务...
echo ================================================
echo 服务启动中，请稍候...
echo 启动成功后可以访问: http://localhost:8088/ping
echo 按 Ctrl+C 停止服务
echo ================================================
echo.

go run cmd\api\main.go

pause
