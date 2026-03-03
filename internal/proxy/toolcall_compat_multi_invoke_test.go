package proxy

import (
	"strings"
	"testing"
)

func TestExtractToolcallCompatInvokeCalls_Multi(t *testing.T) {
	trigger := "<<CALL_multi>>"

	invoke1 := strings.Join([]string{
		`<invoke name="get_weather">`,
		`<parameter name="city">SF</parameter>`,
		`</invoke>`,
	}, "\n")
	invoke2 := strings.Join([]string{
		`<invoke name="get_time">`,
		`<parameter name="tz">UTC</parameter>`,
		`</invoke>`,
	}, "\n")

	content := strings.Join([]string{
		"hello",
		"",
		trigger,
		invoke1,
		invoke2,
	}, "\n")

	plain, calls, triggered, parsed := extractToolcallCompatInvokeCalls(content, trigger)
	if !triggered {
		t.Fatalf("expected triggered")
	}
	if !parsed {
		t.Fatalf("expected parsed")
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if strings.Contains(plain, trigger) || strings.Contains(strings.ToLower(plain), "<invoke") {
		t.Fatalf("expected protocol stripped from plain, got: %q", plain)
	}
	if !strings.Contains(plain, "hello") {
		t.Fatalf("expected prefix preserved, got: %q", plain)
	}
	if calls[0].ToolName != "get_weather" || calls[1].ToolName != "get_time" {
		t.Fatalf("unexpected tool names: %#v", calls)
	}
}

