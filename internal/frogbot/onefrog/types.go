// Package onefrog 是一个精简的 agent 内核：模型 <-> 工具循环、流式事件、
// Codex 风格的分层审批，以及留给聊天层（Onefrog）的接口。
package onefrog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// ---------- 模型 ----------

// Message 是一条对话消息（与 OpenAI 格式接近，但与具体厂商无关）。
type Message struct {
	Role       string     `json:"role"` // system / user / assistant / tool
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// ProviderItems carries opaque continuation items for Responses/OAuth.
	ProviderItems []json.RawMessage `json:"provider_items,omitempty"`
}

// ToolCall 是模型发起的一次工具调用。
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolSpec 是发给模型的工具说明书。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolSpec
	// OnReasoning 收到模型「思考」片段时回调（reasoning_content / reasoning / <think>）。
	// 思考内容只用于展示，不会写回 Message，也就不会喂回模型历史。
	OnReasoning func(string) `json:"-"`
}

// Provider 是模型厂商的抽象。onText 用来流式输出文字（可以为 nil）。
// 返回完整的 assistant 消息（包含工具调用）。以后加 Anthropic 只需再实现这个接口。
type Provider interface {
	Chat(ctx context.Context, req ChatRequest, onText func(delta string)) (Message, error)
}

// ---------- 任务 ----------

type Task struct {
	ID           string        `json:"id"`
	BotID        string        `json:"bot_id,omitempty"`
	Instructions string        `json:"instructions,omitempty"` // 追加到系统提示词（人设、要求）
	Input        string        `json:"input"`
	History      []Message     `json:"history,omitempty"` // 可选：之前的上下文
	Workspace    string        `json:"workspace"`
	Policy       Policy        `json:"policy,omitempty"`
	MaxSteps     int           `json:"max_steps,omitempty"`    // 默认 50
	MaxDuration  time.Duration `json:"max_duration,omitempty"` // 0 = 不限
	// Resume 是暂停/中断时保存的对话（不含系统提示词）；非空时从这里接着跑，而不是从 Input 重新开始。
	Resume    []Message `json:"resume,omitempty"`
	StartStep int       `json:"start_step,omitempty"` // 恢复时已经用掉的步数
}

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusPaused    Status = "paused" // 在安全点暂停，可以用 Resume 接着跑
)

type Result struct {
	TaskID string `json:"task_id"`
	Status Status `json:"status"`
	Output string `json:"output,omitempty"`
	Steps  int    `json:"steps"`
	Error  string `json:"error,omitempty"`
}

// ---------- 事件 ----------

type EventType string

const (
	EvTaskStarted       EventType = "task.started"
	EvTextDelta         EventType = "text.delta"
	EvThinkingDelta     EventType = "thinking.delta"
	EvToolCall          EventType = "tool.call"
	EvApprovalRequested EventType = "approval.requested"
	EvApprovalDecided   EventType = "approval.decided"
	EvToolResult        EventType = "tool.result"
	EvSteered           EventType = "task.steered"
	EvHeartbeat         EventType = "heartbeat"
	EvTaskCompleted     EventType = "task.completed"
	EvTaskFailed        EventType = "task.failed"
	EvTaskCancelled     EventType = "task.cancelled"
	EvTaskPaused        EventType = "task.paused"     // 在安全点暂停（不是结束）
	EvCheckpoint        EventType = "task.checkpoint" // 每步结束后的对话快照，用于暂停/重启后恢复
)

// Event 可以直接 JSON 序列化，用于 SSE 推送或写入事件日志。Seq 在同一任务内递增。
type Event struct {
	Seq    int64     `json:"seq"`
	TaskID string    `json:"task_id"`
	BotID  string    `json:"bot_id,omitempty"`
	Type   EventType `json:"type"`
	Time   time.Time `json:"time"`
	Data   any       `json:"data,omitempty"`
}

// Terminal 表示任务已经结束（完成 / 失败 / 取消）。
func (e Event) Terminal() bool {
	return e.Type == EvTaskCompleted || e.Type == EvTaskFailed || e.Type == EvTaskCancelled
}

type EventSink interface{ Emit(Event) }

// SinkFunc 让普通函数也能当 EventSink。
type SinkFunc func(Event)

func (f SinkFunc) Emit(e Event) { f(e) }

// ---------- 扩展点 ----------

// ApprovalRecorder 保存审批记录（审计用）。frog bot 把它写进事件日志。
type ApprovalRecorder interface {
	SaveApproval(ctx context.Context, r ApprovalRecord) error
}

// MemoryProvider 返回要放进系统提示词的记忆文本（Bot 记忆、共享记忆等）。
type MemoryProvider interface {
	Recall(ctx context.Context, botID, input string) (string, error)
}

// Compactor 在每次请求模型前压缩对话（例如把旧内容总结成摘要）。
type Compactor interface {
	Compact(ctx context.Context, msgs []Message) ([]Message, error)
}

// NewID 生成形如 prefix_1a2b3c4d5e6f7a8b 的随机 ID。
func NewID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
