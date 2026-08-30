package mail2bark

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

type Alert struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	Severity  string `json:"severity"`
	Device    string `json:"device"`
	Component string `json:"component"`
	Event     string `json:"event"`
	Detail    string `json:"detail"`
	Timestamp string `json:"timestamp"`
}

var tagRE = regexp.MustCompile(`(?s)<[^>]*>`)
var spaceRE = regexp.MustCompile(`[ \t]+`)

func ParseAlert(raw []byte, fallbackSubject, fallbackSender string) Alert {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return basicAlert(fallbackSubject, fallbackSender, string(raw))
	}
	subject := decodeHeaderValue(msg.Header.Get("Subject"))
	if subject == "" {
		subject = fallbackSubject
	}
	body := readBody(msg)
	a := extractAlert(body)
	if a.Event == "" {
		a.Event = subject
	}
	if a.Detail == "" {
		a.Detail = body
	}
	a.Severity = severity(subject+"\n"+body, a.Severity)
	if a.Device == "" {
		a.Device = fallbackSender
	}
	a.Title = buildTitle(a)
	a.Detail = trimText(a.Detail, 1800)
	a.Body = renderBody(a)
	return a
}
func basicAlert(subject, sender, body string) Alert {
	a := Alert{Title: subject, Detail: trimText(body, 1800), Device: sender}
	a.Severity = severity(subject+"\n"+body, "")
	a.Event = subject
	a.Title = buildTitle(a)
	a.Body = renderBody(a)
	return a
}
func readBody(m *mail.Message) string {
	ct := m.Header.Get("Content-Type")
	med, params, _ := mime.ParseMediaType(ct)
	if strings.HasPrefix(med, "multipart/") {
		mr := multipart.NewReader(m.Body, params["boundary"])
		var plain, htmlBody string
		for {
			p, e := mr.NextPart()
			if e != nil {
				break
			}
			body, _ := decodePartBody(p, p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"))
			pm, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			if pm == "text/plain" {
				plain = body
			} else if pm == "text/html" {
				htmlBody = body
			}
		}
		if plain != "" {
			return cleanText(plain)
		}
		return cleanText(htmlBody)
	}
	body, _ := decodePartBody(m.Body, ct, m.Header.Get("Content-Transfer-Encoding"))
	if med == "text/html" {
		return cleanText(body)
	}
	return cleanText(body)
}

func decodeHeaderValue(value string) string {
	if value == "" {
		return ""
	}
	decoded, err := (&mime.WordDecoder{CharsetReader: charsetReader}).DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	name := strings.ToLower(strings.TrimSpace(charset))
	var decoder *encoding.Decoder
	switch name {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	case "gbk", "x-gbk", "cp936", "ms936":
		decoder = simplifiedchinese.GBK.NewDecoder()
	case "gb18030":
		decoder = simplifiedchinese.GB18030.NewDecoder()
	case "big5", "big-5", "cp950":
		decoder = traditionalchinese.Big5.NewDecoder()
	case "iso-8859-1", "latin1", "latin-1":
		decoder = charmap.ISO8859_1.NewDecoder()
	case "windows-1252", "cp1252":
		decoder = charmap.Windows1252.NewDecoder()
	default:
		return input, nil
	}
	return transform.NewReader(input, decoder), nil
}

func decodePartBody(body io.Reader, contentType, transferEncoding string) (string, error) {
	var decoded io.Reader = body
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		decoded = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		decoded = quotedprintable.NewReader(body)
	}
	raw, err := io.ReadAll(decoded)
	if err != nil {
		return "", err
	}
	_, params, _ := mime.ParseMediaType(contentType)
	reader, err := charsetReader(params["charset"], bytes.NewReader(raw))
	if err != nil {
		return string(raw), err
	}
	converted, err := io.ReadAll(reader)
	return string(converted), err
}
func cleanText(v string) string {
	v = html.UnescapeString(tagRE.ReplaceAllString(v, " "))
	lines := strings.Split(strings.ReplaceAll(v, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(spaceRE.ReplaceAllString(line, " "))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ">") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "-- ") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
func extractAlert(body string) Alert {
	var a Alert
	for _, line := range strings.Split(body, "\n") {
		p := strings.SplitN(line, ":", 2)
		if len(p) != 2 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(p[0]))
		v := strings.TrimSpace(p[1])
		switch k {
		case "severity", "level", "priority", "级别", "严重性":
			a.Severity = v
		case "device", "host", "server", "hostname", "设备", "主机":
			a.Device = v
		case "component", "组件":
			a.Component = v
		case "event", "alert", "事件", "告警":
			a.Event = v
		case "message", "description", "details", "detail", "消息", "描述", "详情":
			a.Detail = v
		case "timestamp", "time", "date", "时间":
			a.Timestamp = v
		}
	}
	return a
}
func severity(text, hinted string) string {
	x := strings.ToUpper(hinted + " " + text)
	switch {
	case strings.Contains(x, "CRITICAL") || strings.Contains(x, "FATAL") || strings.Contains(x, "ERROR") || strings.Contains(x, "FAILED") || strings.Contains(x, "DOWN") || strings.Contains(x, "故障") || strings.Contains(x, "错误") || strings.Contains(x, "严重"):
		return "CRITICAL"
	case strings.Contains(x, "WARNING") || strings.Contains(x, "WARN") || strings.Contains(x, "警告"):
		return "WARNING"
	case strings.Contains(x, "RECOVER") || strings.Contains(x, "恢复"):
		return "RECOVERED"
	default:
		return "INFO"
	}
}
func buildTitle(a Alert) string {
	bits := []string{"[" + a.Severity + "]"}
	if a.Device != "" {
		bits = append(bits, a.Device)
	}
	if a.Event != "" {
		bits = append(bits, "-", a.Event)
	}
	return trimText(strings.Join(bits, " "), 180)
}
func renderBody(a Alert) string {
	var b strings.Builder
	fmt.Fprintf(&b, "级别: %s\n", a.Severity)
	if a.Device != "" {
		fmt.Fprintf(&b, "设备: %s\n", a.Device)
	}
	if a.Component != "" {
		fmt.Fprintf(&b, "组件: %s\n", a.Component)
	}
	if a.Event != "" {
		fmt.Fprintf(&b, "事件: %s\n", a.Event)
	}
	if a.Timestamp != "" {
		fmt.Fprintf(&b, "时间: %s\n", a.Timestamp)
	}
	b.WriteString("\n详情:\n")
	b.WriteString(a.Detail)
	return trimText(b.String(), 2000)
}
func trimText(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
