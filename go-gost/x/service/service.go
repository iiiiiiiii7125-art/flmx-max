package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/admission"
	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/metrics"
	"github.com/go-gost/core/observer"
	"github.com/go-gost/core/observer/stats"
	"github.com/go-gost/core/recorder"
	"github.com/go-gost/core/service"
	ctxvalue "github.com/go-gost/x/ctx"
	xnet "github.com/go-gost/x/internal/net"
	xmetrics "github.com/go-gost/x/metrics"
	xstats "github.com/go-gost/x/observer/stats"
	"github.com/rs/xid"
)

type options struct {
	admission      admission.Admission
	recorders      []recorder.RecorderObject
	preUp          []string
	postUp         []string
	preDown        []string
	postDown       []string
	stats          stats.Stats
	observer       observer.Observer
	observerPeriod time.Duration
	logger         logger.Logger
}

// R3-A01（顺手项）: the three protocol-block switches are atomic.
// SetProtocolBlock writes them from the command path while
// detectProtocol reads them on the connection-sniffing path; the
// plain ints raced (a connection could observe a mixed old/new
// combination). Scope is exactly these three globals.
var isTls atomic.Int32

var isHttp atomic.Int32

var isSocks atomic.Int32

// needWrap caches "any protocol block is on" for the per-connection
// wrap decision. It was the one switch the R3-A01 atomisation
// missed: SetProtocolBlock writes it from the command path while
// every accepted connection reads it, a plain-bool data race (D2-01)
// that could also leave a just-toggled block silently unapplied to
// racing connections.
var needWrap atomic.Bool

// SetProtocolBlock sets protocol blocking switches and recomputes wrapper need
func SetProtocolBlock(httpOn int, tlsOn int, socksOn int) {
	isHttp.Store(int32(httpOn))
	isTls.Store(int32(tlsOn))
	isSocks.Store(int32(socksOn))
	needWrap.Store(isTls.Load()+isSocks.Load()+isHttp.Load() > 0)
}

// GetProtocolBlock returns the current protocol blocking switches.
// Partial SetProtocol commands (A01-002) read their defaults from
// here so fields the panel did not send keep their live values
// instead of being reset to 0.
func GetProtocolBlock() (httpOn int, tlsOn int, socksOn int) {
	return int(isHttp.Load()), int(isTls.Load()), int(isSocks.Load())
}

type Option func(opts *options)

func init() {
	// NOTE: This package can be imported by tests/tools that don't have a local
	// config.json. Missing config should not crash the process.
	_, _ = LoadConfig("config.json")
	needWrap.Store(isTls.Load()+isSocks.Load()+isHttp.Load() > 0)
}

func AdmissionOption(admission admission.Admission) Option {
	return func(opts *options) {
		opts.admission = admission
	}
}

func RecordersOption(recorders ...recorder.RecorderObject) Option {
	return func(opts *options) {
		opts.recorders = recorders
	}
}

func PreUpOption(cmds []string) Option {
	return func(opts *options) {
		opts.preUp = cmds
	}
}

func PreDownOption(cmds []string) Option {
	return func(opts *options) {
		opts.preDown = cmds
	}
}

func PostUpOption(cmds []string) Option {
	return func(opts *options) {
		opts.postUp = cmds
	}
}

func PostDownOption(cmds []string) Option {
	return func(opts *options) {
		opts.postDown = cmds
	}
}

func StatsOption(stats stats.Stats) Option {
	return func(opts *options) {
		opts.stats = stats
	}
}

func ObserverOption(observer observer.Observer) Option {
	return func(opts *options) {
		opts.observer = observer
	}
}

func ObserverPeriodOption(period time.Duration) Option {
	return func(opts *options) {
		opts.observerPeriod = period
	}
}

func LoggerOption(logger logger.Logger) Option {
	return func(opts *options) {
		opts.logger = logger
	}
}

type defaultService struct {
	name     string
	listener listener.Listener
	handler  handler.Handler
	status   *Status
	options  options
}

