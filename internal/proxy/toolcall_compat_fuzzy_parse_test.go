package proxy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseToolcallCompatInvokeXML_FuzzyAttrs(t *testing.T) {
	tests := []struct {
		name   string
		invoke string
		want   toolcallCompatInvokeCall
	}{
		{
			name: "single_quotes",
			invoke: strings.Join([]string{
				`<invoke name='get_weather'>`,
				`<parameter name='city'>SF</parameter>`,
				`<parameter name='days'>3</parameter>`,
				`</invoke>`,
			}, "\n"),
			want: toolcallCompatInvokeCall{
				ToolName: "get_weather",
				ArgsJSON: `{"city":"SF","days":3}`,
			},
		},
		{
			name: "unquoted",
			invoke: strings.Join([]string{
				`<invoke name=get_weather>`,
				`<parameter name=city>SF</parameter>`,
				`<parameter name=days>3</parameter>`,
				`</invoke>`,
			}, "\n"),
			want: toolcallCompatInvokeCall{
				ToolName: "get_weather",
				ArgsJSON: `{"city":"SF","days":3}`,
			},
		},
		{
			name: "whitespace_variants",
			invoke: strings.Join([]string{
				`<invoke    name =  "get_weather"   >`,
				`<parameter   name = city >SF</parameter>`,
				`</invoke>`,
			}, "\n"),
			want: toolcallCompatInvokeCall{
				ToolName: "get_weather",
				ArgsJSON: `{"city":"SF"}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseToolcallCompatInvokeXML(tt.invoke)
			if !ok {
				t.Fatalf("expected parse ok")
			}
			if got.ToolName != tt.want.ToolName {
				t.Fatalf("tool name mismatch: got %q want %q", got.ToolName, tt.want.ToolName)
			}

			var gotArgs map[string]any
			var wantArgs map[string]any
			if err := json.Unmarshal([]byte(got.ArgsJSON), &gotArgs); err != nil {
				t.Fatalf("unmarshal got args: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.want.ArgsJSON), &wantArgs); err != nil {
				t.Fatalf("unmarshal want args: %v", err)
			}

			if !reflect.DeepEqual(gotArgs, wantArgs) {
				t.Fatalf("args mismatch: got %s want %s", got.ArgsJSON, tt.want.ArgsJSON)
			}
		})
	}
}

