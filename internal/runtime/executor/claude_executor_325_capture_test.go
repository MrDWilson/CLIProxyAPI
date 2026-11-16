package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claude325Capture is one sanitized Claude Code 2.1.325 main turn from
// testdata/claude_code_2_1_325_captures.json.
type claude325Capture struct {
	Name    string            `json:"name"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

type claude325Captures struct {
	MainTurns []claude325Capture `json:"main_turns"`
	MultiTurn []claude325Capture `json:"multi_turn"`
}

func loadClaude325Captures(t *testing.T) claude325Captures {
	t.Helper()
	raw, errRead := os.ReadFile("testdata/claude_code_2_1_325_captures.json")
	if errRead != nil {
		t.Fatal(errRead)
	}
	var captures claude325Captures
	if errDecode := json.Unmarshal(raw, &captures); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(captures.MainTurns) == 0 || len(captures.MultiTurn) != 3 {
		t.Fatalf("unexpected fixture shape: %d main turns, %d multi-turn requests", len(captures.MainTurns), len(captures.MultiTurn))
	}
	return captures
}

func (captures claude325Captures) all() []claude325Capture {
	return append(append([]claude325Capture(nil), captures.MainTurns...), captures.MultiTurn...)
}

func (c claude325Capture) header() http.Header {
	headers := make(http.Header, len(c.Headers))
	for name, value := range c.Headers {
		headers.Set(name, value)
	}
	return headers
}

var claude325GatewayHints = []string{
	claude1PRequestClassHeader,
	claudeRequestClassHeader,
	claudePromptIDHeader,
	claudeToolsUsedHeader,
}

// The cloaked beta header for each captured 2.1.325 main-turn body must equal
// the header real Claude Code sent with it, and the gateway hints must carry
// the captured values.
func TestClaudeCodeCLIBetas_Matches2_1_325Captures(t *testing.T) {
	captures := loadClaude325Captures(t)
	for _, capture := range captures.all() {
		t.Run(capture.Name, func(t *testing.T) {
			want := capture.Headers["anthropic-beta"]
			if got := claudeCodeCLIBetas(capture.Body, nil, true); got != want {
				t.Fatalf("claudeCodeCLIBetas() =\n%s\nwant\n%s", got, want)
			}

			const token = "sk-ant-oat-capture-fixture"
			auth := &cliproxyauth.Auth{ID: "capture-fixture", Metadata: map[string]any{"access_token": token}}
			req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
			if errReq != nil {
				t.Fatal(errReq)
			}
			if errHeaders := applyClaudeHeaders(req, auth, token, true, nil, capture.Body, &config.Config{}, nil, false); errHeaders != nil {
				t.Fatal(errHeaders)
			}
			if got := req.Header.Get("Anthropic-Beta"); got != want {
				t.Errorf("cloaked Anthropic-Beta =\n%s\nwant\n%s", got, want)
			}
			for _, name := range claude325GatewayHints {
				if got, want := req.Header.Get(name), capture.header().Get(name); got != want {
					t.Errorf("%s = %q, want captured %q", name, got, want)
				}
			}
		})
	}
}

const claude325StreamEvents = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_325","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// A genuine 2.1.325 client's gateway hints reach Anthropic unchanged, both on
// native passthrough and on the confirmed-client path.
func TestClaudeExecutor_Forwards2_1_325GatewayHintsUnchanged(t *testing.T) {
	captures := loadClaude325Captures(t)
	for _, passthrough := range []bool{true, false} {
		for _, capture := range captures.all() {
			t.Run(fmt.Sprintf("passthrough=%t/%s", passthrough, capture.Name), func(t *testing.T) {
				headers := capture.header()
				detection := helps.DetectClaudeCodeRequest(headers, capture.Body, false)
				if !detection.NativePassthrough || !detection.Confirmed {
					t.Fatalf("captured client is not a confirmed native client: %#v", detection)
				}
				var sentHeaders http.Header
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					_, _ = io.Copy(io.Discard, req.Body)
					sentHeaders = req.Header.Clone()
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(claude325StreamEvents)), Request: req}, nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				executor := NewClaudeExecutor(&config.Config{ClaudeNativePassthrough: passthrough})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "local-test-key", "auth_kind": cliproxyauth.AuthKindAPIKey}}
				model := gjson.GetBytes(capture.Body, "model").String()
				result, errStream := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: capture.Body}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: capture.Body, Headers: headers})
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
				for _, name := range claude325GatewayHints {
					if got, want := helps.HeaderValueCaseInsensitive(sentHeaders, name), headers.Get(name); got != want {
						t.Errorf("%s = %q, want forwarded %q", name, got, want)
					}
				}
			})
		}
	}
}

var claude325BillingPattern = regexp.MustCompile(`^x-anthropic-billing-header: cc_version=2\.1\.325\.[0-9a-f]{3}; cc_entrypoint=cli; cch=[0-9a-f]{5};` +
	`(?: cc_prev_req=(req_[A-Za-z0-9_]+);)? cc_prompt_id=([0-9a-f-]{36}); cc_turn_origin=human; cc_prompt_index=(\d+); cc_turn_index=(\d+);$`)

type claude325BillingFields struct {
	prevReq, promptID, promptIndex, turnIndex string
}

func parseClaude325Billing(t *testing.T, text string) claude325BillingFields {
	t.Helper()
	match := claude325BillingPattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("billing header does not follow the 2.1.325 field order: %s", text)
	}
	return claude325BillingFields{prevReq: match[1], promptID: match[2], promptIndex: match[3], turnIndex: match[4]}
}

// A cloaked client replaying the captured multi-turn transcript gets the same
// turn positions, prompt-id chaining and gateway hints as real 2.1.325: the
// tool loop keeps prompt 1/1 with cc_prev_req, the second prompt moves to 2/2.
func TestClaudeExecutor_Cloaked2_1_325MultiTurnBillingAndGatewayHints(t *testing.T) {
	captures := loadClaude325Captures(t)

	var sentBodies [][]byte
	var sentHeaders []http.Header
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		sentBodies = append(sentBodies, body)
		sentHeaders = append(sentHeaders, req.Header.Clone())
		n := len(sentBodies)
		response := fmt.Sprintf(`{"id":"msg_cloaked_%d","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`, n)
		header := http.Header{"Content-Type": {"application/json"}, "Request-Id": {fmt.Sprintf("req_cloaked_turn_%d", n)}}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	testID := uuid.NewString()
	auth := &cliproxyauth.Auth{
		ID:         "cloaked-325-" + testID,
		Attributes: map[string]string{"api_key": "sk-ant-oat-cloaked-325", "cloak_mode": "always"},
		Metadata:   claudeOAuthTestMetadata(),
	}
	executor := NewClaudeExecutor(&config.Config{})
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "cloaked-325-" + testID},
	}

	for _, capture := range captures.MultiTurn {
		// A third-party client sends only its own transcript: the user and
		// assistant turns, without Claude Code's system prompt or role=system turns.
		payload := []byte(`{"model":"claude-opus-5-5","max_tokens":1024,"messages":[]}`)
		gjson.GetBytes(capture.Body, "messages").ForEach(func(_, message gjson.Result) bool {
			if role := message.Get("role").String(); role == "user" || role == "assistant" {
				payload, _ = sjson.SetRawBytes(payload, "messages.-1", []byte(message.Raw))
			}
			return true
		})
		if _, errExecute := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, options); errExecute != nil {
			t.Fatalf("%s: %v", capture.Name, errExecute)
		}
	}
	if len(sentBodies) != len(captures.MultiTurn) {
		t.Fatalf("sent %d requests, want %d", len(sentBodies), len(captures.MultiTurn))
	}

	var got []claude325BillingFields
	for i, capture := range captures.MultiTurn {
		fields := parseClaude325Billing(t, gjson.GetBytes(sentBodies[i], "system.0.text").String())
		captured := parseClaude325Billing(t, gjson.GetBytes(capture.Body, "system.0.text").String())
		if fields.promptIndex != captured.promptIndex || fields.turnIndex != captured.turnIndex {
			t.Errorf("%s: cc_prompt_index/cc_turn_index = %s/%s, want captured %s/%s", capture.Name, fields.promptIndex, fields.turnIndex, captured.promptIndex, captured.turnIndex)
		}
		if (fields.prevReq == "") != (captured.prevReq == "") {
			t.Errorf("%s: cc_prev_req = %q, captured presence %q", capture.Name, fields.prevReq, captured.prevReq)
		}
		for _, name := range []string{claude1PRequestClassHeader, claudeRequestClassHeader, claudeToolsUsedHeader} {
			if got, want := helps.HeaderValueCaseInsensitive(sentHeaders[i], name), capture.header().Get(name); got != want {
				t.Errorf("%s: %s = %q, want captured %q", capture.Name, name, got, want)
			}
		}
		if got := helps.HeaderValueCaseInsensitive(sentHeaders[i], claudePromptIDHeader); got != fields.promptID {
			t.Errorf("%s: %s = %q, want the billing cc_prompt_id %q", capture.Name, claudePromptIDHeader, got, fields.promptID)
		}
		got = append(got, fields)
	}
	if got[1].prevReq != "req_cloaked_turn_1" || got[2].prevReq != "req_cloaked_turn_2" {
		t.Errorf("cc_prev_req chain = %q, %q; want req_cloaked_turn_1, req_cloaked_turn_2", got[1].prevReq, got[2].prevReq)
	}
	if got[0].promptID != got[1].promptID {
		t.Errorf("tool loop changed the prompt id: %s -> %s", got[0].promptID, got[1].promptID)
	}
	if got[2].promptID == got[1].promptID {
		t.Errorf("second prompt reused prompt id %s", got[2].promptID)
	}
}
