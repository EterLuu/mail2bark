package mail2bark

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func TestParseAlert(t *testing.T) {
	raw := []byte("Subject: [CRITICAL] R740 Fan alert\nFrom: idrac@example\nContent-Type: text/plain; charset=utf-8\n\nSeverity: CRITICAL\nDevice: R740\nComponent: Fan 1\nEvent: Fan redundancy lost\n")
	a := ParseAlert(raw, "fallback", "sender")
	if a.Severity != "CRITICAL" || a.Device != "R740" || a.Component != "Fan 1" {
		t.Fatalf("unexpected alert: %+v", a)
	}
	if a.Title == "" || a.Body == "" {
		t.Fatal("title/body must be rendered")
	}
}

func TestParseHTMLFallback(t *testing.T) {
	raw := []byte("Subject: Warning\nContent-Type: text/html\n\n<html><body><b>WARNING</b>: disk degraded</body></html>")
	a := ParseAlert(raw, "", "host")
	if a.Severity != "WARNING" || a.Detail == "" {
		t.Fatalf("unexpected html alert: %+v", a)
	}
}

func TestParseEncodedChineseMail(t *testing.T) {
	subject := mime.QEncoding.Encode("UTF-8", "磁盘告警")
	body := base64.StdEncoding.EncodeToString([]byte("级别: 严重\n设备: 数据库-01\n事件: 磁盘空间不足"))
	raw := []byte(fmt.Sprintf("Subject: %s\r\nFrom: =?UTF-8?B?5rWL6K+V5Z+65Zmo?= <monitor@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n", subject, body))

	alert := ParseAlert(raw, "fallback", "sender")
	if alert.Severity != "CRITICAL" || alert.Device != "数据库-01" || alert.Event != "磁盘空间不足" {
		t.Fatalf("encoded Chinese mail was not decoded: %+v", alert)
	}
	if !strings.Contains(alert.Detail, "磁盘空间不足") || strings.Contains(alert.Detail, "=E7") {
		t.Fatalf("decoded detail is incorrect: %q", alert.Detail)
	}
}

func TestParseGBKMail(t *testing.T) {
	gbkBody, err := io.ReadAll(transform.NewReader(strings.NewReader("级别: 警告\n设备: 服务器-01\n事件: 磁盘空间不足"), simplifiedchinese.GBK.NewEncoder()))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(fmt.Sprintf("Subject: =?GBK?B?xOO6w8fU?=\r\nContent-Type: text/plain; charset=GBK\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s", gbkBody))
	alert := ParseAlert(raw, "fallback", "sender")
	if alert.Severity != "WARNING" || alert.Device != "服务器-01" || alert.Event != "磁盘空间不足" {
		t.Fatalf("GBK mail was not decoded: %+v", alert)
	}
}
