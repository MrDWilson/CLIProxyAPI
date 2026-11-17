package helps

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// Limits of Claude Code 2.1.325's x-claude-code-sandy-plover (tools used) header.
const (
	claudeToolsUsedMaxEntries = 64
	claudeToolsUsedMaxBytes   = 7168
)

// claudeToolsUsedCoreTools are the built-in tools 2.1.325 lists after every
// other tool in the tools-used header.
var claudeToolsUsedCoreTools = map[string]bool{
	"Bash":          true,
	"Read":          true,
	"Edit":          true,
	"Write":         true,
	"Glob":          true,
	"Grep":          true,
	"NotebookEdit":  true,
	"TodoWrite":     true,
	"Agent":         true,
	"Task":          true,
	"ToolSearch":    true,
	"EnterPlanMode": true,
	"ExitPlanMode":  true,
}

// claudeToolsUsedRecord is one entry of the tools-used header.
type claudeToolsUsedRecord struct {
	Name string `json:"name"`
	MCP  bool   `json:"mcp,omitempty"`
}

// ClaudeToolsUsedHeaderValue rebuilds 2.1.325's x-claude-code-sandy-plover
// value from the transcript a request carries: a JSON array with one record per
// distinct tool the conversation has called (tool_use blocks), in first-use
// order, with the built-in core tools moved after the others. MCP tools are
// marked with "mcp":true, as Claude Code marks them. A conversation without
// tool calls yields "[]".
//
// Claude Code keeps this list per session and can attach connector URLs to MCP
// records; a stateless proxy only sees the transcript, so the records carry the
// name (and MCP mark) alone. The entry and byte caps match 2.1.325; its
// truncation marker for longer lists is not reproduced.
func ClaudeToolsUsedHeaderValue(body []byte) string {
	seen := make(map[string]bool)
	var names []string
	gjson.GetBytes(body, "messages").ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() != "assistant" {
			return true
		}
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "tool_use" {
				return true
			}
			name := block.Get("name").String()
			if name == "" || seen[name] {
				return true
			}
			seen[name] = true
			names = append(names, name)
			return true
		})
		return true
	})

	ordered := make([]string, 0, len(names))
	for _, name := range names {
		if !claudeToolsUsedCoreTools[name] {
			ordered = append(ordered, name)
		}
	}
	for _, name := range names {
		if claudeToolsUsedCoreTools[name] {
			ordered = append(ordered, name)
		}
	}

	records := make([]claudeToolsUsedRecord, 0, len(ordered))
	size := len("[]")
	for _, name := range ordered {
		if len(records) == claudeToolsUsedMaxEntries {
			break
		}
		record := claudeToolsUsedRecord{Name: name, MCP: strings.HasPrefix(name, "mcp__")}
		added := len(encodeClaudeToolsUsedJSON(record))
		if len(records) > 0 {
			added++
		}
		if size+added > claudeToolsUsedMaxBytes {
			break
		}
		records = append(records, record)
		size += added
	}
	return encodeClaudeToolsUsedJSON(records)
}

// encodeClaudeToolsUsedJSON marshals v like JSON.stringify: compact and
// without Go's HTML escaping.
func encodeClaudeToolsUsedJSON(v any) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if errEncode := encoder.Encode(v); errEncode != nil {
		return "[]"
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
