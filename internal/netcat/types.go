// Package netcat 提供 Web Netcat：在网页里监听任意 TCP 端口，
// 把接入的连接当成反弹 shell 终端使用，并按「特征 → Payload」规则自动应答。
//
// 三种典型用法：
//  1. shell：监听端口等待反弹 shell，浏览器 xterm 直接操作，并可配置自动下发的命令集；
//  2. service：按服务类型（ldap/jndi、rmi、redis、http）内置应答协议，自动回 Exp payload；
//  3. rule：任意端口上按内容特征（子串/前缀/正则/十六进制）匹配，命中即推送对应 payload，
//     一个监听可同时挂多条规则，互不干扰。
package netcat

// 监听模式
const (
	ModeShell = "shell" // 反弹 shell 终端（浏览器交互 + 自动命令集）
	ModeRaw   = "raw"   // 纯特征应答（banner + 规则命中推送 payload）
	ModeRedis = "redis" // 内置 redis rogue server 应答（PING/REPLCONF/PSYNC + 规则）
	ModeLDAP  = "ldap"  // 内置 LDAP/JNDI 应答（Bind/Search，返回 CodeBase/序列化数据/Referral）
	ModeRMI   = "rmi"   // JRMI 握手 + 自定义序列化载荷应答
	ModeHTTP  = "http"  // JNDI CodeBase HTTP 服务，按路径返回 payload 文件
)

// 规则匹配方式
const (
	MatchContains = "contains"
	MatchPrefix   = "prefix"
	MatchSuffix   = "suffix"
	MatchRegex    = "regex"
	MatchHex      = "hex"
)

// 规则载荷来源
const (
	PayloadText   = "text"
	PayloadBase64 = "base64"
	PayloadFile   = "file" // data/netcat/payloads 下的文件
)

// AutoCmd 连接建立后自动下发的命令。
// WaitFor 为空时按 DelayMs 定时发送；填写正则时等到输出命中该正则再发（超时后仍发送）。
type AutoCmd struct {
	Cmd       string `json:"cmd"`
	DelayMs   int    `json:"delay_ms"`
	WaitFor   string `json:"wait_for,omitempty"`
	MaxWaitMs int    `json:"max_wait_ms,omitempty"`
}

// Rule 一条「特征 → payload」规则
type Rule struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Enabled    bool   `json:"enabled"`
	MatchType  string `json:"match_type"` // contains/prefix/suffix/regex/hex
	Pattern    string `json:"pattern"`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
	Window     int    `json:"window,omitempty"` // 参与匹配的滚动窗口字节数，默认 4096

	PayloadType string `json:"payload_type"` // text/base64/file
	Payload     string `json:"payload,omitempty"`
	PayloadFile string `json:"payload_file,omitempty"`

	// Wrap 发送包装：空=原样发送；bulk=redis 批量字符串（$len\r\n data \r\n）
	Wrap       string `json:"wrap,omitempty"`
	SendTimes  int    `json:"send_times,omitempty"` // 每个连接最多命中次数，0=不限
	DelayMs    int    `json:"delay_ms,omitempty"`   // 命中后延迟发送
	CloseAfter bool   `json:"close_after,omitempty"`
}

// Listener 一个监听端口配置
type Listener struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Bind      string    `json:"bind"`
	Port      int       `json:"port"`
	Mode      string    `json:"mode"`
	Banner    string    `json:"banner,omitempty"` // 连接后立即发送的 banner（支持 \n 转义）
	AutoCmds  []AutoCmd `json:"auto_cmds,omitempty"`
	Rules     []Rule    `json:"rules,omitempty"`
	AutoStart bool      `json:"auto_start"`
	MaxConns  int       `json:"max_conns,omitempty"`
	// InitialDelayMs 连接后等待多久再执行 banner / 自动命令集 / 默认 payload
	InitialDelayMs int `json:"initial_delay_ms,omitempty"`

	// LDAP / JNDI
	JavaCodeBase          string `json:"java_code_base,omitempty"` // 例如 http://1.2.3.4:8080/
	JavaClassName         string `json:"java_class_name,omitempty"`
	JavaFactory           string `json:"java_factory,omitempty"`
	JNDIRefAddress        string `json:"jndi_ref_address,omitempty"`        // javaReferenceAddress，可留空
	JavaSerializedPayload string `json:"java_serialized_payload,omitempty"` // base64，作为 javaSerializedData 返回
	LDAPReferral          string `json:"ldap_referral,omitempty"`           // 非空则返回 referral 让客户端改连（resultCode 10）

	// RMI
	Payload     string `json:"payload,omitempty"`      // base64，收到客户端数据后发送
	PayloadFile string `json:"payload_file,omitempty"` // payload 目录下的文件，优先于 Payload

	// HTTP（JNDI CodeBase 服务）
	HTTPStatus      int    `json:"http_status,omitempty"`
	HTTPContentType string `json:"http_content_type,omitempty"`
	HTTPBody        string `json:"http_body,omitempty"`
	ServePayloadDir bool   `json:"serve_payload_dir,omitempty"`
}

// ListenerView 供 API 返回：配置 + 运行状态
type ListenerView struct {
	Listener
	Running   bool   `json:"running"`
	StartedAt string `json:"started_at,omitempty"`
	Error     string `json:"error,omitempty"`
	Sessions  int    `json:"sessions"`
	Active    int    `json:"active"`
	TotalIn   int64  `json:"total_in"`
	TotalOut  int64  `json:"total_out"`
	RuleHits  int64  `json:"rule_hits"`
	Address   string `json:"address,omitempty"`
}

// SessionView 供 API 返回的会话信息
type SessionView struct {
	ID         string `json:"id"`
	ListenerID string `json:"listener_id"`
	ListenerNm string `json:"listener_name"`
	Mode       string `json:"mode"`
	Remote     string `json:"remote"`
	StartedAt  string `json:"started_at"`
	BytesIn    int64  `json:"bytes_in"`
	BytesOut   int64  `json:"bytes_out"`
	Attached   bool   `json:"attached"`
	Closed     bool   `json:"closed"`
	Cmds       int    `json:"auto_cmds,omitempty"`
}

// Event 监听上的事件流水（连接、规则命中、关闭）
type Event struct {
	Time   string `json:"time"`
	Type   string `json:"type"` // connect/rule/payload/close/error/info
	Remote string `json:"remote,omitempty"`
	Detail string `json:"detail,omitempty"`
}

const (
	eventConnect = "connect"
	eventRule    = "rule"
	eventPayload = "payload"
	eventClose   = "close"
	eventError   = "error"
	eventInfo    = "info"
)

// maxSessionsPerListener 每个监听保留的会话数（含已关闭，供回看历史输出）
const maxSessionsPerListener = 120

// maxEventLog 每个监听保留的事件条数
const maxEventLog = 200

// maxHistoryBytes 单个会话保留的输出字节数（浏览器重新挂上来时回放）
const maxHistoryBytes = 512 * 1024
