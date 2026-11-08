package bot

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"onefrog/internal/id"
	"onefrog/internal/model"
	"onefrog/internal/task"
)

const correctionPrefix = "\x00chat-correction:"
const correctionSkipped = "未执行：用户纠正了当前轮，原先尚未开始的调用已跳过。请按最新消息重新判断。"
const incompleteVerification = "Bot 未能核实这一轮是否完成，已结束回复。若可能执行过变更，请先只读核对结果。"

var errCorrectionPending = errors.New("当前轮有尚未读取的用户纠正")

func correction(entry string) (messageID, text string, ok bool) {
	raw, ok := strings.CutPrefix(entry, correctionPrefix)
	if !ok {
		return "", "", false
	}
	messageID, text, ok = strings.Cut(raw, ":")
	return messageID, text, ok && messageID != "" && text != ""
}

func hasCorrection(t task.Task) bool {
	for _, entry := range t.Guidance {
		if _, _, ok := correction(entry); ok {
			return true
		}
	}
	return false
}

// Older versions parked the missing-verification fallback without a card.
func (r *Runner) repairUnanswerableWaits() {
	unanswerable := func(t task.Task) bool {
		return t.Status == task.StatusWaitingUser && t.Pending != nil &&
			t.Pending.Kind == task.PendingUser && t.Pending.CardMessageID == ""
	}
	for _, it := range r.Book.List("") {
		if !unanswerable(it) {
			continue
		}
		_, _ = r.Book.Update(it.ID, func(t *task.Task) error {
			if unanswerable(*t) {
				t.Status, t.LastError, t.Pending = task.StatusFailed, incompleteVerification, nil
			}
			return nil
		})
	}
}

// GuideChat targets one exact live root. It never answers or approves a card.
func (r *Runner) GuideChat(ownerID, taskID, text string) (task.Task, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 1800 {
		return task.Task{}, errors.New("纠正内容需要 1–1800 字")
	}
	entry := correctionPrefix + "chat-steer-" + id.New() + ":" + text
	it, err := r.Book.Update(taskID, func(t *task.Task) error {
		if t.OwnerID != ownerID || t.ParentID != "" {
			return task.ErrNotFound
		}
		if t.Status != task.StatusRunning && (t.Status != task.StatusQueued || t.StartedAt.IsZero() || t.Pending != nil) {
			return errors.New("这一轮已不在处理中，请检查当前状态；这条消息尚未发送")
		}
		if len(t.Guidance) >= task.MaxGuidance {
			return errors.New("当前轮待处理的补充已满，请稍后再发")
		}
		t.Guidance = append(t.Guidance, entry)
		return nil
	})
	if err == nil {
		r.Wake()
	}
	return it, err
}

// A crash between the stores retries the same transcript ID.
func persistCorrection(ctx context.Context, a *Agent, session, messageID, text string) error {
	rows, err := a.Store.ListMessages(ctx, session)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID == messageID {
			return nil
		}
	}
	_, err = a.Store.AddMessage(ctx, model.Message{ID: messageID, SessionID: session, Role: "user", Content: text})
	return err
}
