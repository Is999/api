package bootstrap

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/service"
	"github.com/zeromicro/go-zero/rest"
)

// TestLimitHTTPDrainClosesLongRequest 确保长请求不会阻塞后续资源关闭。
func TestLimitHTTPDrainClosesLongRequest(t *testing.T) {
	// 处理器阻塞到请求上下文取消，用于模拟超过排空期限的长请求。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	requestStarted := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
	})}
	drainDone := make(chan struct{})
	defer close(drainDone)
	limitHTTPDrain(server, 30*time.Millisecond, drainDone)

	// 服务启动并确认请求进入后再触发关闭，避免把连接竞态当成排空结果。
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	go func() {
		_, _ = http.Get("http://" + listener.Addr().String())
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("long request did not start")
	}

	// Shutdown 必须在排空上限内结束，同时 Serve 只允许返回标准关闭错误。
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- server.Shutdown(context.Background())
	}()
	select {
	case err = <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shutdown exceeded HTTP drain limit")
	}
	if err = <-serveDone; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve error = %v", err)
	}
}

// TestRunHTTPServersReturnsBindErrorAndClosesPeer 确保端口冲突返回 error，且已启动的同组监听器会被关闭。
func TestRunHTTPServersReturnsBindErrorAndClosesPeer(t *testing.T) {
	goodPort := reserveHTTPTestPort(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen occupied port: %v", err)
	}
	defer func() { _ = occupied.Close() }()
	badPort := occupied.Addr().(*net.TCPAddr).Port

	good := newHTTPServerRun("测试公网", "127.0.0.1", goodPort, newHTTPTestServer(t, goodPort))
	bad := newHTTPServerRun("测试内网", "127.0.0.1", badPort, newHTTPTestServer(t, badPort))
	if err = runHTTPServers([]*httpServerRun{good, bad}, 200*time.Millisecond); err == nil {
		t.Fatal("runHTTPServers() error = nil, want occupied port error")
	}
	connection, dialErr := net.DialTimeout("tcp", good.address, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		t.Fatalf("peer listener %s is still reachable after startup failure", good.address)
	}
}

// reserveHTTPTestPort 获取一个当前空闲端口并立即释放，仅用于本进程启动测试。
func reserveHTTPTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}
	return port
}

// newHTTPTestServer 创建只用于生命周期测试的最小 go-zero HTTP Server。
func newHTTPTestServer(t *testing.T, port int) *rest.Server {
	t.Helper()
	server, err := rest.NewServer(rest.RestConf{
		ServiceConf: service.ServiceConf{Name: "http-lifecycle-test"},
		Host:        "127.0.0.1",
		Port:        port,
	})
	if err != nil {
		t.Fatalf("rest.NewServer() error = %v", err)
	}
	return server
}