func NewService(name string, ln listener.Listener, h handler.Handler, opts ...Option) service.Service {
	var options options
	for _, opt := range opts {
		opt(&options)
	}
	s := &defaultService{
		name:     name,
		listener: ln,
		handler:  h,
		options:  options,
		status: &Status{
			createTime: time.Now(),
			events:     make([]Event, 0, MaxEventSize),
			stats:      options.stats,
		},
	}
	s.setState(StateRunning)

	s.execCmds("pre-up", s.options.preUp)

	return s
}

func (s *defaultService) Addr() net.Addr {
	return s.listener.Addr()
}

// FlushTraffic 把服务 stats 里尚未被周期采样换出的字节原子换出并
// 交给全局管理器。调用点：服务 Close 自身（一切卸载路径的收口）、
// program.stopRuntime 的两遍承接、loader 换代卸载点——最后一批
// 不足一个采样周期的字节不再滞留在服务 stats 里随对象/进程消
// 失，而是进管理器计数并由管理器落盘进 spool 续报。与周期采样
// 共用 TakeTraffic，同一字节只会被换出一次，多点重复调用恒为
// 零副作用。
func (s *defaultService) FlushTraffic() {
	if s.options.observer == nil {
		return
	}
	st, ok := s.status.Stats().(*xstats.Stats)
	if !ok || st == nil {
		return
	}
	input, output := st.TakeTraffic()
	if input == 0 && output == 0 {
		return
	}
	GetGlobalTrafficManager().addServiceTraffic(s.name, int64(output), int64(input))
}

// FlushServicesTraffic 把一组服务 stats 里尚未被周期采样换出的尾
// 量逐个交进全局管理器（与周期采样共用 TakeTraffic 出口，同一字
// 节只换出一次）。
func FlushServicesTraffic(services map[string]service.Service) {
	for _, svc := range services {
		if flusher, ok := svc.(interface{ FlushTraffic() }); ok {
			flusher.FlushTraffic()
		}
	}
}

// FlushServicesTrafficTwice 两遍换出：第一遍之后留一个收敛窗口再
// 换第二遍，承接第一遍之后才计入 stats 的尾量（采样来源已关停
// 后、排空中的连接写入的最后字节）。停机路径专用，顺序编排见
// program.stopRuntime（先全集注销关源、再两遍换出）。
func FlushServicesTrafficTwice(services map[string]service.Service, settle time.Duration) {
	FlushServicesTraffic(services)
	if settle > 0 {
		time.Sleep(settle)
	}
	FlushServicesTraffic(services)
}

