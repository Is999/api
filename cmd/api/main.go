package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"api/internal/bootstrap"
	"api/internal/infra/loggerx"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/proc"
)

const (
	// shutdownTimeout 为 HTTP 排空后的基础设施关闭预留时间。
	shutdownTimeout = 20 * time.Second
	// forceQuitTimeout 必须晚于应用停止期限，并早于容器默认 30 秒终止宽限期。
	forceQuitTimeout = 29 * time.Second
)

// configFile 支持通过 -f 指定配置文件，便于区分本地、测试和线上环境。
var configFile = flag.String("f", "./etc/config.yaml", "the config file")

// buildVersion 由构建阶段通过 -ldflags 注入，用于发布排查。
var buildVersion = "dev"

// showVersion 控制是否只输出二进制版本并退出。
var showVersion = flag.Bool("version", false, "print build version and exit")

// lifecycleApp 是进程入口需要的最小应用生命周期，便于验证启动和停止失败的退出码。
type lifecycleApp interface {
	Start() error               // Start 阻塞运行，监听器异常时返回错误。
	Stop(context.Context) error // Stop 在统一期限内排空请求并释放资源。
}

// main 解析启动参数并按 runApp 退出码结束进程。
func main() {
	flag.Parse()
	if *showVersion {
		fmt.Println(buildVersion)
		return
	}
	os.Exit(runApp(context.Background(), *configFile))
}

// runApp 执行应用装配、启动和停止，并在全部资源释放后关闭日志。
func runApp(ctx context.Context, configFile string) (exitCode int) {
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		// 日志最后关闭，确保 HTTP、热加载、连接池和 tracing 的停止错误仍写入正式日志通道。
		if err := logx.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "close application log: %v\n", err)
			exitCode = 1
		}
	}()
	proc.SetTimeToForceQuit(forceQuitTimeout)
	app, err := bootstrap.Wire(ctx, configFile)
	if err != nil {
		loggerx.Errorw(ctx, "应用启动装配失败", err)
		return 1
	}
	return runAppLifecycle(ctx, app)
}

// runAppLifecycle 把启动或停止任一失败转换为非零退出码，避免编排系统误判发布成功。
func runAppLifecycle(ctx context.Context, app lifecycleApp) (exitCode int) {
	if ctx == nil {
		ctx = context.Background()
	}
	if app == nil {
		loggerx.Errorw(ctx, "应用生命周期未初始化", errors.New("应用生命周期为空"))
		return 1
	}
	defer func() {
		// 退出时统一关闭 server、热加载、连接池和 tracer provider；停止失败覆盖成功启动结果。
		stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := app.Stop(stopCtx); err != nil {
			loggerx.Errorw(stopCtx, "应用停止失败", err)
			exitCode = 1
		}
	}()

	if err := app.Start(); err != nil {
		loggerx.Errorw(ctx, "应用启动失败", err)
		return 1
	}
	return 0
}
