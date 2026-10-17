package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/memories-cn/user-management-api/internal/bootstrap"
)

const (
	// healthcheckPath 是就绪检查路径。
	healthcheckPath = "/health/ready"
	// healthcheckTimeout 是探针单次请求的超时。
	healthcheckTimeout = 5 * time.Second
)

func main() {
	// -healthcheck 供容器探针使用：运行镜像是 distroless，
	// 没有 shell 也没有 curl，无法在 compose 里写 CMD 探针，
	// 因此由二进制自己提供这个子命令。
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(runHealthcheck())
	}

	_, err := bootstrap.Build()
	if err != nil {
		slog.Error("service failed to start", "error", err)
		os.Exit(1)
	}
}

// runHealthcheck 请求本地就绪检查端点，返回进程退出码。
func runHealthcheck() int {
	addr := os.Getenv("APP_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}

	url := "http://" + host + healthcheckPath
	client := &http.Client{Timeout: healthcheckTimeout}

	resp, err := client.Get(url)
	if err != nil {
		slog.Error("healthcheck request failed", "error", err, "url", url)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("healthcheck not ready", "status", resp.StatusCode)
		return 1
	}
	return 0
}
