package ws

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// R2-P1-F09 回归：停滞消费者（无人取队列）时，字节预算必须把单会话
// 滞留压在预算内——此前只有 16 条深度帽，单条可达 10MiB 读上限，
// 最坏滞留 160MiB/会话。
func TestRealtimeQueueByteBudgetBoundsStalledConsumer(t *testing.T) {
	q := newRealtimeQueue()
	frame := make([]byte, 3<<20) // 3MiB

	// 预算 8MiB：前两帧准入（6MiB），第三帧起超预算被丢。
	want := []bool{true, true, false, false}
	for i, w := range want {
		if got := q.offer(frame); got != w {
			t.Fatalf("offer #%d = %v, want %v", i+1, got, w)
		}
	}
	if got := q.queuedBytes.Load(); got != 6<<20 {
		t.Fatalf("queuedBytes = %d, want %d", got, 6<<20)
	}
	if got := q.dropped.Load(); got != 2 {
		t.Fatalf("dropped = %d, want 2", got)
	}
	if got := q.droppedBytes.Load(); got != 6<<20 {
		t.Fatalf("droppedBytes = %d, want %d", got, 6<<20)
	}
	// 实际滞留在通道内的字节同样不得超过预算。
	var retained int64
	for {
		select {
		case msg := <-q.ch:
			retained += int64(len(msg))
		default:
			if retained != 6<<20 {
				t.Fatalf("retained bytes in channel = %d, want %d", retained, 6<<20)
			}
			return
		}
	}
}

// R2-P1-F09：单条合法大帧（等于入站读上限 10MiB，大于 8MiB 预算）
// 在队列为空时必须准入，否则该类帧对任何消费者都永久不可达；队列
// 非空时第二条同等大帧被预算挡下。
func TestRealtimeQueueOversizedLoneFrameAdmitted(t *testing.T) {
	q := newRealtimeQueue()
	big := make([]byte, wsMaxMessageSize)

	if !q.offer(big) {
		t.Fatal("lone 10MiB frame on empty queue must be admitted")
	}
	if got := q.queuedBytes.Load(); got != wsMaxMessageSize {
		t.Fatalf("queuedBytes = %d, want %d", got, wsMaxMessageSize)
	}
	if q.offer(big) {
		t.Fatal("second 10MiB frame over a non-empty queue must be dropped by the byte budget")
	}
	if got := q.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	// 模拟 writer 取走第一帧后，下一条大帧重新可准入。
	msg := <-q.ch
	q.queuedBytes.Add(-int64(len(msg)))
	if !q.offer(big) {
		t.Fatal("10MiB frame after drain must be admitted again")
	}
}

// R2-P1-F09：深度帽（16 条）语义不回退——小帧由深度帽先行封顶，
// 记账与通道内容一致。
func TestRealtimeQueueDepthCapPreserved(t *testing.T) {
	q := newRealtimeQueue()
	tiny := []byte("x")
	for i := 0; i < wsBroadcastQueueSize; i++ {
		if !q.offer(tiny) {
			t.Fatalf("tiny frame #%d must be admitted", i+1)
		}
	}
	if q.offer(tiny) {
		t.Fatal("frame beyond depth cap must be dropped")
	}
	if got := q.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	if got := q.queuedBytes.Load(); got != int64(wsBroadcastQueueSize) {
		t.Fatalf("queuedBytes = %d, want %d", got, wsBroadcastQueueSize)
	}
	if got := len(q.ch); got != wsBroadcastQueueSize {
		t.Fatalf("channel depth = %d, want %d", got, wsBroadcastQueueSize)
	}
}

