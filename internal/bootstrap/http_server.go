package bootstrap

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/proc"
	"github.com/zeromicro/go-zero/rest"
)

const (
	// httpStartupProbeTimeout 限制单个监听器从启动到端口可连接的最长等待时间。
	httpStartupProbeTimeout = 3 * time.Second
	// httpStartupProbeInterval 控制启动探测频率，避免端口尚未监听时产生高频空转。
	httpStartupProbeInterval = 20 * time.Millisecond
)

// httpServerRun 隔离 go-zero HTTP 启动 API 的进程级 panic，并保存可优雅关闭的底层 Server。
type httpServerRun struct {
	name    string                      // 监听器名称，用于定位公网或内网启动失败
	address string                      // TCP 探测地址，通配监听地址会转换为本机回环地址
	server  *rest.Server                // 已完成路由注册的 go-zero HTTP Server
	active  atomic.Pointer[http.Server] // StartOption 暴露的底层 Server，供局部失败时关闭同组监听器
}

// httpServerResult 保存监听器退出结果，正常停机时 err 为空。
type httpServerResult struct {
	run *httpServerRun // 已退出的监听器
	err error          // 启动或运行失败，正常信号停机为空
}

// newHTTPServerRun 创建一个可探测、可关闭的 HTTP 监听器运行单元。
func newHTTPServerRun(name, host string, port int, server *rest.Server) *httpServerRun {
	return &httpServerRun{
		name:    name,
		address: httpProbeAddress(host, port),
		server:  server,
	}
}

// serve 启动单个监听器，仅将 go-zero 的监听或 TLS panic 转为 error。
func (r *httpServerRun) serve(drainTimeout time.Duration) (err error) {
	if r == nil || r.server == nil {
		return errors.Errorf("HTTP 监听器未初始化")
	}
	// go-zero 将监听失败转换为 panic，此处收口为启动错误。
	defer func() {
		if recovered := recover(); recovered != nil {
			switch value := recovered.(type) {
			case error:
				err = errors.Wrapf(value, "%s HTTP 监听器异常退出", r.name)
			default:
				err = errors.Errorf("%s HTTP 监听器异常退出: %v", r.name, value)
			}
		}
	}()
	drainDone := make(chan struct{})
	defer close(drainDone)
	r.server.StartWithOpts(func(server *http.Server) {
		r.active.Store(server)
		limitHTTPDrain(server, drainTimeout, drainDone)
	})
	return nil
}

// shutdown 停止已进入监听阶段的底层 HTTP Server，尚未启动或已停止时不报错。
func (r *httpServerRun) shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	server := r.active.Load()
	if server == nil {
		return nil
	}
	return errors.Tag(server.Shutdown(ctx))
}

// runHTTPServers 逐个确认端口监听，任一失败时关闭同组已启动监听器。
func runHTTPServers(runs []*httpServerRun, drainTimeout time.Duration) error {
	if len(runs) == 0 {
		return errors.Errorf("HTTP 监听器不能为空")
	}
	// 启动 goroutine 前完整校验列表，避免配置错误造成部分监听。
	for _, run := range runs {
		if run == nil || run.server == nil || run.address == "" {
			return errors.Errorf("HTTP 监听器配置不完整")
		}
	}
	// 结果通道按监听器数量缓冲，回滚期间 serve 不会阻塞上报。
	results := make(chan httpServerResult, len(runs))
	started := make([]*httpServerRun, 0, len(runs))
	// 每个端口确认可连接后再启动下一项，使失败回滚范围保持确定。
	for _, run := range runs {
		started = append(started, run)
		go func(current *httpServerRun) {
			results <- httpServerResult{run: current, err: current.serve(drainTimeout)}
		}(run)
		exited, err := waitHTTPServerReady(run, results, httpStartupProbeTimeout)
		if err != nil {
			// 先关闭已登记监听器，阻止启动失败后继续接收请求。
			if cleanupErr := stopHTTPServerGroup(started, drainTimeout); cleanupErr != nil {
				return errors.Wrapf(err, "HTTP 启动失败且关闭同组监听器失败 cleanup_error=%v", cleanupErr)
			}
			remaining := len(started)
			if exited {
				remaining--
			}
			// 再收齐 serve 结果，避免遗留 goroutine 进入全局停机阶段。
			if joinErr := waitHTTPServerResults(results, remaining, drainTimeout); joinErr != nil {
				return errors.Wrapf(err, "HTTP 启动失败且等待同组监听器退出失败 join_error=%v", joinErr)
			}
			return errors.Tag(err)
		}
	}

	// 异常退出时关闭同组监听器；正常退出继续收齐全局停机中的其他结果。
	remaining := len(started)
	for remaining > 0 {
		result := <-results
		remaining--
		if result.err == nil {
			continue
		}
		if cleanupErr := stopHTTPServerGroup(started, drainTimeout); cleanupErr != nil {
			return errors.Wrapf(result.err, "HTTP 运行失败且关闭同组监听器失败 cleanup_error=%v", cleanupErr)
		}
		return errors.Tag(result.err)
	}
	return nil
}

