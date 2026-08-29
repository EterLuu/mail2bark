package mail2bark

import "testing"

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
