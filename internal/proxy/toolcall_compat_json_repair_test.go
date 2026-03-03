package proxy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseToolcallCompatInvokeXML_JSONRepair(t *testing.T) {
	tests := []struct {
		name     string
		rawValue string
		want     map[string]any
	}{
		{
			name:     "trailing_comma",
			rawValue: `{"a":1,}`,
			want:     map[string]any{"a": float64(1)},
		},
		{
			name:     "unclosed_object",
			rawValue: `{"a":1`,
			want:     map[string]any{"a": float64(1)},
		},
		{
			name:     "prefix_suffix_garbage",
			rawValue: `some text {"a":1} more text`,
			want:     map[string]any{"a": float64(1)},
		},
		{
			name: "string_newline",
			rawValue: strings.Join([]string{
				`{"a":"line1`,
				`line2"}`,
			}, "\n"),
			want: map[string]any{"a": "line1\nline2"},
		},
		{
			name:     "nested_unclosed",
			rawValue: `{"a":{"b":1}`,
			want: map[string]any{
				"a": map[string]any{
					"b": float64(1),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invoke := strings.Join([]string{
				`<invoke name="tool">`,
				`<parameter name="arg">` + tt.rawValue + `</parameter>`,
				`</invoke>`,
			}, "\n")

			call, ok := parseToolcallCompatInvokeXML(invoke)
			if !ok {
				t.Fatalf("expected parse ok")
			}

			var got map[string]any
			if err := json.Unmarshal([]byte(call.ArgsJSON), &got); err != nil {
				t.Fatalf("unmarshal args: %v", err)
			}

			arg, ok := got["arg"]
			if !ok {
				t.Fatalf("missing arg in args: %v", got)
			}

			argObj, ok := arg.(map[string]any)
			if !ok {
				encoded, _ := json.Marshal(arg)
				t.Fatalf("expected arg object, got %T: %s", arg, string(encoded))
			}

			if !reflect.DeepEqual(argObj, tt.want) {
				t.Fatalf("arg mismatch: got %#v want %#v (full=%s)", argObj, tt.want, call.ArgsJSON)
			}
		})
	}
}