// stopHTTPServerGroup 限时关闭本应用监听器；存在已启动服务时也触发 go-zero 全局停机以释放 Start 等待者。
func stopHTTPServerGroup(runs []*httpServerRun, drainTimeout time.Duration) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	return errors.Tag(shutdownHTTPServers(shutdownCtx, runs...))
}

// waitHTTPServerReady 以 TCP 连接确认监听器已绑定，避免在端口冲突时提前输出启动成功语义。
func waitHTTPServerReady(run *httpServerRun, results <-chan httpServerResult, timeout time.Duration) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(httpStartupProbeInterval)
	defer ticker.Stop()
	for {
		connection, err := net.DialTimeout("tcp", run.address, httpStartupProbeInterval)
		if err == nil {
			_ = connection.Close()
			return false, nil
		}
		select {
		case result := <-results:
			if result.err != nil {
				return true, errors.Tag(result.err)
			}
			return true, errors.Errorf("%s HTTP 监听器在启动确认前停止", result.run.name)
		case <-ticker.C:
		case <-timer.C:
			return false, errors.Errorf("%s HTTP 监听器启动超时 address=%s timeout=%s", run.name, run.address, timeout)
		}
	}
}

// waitHTTPServerResults 在启动回滚后收齐已登记监听器的退出结果，避免遗留 goroutine 访问全局停机注册表。
func waitHTTPServerResults(results <-chan httpServerResult, count int, timeout time.Duration) error {
	if count <= 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for range count {
		select {
		case <-results:
		case <-timer.C:
			return errors.Errorf("等待 %d 个 HTTP 监听器退出超时 timeout=%s", count, timeout)
		}
	}
	return nil
}

// shutdownHTTPServers 并行关闭公网与内网监听器，使两者共享调用方给出的总体截止时间。
func shutdownHTTPServers(ctx context.Context, runs ...*httpServerRun) error {
	if ctx == nil {
		ctx = context.Background()
	}
	results := make(chan error, len(runs))
	count := 0
	startedCount := 0
	// 各监听器共享同一截止时间并行排空。
	for _, run := range runs {
		if run == nil {
			continue
		}
		count++
		if run.active.Load() != nil {
			startedCount++
		}
		go func(current *httpServerRun) {
			results <- current.shutdown(ctx)
		}(run)
	}
	var firstErr error
	// 等待全部关闭结果，只保留第一处错误。
	for range count {
		if err := <-results; err != nil && firstErr == nil {
			firstErr = errors.Tag(err)
		}
	}
	if startedCount > 0 {
		// 已启动的 StartWithOpts 还需全局信号才能释放等待协程。
		done := make(chan struct{})
		go func() {
			proc.Shutdown()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			if firstErr == nil {
				firstErr = errors.Wrap(ctx.Err(), "等待 go-zero HTTP 停机监听器退出超时")
			}
		}
	}
	return firstErr
}

// httpProbeAddress 把通配监听地址转换为本机可连接地址，端口非法时返回空值。
func httpProbeAddress(host string, port int) string {
	if port <= 0 || port > 65535 || host != strings.TrimSpace(host) {
		return ""
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