// R2-P1-F09：广播面集成——同一批广播下，停滞会话被字节预算封顶并
// 计数，正常消费会话帧序/内容完整无丢失，全服聚合计数只含停滞侧。
func TestBroadcastToRealtimeByteBudgetAcrossSessions(t *testing.T) {
	s := NewServer(nil, "wsq-secret")
	stalled := &adminSession{userID: 1, queue: newRealtimeQueue()}
	live := &monitorSession{userID: 2, queue: newRealtimeQueue()}
	s.mu.Lock()
	s.admins[stalled] = struct{}{}
	s.monitors[live] = struct{}{}
	s.mu.Unlock()

	// 正常消费方：与 runRealtimeWriter 相同的取帧+冲减记账循环。
	var mu sync.Mutex
	var received []string
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for i := 0; i < 20; i++ {
			msg := <-live.queue.ch
			live.queue.queuedBytes.Add(-int64(len(msg)))
			mu.Lock()
			received = append(received, string(msg))
			mu.Unlock()
		}
	}()

	const frameSize = 512 << 10 // 512KiB
	const frames = 20           // 合计 10MiB，超过 8MiB 预算
	for i := 0; i < frames; i++ {
		payload := fmt.Sprintf("%06d|", i) + strings.Repeat("a", frameSize)
		s.broadcastToRealtime(payload)
	}

	select {
	case <-drainDone:
	case <-time.After(10 * time.Second):
		t.Fatal("live consumer did not receive all frames")
	}
	mu.Lock()
	if len(received) != frames {
		t.Fatalf("live consumer received %d frames, want %d", len(received), frames)
	}
	for i, got := range received {
		wantPrefix := fmt.Sprintf("%06d|", i)
		if !strings.HasPrefix(got, wantPrefix) || len(got) != frameSize+len(wantPrefix) {
			t.Fatalf("frame #%d corrupted or out of order (len=%d)", i, len(got))
		}
	}
	mu.Unlock()
	if got := live.queue.dropped.Load(); got != 0 {
		t.Fatalf("live session dropped = %d, want 0", got)
	}

	// 停滞会话：准入帧数 = 预算 / 单帧长度（向下取整），其余丢弃。
	frameLen := int64(frameSize + len("000000|"))
	admitted := wsBroadcastQueueBytes / frameLen
	wantDropped := int64(frames) - admitted
	if got := stalled.queue.queuedBytes.Load(); got != admitted*frameLen {
		t.Fatalf("stalled queuedBytes = %d, want %d", got, admitted*frameLen)
	}
	if got := stalled.queue.dropped.Load(); got != wantDropped {
		t.Fatalf("stalled dropped = %d, want %d", got, wantDropped)
	}
	if got := s.broadcastDropped.Load(); got != wantDropped {
		t.Fatalf("server broadcastDropped = %d, want %d", got, wantDropped)
	}
	if got := s.broadcastDroppedBytes.Load(); got != wantDropped*frameLen {
		t.Fatalf("server broadcastDroppedBytes = %d, want %d", got, wantDropped*frameLen)
	}
}

// R2-P1-F09：真实 runRealtimeWriter 路径——取帧后冲减记账，总量超过
// 预算的帧流仍能逐条送达，队列记账最终回零。
func TestRealtimeWriterDrainsAndZeroesAccounting(t *testing.T) {
	upgrader := websocket.Upgrader{}
	connCh := make(chan *websocket.Conn, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- c
	}))
	defer ts.Close()

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	serverConn := <-connCh
	defer serverConn.Close()

	q := newRealtimeQueue()
	done := make(chan struct{})
	defer close(done)
	go runRealtimeWriter(&connWrap{conn: serverConn}, q, done)

	const frameSize = 256 << 10
	const frames = 5
	for i := 0; i < frames; i++ {
		payload := []byte(fmt.Sprintf("%06d|", i) + strings.Repeat("b", frameSize))
		if !q.offer(payload) {
			t.Fatalf("frame #%d must be admitted", i)
		}
	}
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < frames; i++ {
		_, msg, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read frame #%d: %v", i, err)
		}
		if want := fmt.Sprintf("%06d|", i); !strings.HasPrefix(string(msg), want) {
			t.Fatalf("frame #%d out of order", i)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for q.queuedBytes.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := q.queuedBytes.Load(); got != 0 {
		t.Fatalf("queuedBytes after drain = %d, want 0", got)
	}
}
