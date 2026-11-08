package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"onefrog/internal/model"
	"onefrog/internal/task"
)

// Chat queue metadata lives in the existing extensible options JSON so older
// versions can still decode the task registry. Only validated attachment refs
// are persisted here; neither payloads nor credentials leave the checkpoint.
type chatQueueOptions struct {
	OrderedChat bool               `json:"ordered_chat,omitempty"`
	Attachments []model.Attachment `json:"chat_attachments,omitempty"`
}

func chatQueueOptionsOf(t task.Task) chatQueueOptions {
	var opt chatQueueOptions
	_ = json.Unmarshal([]byte(t.OptionsJSON), &opt)
	return opt
}

// ChatQueueMessage identifies the single transcript entry for this submission.
// A waiting turn is represented by its task until this message is written.
func ChatQueueMessage(t task.Task) (string, []model.Attachment) {
	opt := chatQueueOptionsOf(t)
	if !opt.OrderedChat {
		return "", nil
	}
	return "chat-" + t.ID, opt.Attachments
}

// chatQueueHeads returns the oldest unfinished root in every ordered
// conversation. A parked turn still owns its place until answered or stopped.
// Other sessions and delegated child sessions keep their own concurrency.
func chatQueueHeads(items []task.Task) map[string]string {
	ordered := make(map[string]bool)
	oldest := make(map[string]task.Task)
	for _, it := range items {
		if it.ParentID != "" || it.SessionID == "" || it.Terminal() {
			continue
		}
		if chatQueueOptionsOf(it).OrderedChat {
			ordered[it.SessionID] = true
		}
		cur, ok := oldest[it.SessionID]
		if !ok || it.CreatedAt.Before(cur.CreatedAt) || (it.CreatedAt.Equal(cur.CreatedAt) && it.ID < cur.ID) {
			oldest[it.SessionID] = it
		}
	}
	heads := make(map[string]string, len(ordered))
	for session := range ordered {
		heads[session] = oldest[session].ID
	}
	return heads
}

// WithdrawChat only removes a message that hasn't started. A stale UI cannot
// turn "取消排队" into a stop of a command that has already begun.
func (r *Runner) WithdrawChat(ownerID, id string) (task.Task, error) {
	it, err := r.Book.Update(id, func(t *task.Task) error {
		if t.OwnerID != ownerID || !chatQueueOptionsOf(*t).OrderedChat {
			return task.ErrNotFound
		}
		if t.Status == task.StatusCancelled && t.StartedAt.IsZero() {
			return nil
		}
		if t.Status != task.StatusQueued || !t.StartedAt.IsZero() {
			return errors.New("这条消息已不在排队，请刷新状态后再操作")
		}
		t.Status = task.StatusCancelled
		return nil
	})
	if err == nil {
		r.Wake()
	}
	return it, err
}

// openChatTurn runs under the session's dispatch lease, before the first model
// step. Read the preceding replies now, while keeping the original accepted
// system prompt, user content (including media) and execution options.
func (r *Runner) openChatTurn(ctx context.Context, it task.Task) (task.Task, error) {
	messageID, refs := ChatQueueMessage(it)
	if messageID == "" || it.TotalSteps != 0 {
		return it, nil
	}
	snapshot := it.Checkpoint.Messages
	if len(snapshot) < 2 {
		return it, errors.New("对话消息缺少已接受的内容")
	}
	system, ok := snapshot[0]["content"].(string)
	if !ok || snapshot[0]["role"] != "system" || snapshot[len(snapshot)-1]["role"] != "user" {
		return it, errors.New("对话消息的已接受内容无效")
	}
	b, err := r.Agent.Store.GetBot(ctx, it.BotID)
	if err != nil {
		return it, err
	}
	rows, err := r.Agent.Store.ListMessages(ctx, it.SessionID)
	if err != nil {
		return it, err
	}
	exists := false
	for i, row := range rows {
		if row.ID == messageID {
			// Recovery after writing the message but before the first step:
			// rebuild the same prefix without appending a duplicate.
			rows, exists = rows[:i], true
			break
		}
	}
	optCtx := context.WithValue(ctx, optionsKey{}, taskOptions(it))
	history, err := r.Agent.chatHistory(optCtx, rows, system, b.Name)
	if err != nil {
		return it, err
	}
	history = append(history, snapshot[len(snapshot)-1])
	if !exists {
		if err := ctx.Err(); err != nil {
			return it, err
		}
		if _, err := r.Agent.Store.AddMessage(ctx, model.Message{
			ID: messageID, SessionID: it.SessionID, Role: "user", Content: strings.TrimSpace(it.Goal), Attachments: refs,
		}); err != nil {
			return it, err
		}
	}
	return r.Book.Update(it.ID, func(t *task.Task) error {
		if !windowOwns(t) {
			return errNotQueued
		}
		t.Checkpoint.Messages = history
		return nil
	})
}
