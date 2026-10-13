package llm

import "encoding/json"

// Roles of a Messages API turn.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Content block types of the Messages API.
const (
	BlockText       = "text"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

// StopReasonToolUse means the model wants a tool executed before it continues.
const StopReasonToolUse = "tool_use"

// Message is one turn of the conversation sent to or received from the model.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Block is one content block of a message.
//
// The Messages API uses a single tagged union for text, tool calls and tool
// results, so one struct with omitempty fields matches the wire format and keeps
// the request assembly in the agent readable.
type Block struct {
	Type string `json:"type"`

	// Text carries BlockText content.
	Text string `json:"text,omitempty"`

	// ID, Name and Input carry BlockToolUse content.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// ToolUseID, Content and IsError carry BlockToolResult content.
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// TextBlock builds a plain text block.
func TextBlock(text string) Block {
	return Block{Type: BlockText, Text: text}
}

// ToolResultBlock builds the reply to one tool call.
func ToolResultBlock(toolUseID, content string, isError bool) Block {
	return Block{Type: BlockToolResult, ToolUseID: toolUseID, Content: content, IsError: isError}
}

// ToolDef is a tool the model may call.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

// Request is a Messages API request body.
type Request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
}

// Response is a Messages API response body.
type Response struct {
	ID         string  `json:"id"`
	Role       string  `json:"role"`
	Model      string  `json:"model"`
	StopReason string  `json:"stop_reason"`
	Content    []Block `json:"content"`
	Usage      Usage   `json:"usage"`
}

// Usage reports the token cost of one response.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ToolCalls returns the tool_use blocks of a response, in order.
func (r *Response) ToolCalls() []Block {
	calls := make([]Block, 0, len(r.Content))
	for _, block := range r.Content {
		if block.Type == BlockToolUse {
			calls = append(calls, block)
		}
	}
	return calls
}

// Text concatenates the text blocks of a response.
func (r *Response) Text() string {
	var out string
	for _, block := range r.Content {
		if block.Type == BlockText {
			out += block.Text
		}
	}
	return out
}