func (s *defaultService) Serve() error {

	s.execCmds("post-up", s.options.postUp)
	s.setState(StateReady)
	s.status.addEvent(Event{
		Time:    time.Now(),
		Message: fmt.Sprintf("service %s is listening on %s", s.name, s.listener.Addr()),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if s.status.Stats() != nil {
		go s.observeStats(ctx)
	}

	if v := xmetrics.GetGauge(
		xmetrics.MetricServicesGauge,
		metrics.Labels{}); v != nil {
		v.Inc()
		defer v.Dec()
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	var tempDelay time.Duration
	for {
		conn, e := s.listener.Accept()
		if e != nil {

			// TODO: remove Temporary checking
			if ne, ok := e.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 1 * time.Second
				} else {
					tempDelay *= 2
				}
				if max := 5 * time.Second; tempDelay > max {
					tempDelay = max
				}

				s.setState(StateFailed)

				s.options.logger.Warnf("accept: %v, retrying in %v", e, tempDelay)
				time.Sleep(tempDelay)
				continue
			}
			s.setState(StateClosed)

			if !errors.Is(e, net.ErrClosed) {
				s.options.logger.Errorf("accept: %v", e)
			}

			return e
		}

		if tempDelay > 0 {
			tempDelay = 0
			s.setState(StateReady)
		}

		clientAddr := conn.RemoteAddr().String()
		if ca, ok := conn.(xnet.ClientAddr); ok {
			if addr := ca.ClientAddr(); addr != nil {
				clientAddr = addr.String()
			}
		}
		clientIP := clientAddr
		if h, _, _ := net.SplitHostPort(clientAddr); h != "" {
			clientIP = h
		}

		sid := xid.New().String()
		ctx := ctxvalue.ContextWithSid(ctx, ctxvalue.Sid(sid))
		ctx = ctxvalue.ContextWithClientAddr(ctx, ctxvalue.ClientAddr(clientAddr))
		ctx = ctxvalue.ContextWithHash(ctx, &ctxvalue.Hash{Source: clientIP})

		log := s.options.logger.WithFields(map[string]any{
			"sid": sid,
		})

		for _, rec := range s.options.recorders {
			if rec.Record == recorder.RecorderServiceClientAddress {
				if err := rec.Recorder.Record(ctx, []byte(clientIP)); err != nil {
					log.Errorf("record %s: %v", rec.Record, err)
				}
				break
			}
		}
		if s.options.admission != nil &&
			!s.options.admission.Admit(ctx, clientAddr) {
			conn.Close()
			log.Debugf("admission: %s is denied", clientAddr)
			continue
		}

		wg.Add(1)

		go func() {
			defer wg.Done()

			// D3-02: a panic anywhere in a connection handler
			// must kill only this connection, never the agent
			// process. Before this boundary, the dispatch had
			// no recover at all and one bad connection (e.g. an
			// unchecked type assertion in a protocol handler)
			// took down every service on the node.
			defer func() {
				if r := recover(); r != nil {
					log.Errorf("handler panic, connection dropped (service %s, client %s): %v", s.name, clientAddr, r)
					conn.Close()
					if v := xmetrics.GetCounter(xmetrics.MetricServiceHandlerErrorsCounter,
						metrics.Labels{"service": s.name, "client": clientIP}); v != nil {
						v.Inc()
					}
					if sts := s.status.stats; sts != nil {
						sts.Add(stats.KindTotalErrs, 1)
					}
				}
			}()

			if v := xmetrics.GetCounter(xmetrics.MetricServiceRequestsCounter,
				metrics.Labels{"service": s.name, "client": clientIP}); v != nil {
				v.Inc()
			}

			if v := xmetrics.GetGauge(xmetrics.MetricServiceRequestsInFlightGauge,
				metrics.Labels{"service": s.name, "client": clientIP}); v != nil {
				v.Inc()
				defer v.Dec()
			}

			start := time.Now()
			if v := xmetrics.GetObserver(xmetrics.MetricServiceRequestsDurationObserver,
				metrics.Labels{"service": s.name}); v != nil {
				defer func() {
					v.Observe(float64(time.Since(start).Seconds()))
				}()
			}

			if needWrap.Load() {
				conn = wrapConnPDetection(conn)
			}

			if err := s.handler.Handle(ctx, conn); err != nil {
				if !errors.Is(err, net.ErrClosed) {
					log.Error(err)
				}
				if v := xmetrics.GetCounter(xmetrics.MetricServiceHandlerErrorsCounter,
					metrics.Labels{"service": s.name, "client": clientIP}); v != nil {
					v.Inc()
				}
				if sts := s.status.stats; sts != nil {
					sts.Add(stats.KindTotalErrs, 1)
				}
			}
		}()
	}
}

func (s *defaultService) Status() *Status {
	return s.status
}

func (s *defaultService) Close() error {
	// 服务关闭即终局（注销/换代/暂停/删除都经此路或 Unregister
	// 连带至此）：先把 stats 里未被周期采样换出的尾量交进全局管
	// 理器再关——否则最后一截（不足一个采样周期）流量随 stats
	// 对象丢弃。此前只有 program.stopRuntime 在停机时显式调用
	// FlushTraffic，面板/API 驱动的服务换代、删除、暂停卸载全都
	// 漏了这一截（与审计 A-T-03 在 reload 链立案的漏承接同机制，
	// loader 卸载点已另行补齐；此处收口到关闭动作本身，使全部服
	// 务卸载路径差集归零）。重复 Close 时 TakeTraffic 恒为零，
	// 无副作用。
	s.FlushTraffic()

	s.execCmds("pre-down", s.options.preDown)
	defer s.execCmds("post-down", s.options.postDown)

	if closer, ok := s.handler.(io.Closer); ok {
		closer.Close()
	}
	return s.listener.Close()
}

func (s *defaultService) execCmds(phase string, cmds []string) {
	for _, cmd := range cmds {
		cmd := strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		s.options.logger.Info(cmd)

		if err := exec.Command("/bin/sh", "-c", cmd).Run(); err != nil {
			s.options.logger.Warnf("[%s] %s: %v", phase, cmd, err)
		}
	}
}

func (s *defaultService) setState(state State) {
	s.status.setState(state)

	msg := fmt.Sprintf("service %s is %s", s.name, state)
	s.status.addEvent(Event{
		Time:    time.Now(),
		Message: msg,
	})

	if obs := s.options.observer; obs != nil {
		obs.Observe(context.Background(), []observer.Event{ServiceEvent{
			Kind:    "service",
			Service: s.name,
			State:   state,
			Msg:     msg,
		}})
	}
}

func (s *defaultService) observeStats(ctx context.Context) {
	if s.options.observer == nil {
		return
	}

	d := s.options.observerPeriod
	if d == 0 {
		d = 5 * time.Second
	}
	if d < time.Second {
		d = 1 * time.Second
	}

	var events []observer.Event

	ticker := time.NewTicker(d)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:

			// First, try to send any pending events
			if len(events) > 0 {
				if err := s.options.observer.Observe(ctx, events); err == nil {
					events = nil
				}
				continue
			}

			st := s.status.Stats()
			if st == nil {
				continue
			}

			isUpdated := st.IsUpdated()
			if isUpdated {
				inputBytes := st.Get(stats.KindInputBytes)
				outputBytes := st.Get(stats.KindOutputBytes)
				// 流量字节以 TakeTraffic 原子换出：换出量即本周期
				// 增量（计数器此前只保留未采样量，与旧 Get+按快照
				// 扣减的事件口径一致），且与 FlushTraffic 的停机
				// 换出互斥——同一字节只会被一方换走，不重不漏。
				if localStats, ok := st.(*xstats.Stats); ok {
					inputBytes, outputBytes = localStats.TakeTraffic()
				}

				evs := []observer.Event{
					xstats.StatsEvent{
						Kind:         "service",
						Service:      s.name,
						TotalConns:   st.Get(stats.KindTotalConns),
						CurrentConns: st.Get(stats.KindCurrentConns),
						InputBytes:   inputBytes,
						OutputBytes:  outputBytes,
						TotalErrs:    st.Get(stats.KindTotalErrs),
					},
				}

				// 将流量累积到全局管理器，而不是立即上报。
				// 注意 AddTraffic(up, down) 的实参顺序：up=gost
				// OutputBytes（远端→客户端）、down=gost InputBytes
				// （客户端→远端）。上报线格式 U/D 沿用此映射，面板按
				// U=出、D=入解读（N-01 钉死，勿按 U=上行字面修改）。
				//
				// 计数口径（A02-003）：此处读的是服务级 stats，其来源
				// 是客户端连接在 listener 接受时挂的 wrapper——计的是
				// 客户端侧连接的全部线字节。带握手的协议（SOCKS5 的
				// greeting+请求/应答、HTTP 的请求行与头）其协议字节
				// 一并计入（SOCKS5 每连接双向各约 +12B）；无握手的
				// forward/local 即纯载荷。这是统一的「客户端连接线
				// 字节」口径，非逐协议载荷口径，配额/计费标定须按此
				// 理解（见 observer/stats/wrapper 的同注）。
				if outputBytes > 0 || inputBytes > 0 {
					globalManager := GetGlobalTrafficManager()
					globalManager.addServiceTraffic(s.name, int64(outputBytes), int64(inputBytes))
				}

				if err := s.options.observer.Observe(ctx, evs); err != nil {
					fmt.Printf("发送观察器事件失败: %v", err)
					events = evs
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

type ServiceEvent struct {
	Kind    string
	Service string
	State   State
	Msg     string
}

func (ServiceEvent) Type() observer.EventType {
	return observer.EventStatus
}

func wrapConnPDetection(conn net.Conn) net.Conn {
	return &detectConn{
		Conn:   conn,
		reader: bufio.NewReader(conn),
	}
}

type detectConn struct {
	net.Conn
	reader   *bufio.Reader
	detected bool
}

func (c *detectConn) Read(b []byte) (int, error) {
	n, err := c.reader.Read(b)
	if n > 0 && !c.detected {
		c.detected = true
		if detectProtocol(b[:n], c.Conn) {
			return 0, fmt.Errorf("connection blocked")
		}
	}
	return n, err
}

func detectProtocol(data []byte, conn net.Conn) (blocked bool) {
	// 如果是 UDP，则不检测，直接放行
	if conn.RemoteAddr().Network() == "udp" || conn.RemoteAddr().Network() == "udp4" || conn.RemoteAddr().Network() == "udp6" {
		return false
	}

	if isHttp.Load() == 1 && detectHTTP(data) {
		conn.Close()
		return true
	}

	if isTls.Load() == 1 && detectTLS(data) {
		conn.Close()
		return true
	}

	if isSocks.Load() == 1 && detectSOCKS(data) {
		conn.Close()
		return true
	}

	return false
}

func detectHTTP(data []byte) bool {
	if len(data) < 3 {
		return false
	}
	switch {
	case len(data) >= 3 && data[0] == 'G' && data[1] == 'E' && data[2] == 'T':
		return true
	case len(data) >= 4 && data[0] == 'P' && data[1] == 'O' && data[2] == 'S' && data[3] == 'T':
		return true
	case len(data) >= 3 && data[0] == 'P' && data[1] == 'U' && data[2] == 'T':
		return true
	case len(data) >= 6 && data[0] == 'D' && data[1] == 'E' && data[2] == 'L' &&
		data[3] == 'E' && data[4] == 'T' && data[5] == 'E':
		return true
	case len(data) >= 4 && data[0] == 'H' && data[1] == 'E' && data[2] == 'A' && data[3] == 'D':
		return true
	case len(data) >= 7 && data[0] == 'O' && data[1] == 'P' && data[2] == 'T' &&
		data[3] == 'I' && data[4] == 'O' && data[5] == 'N' && data[6] == 'S':
		return true
	case len(data) >= 5 && data[0] == 'P' && data[1] == 'A' && data[2] == 'T' &&
		data[3] == 'C' && data[4] == 'H':
		return true
	case len(data) >= 7 && data[0] == 'C' && data[1] == 'O' && data[2] == 'N' &&
		data[3] == 'N' && data[4] == 'E' && data[5] == 'C' && data[6] == 'T': // HTTPS proxy
		return true
	default:
		return false
	}
}

func detectTLS(data []byte) bool {
	if len(data) < 5 {
		return false
	}
	if data[0] == 0x16 && data[1] == 0x03 && data[2] >= 0x01 && data[2] <= 0x04 {
		return true
	}
	return false
}

func detectSOCKS(data []byte) bool {
	if len(data) == 0 {
		return false
	}

	switch data[0] {
	case 0x04:
		if len(data) < 7 {
			return false
		}
		cmd := data[1]
		if cmd != 0x01 && cmd != 0x02 {
			return false
		}
		return true
	case 0x05:
		if len(data) < 2 {
			return false
		}
		nMethods := int(data[1])
		if len(data) < 2+nMethods {
			return false
		}
		for _, method := range data[2 : 2+nMethods] {
			if method == 0x00 || method == 0x02 {
				return true
			}
		}
	}
	return false
}

// Config 配置结构体
type Config struct {
	Addr   string `json:"addr"`
	Secret string `json:"secret"`
	Http   int    `json:"http"`
	Tls    int    `json:"tls"`
	Socks  int    `json:"socks"`
}

func LoadConfig(configPath string) (string, error) {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return "", fmt.Errorf("配置文件不存在: %s", configPath)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("读取配置文件失败: %v", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("解析配置文件失败: %v", err)
	}

	isTls.Store(int32(config.Tls))
	isSocks.Store(int32(config.Socks))
	isHttp.Store(int32(config.Http))

	return "", nil

}
