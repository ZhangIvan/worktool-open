package gateway

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
)

// pipeListener 把手工配对的 net.Pipe 连接喂给 http.Server——WS 会话建立在
// **同步无缓冲管道**上（Write 阻塞至对端读走，即确定性 TCP 零窗口语义；
// 内核 loopback 缓冲会吸收整帧大写入，无法可靠复现停读阻塞）。
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// TestSlowClientDoesNotStallOtherDevices（CR-t32-T3.2-R4）：停读客户端 + 下发帧时，
// 单帧写必须在写 deadline 内失败并释放 Hub flushMu——健康设备的派发不被拖死。
// 旧实现 writeRaw 无 SetWriteDeadline：阻塞写长期持有 flushMu → 全部设备调度停摆。
func TestSlowClientDoesNotStallOtherDevices(t *testing.T) {
	db, err := sqlite.OpenMem()
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	machine := statemachine.Default()
	pipeline := scheduler.New(db, machine)
	hub := New(db, pipeline, machine, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /webserver/wework/{robotId}", hub.HandleV1)
	ln := &pipeListener{conns: make(chan net.Conn, 4), done: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// 缩短写 deadline（生产 10s；测试不等真实值），用例结束恢复
	origDeadline := wsWriteDeadline
	wsWriteDeadline = 300 * time.Millisecond
	t.Cleanup(func() { wsWriteDeadline = origDeadline })

	// dialPipe：服务端 conn 入 listener，客户端经 NetDial 拿配对端；
	// stallRead=true 时客户端在握手完成后**永不读**（管道写端随之阻塞）。
	dialPipe := func(robot string, stallRead bool) *websocket.Conn {
		t.Helper()
		serverEnd, clientEnd := net.Pipe()
		select {
		case ln.conns <- serverEnd:
		default:
			t.Fatal("listener 队列满")
		}
		dialer := websocket.Dialer{NetDial: func(network, addr string) (net.Conn, error) {
			return clientEnd, nil
		}}
		c, _, err := dialer.Dial("ws://pipe.invalid/webserver/wework/"+robot, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", robot, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		if stallRead {
			return c // 不启动读循环：停读客户端
		}
		go func() { // 健康设备持续读（管道不积压）
			for {
				if _, _, err := c.ReadMessage(); err != nil {
					return
				}
			}
		}()
		return c
	}

	dialPipe("robot-slow", true)
	dialPipe("robot-healthy", false)
	time.Sleep(150 * time.Millisecond) // 等两端建连注册/空队列冲刷完成

	// 各入队一条待下发指令（慢设备帧大小无关紧要：管道零读 = 写必阻塞）
	now := time.Now().UTC()
	for _, c := range []sqlite.Command{
		{CommandID: "cmd-slow", Target: "v1:robot-slow", CommandType: 203,
			Payload: `{"type":203,"receivedContent":"` + strings.Repeat("x", 64<<10) + `"}`,
			State:   "queued", RiskClass: "L0", CreatedAt: now},
		{CommandID: "cmd-ok", Target: "v1:robot-healthy", CommandType: 203,
			Payload: `{"type":203,"receivedContent":"ok"}`, State: "queued", RiskClass: "L0", CreatedAt: now},
	} {
		if err := db.InsertCommand(context.Background(), nil, c); err != nil {
			t.Fatalf("入队 %s: %v", c.CommandID, err)
		}
	}

	// 慢设备派发：单帧写必须在写 deadline 内失败（而非无限期阻塞持锁）
	slowDone := make(chan error, 1)
	go func() {
		_, err := hub.DispatchQueued(context.Background(), "v1:robot-slow")
		slowDone <- err
	}()
	select {
	case err := <-slowDone:
		if err == nil {
			t.Fatal("停读客户端上的帧写应失败")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("慢客户端写未在写 deadline 内失败（writeRaw 缺 SetWriteDeadline？）")
	}

	// flushMu 已释放：健康设备派发不被慢设备拖死（若锁被长期持有，此处将一直阻塞）
	healthyDone := make(chan int, 1)
	go func() {
		n, _ := hub.DispatchQueued(context.Background(), "v1:robot-healthy")
		healthyDone <- n
	}()
	select {
	case n := <-healthyDone:
		if n != 1 {
			t.Fatalf("健康设备应派发 1 条，实际 %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("健康设备派发被慢客户端拖死（flushMu 未释放？）")
	}
}
