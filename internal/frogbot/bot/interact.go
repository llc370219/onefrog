package bot

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"onefrog/internal/frogbot/onefrog"
)

// 和界面交互相关的操作：重新生成、编辑重发、表情回应、上传附件、打开文件。

// lastExchange 返回最后一条用户消息，以及它后面聊天层发出的文字回复。
func (b *Bot) lastExchange() (user Message, replies []string, err error) {
	msgs := b.store.Messages()
	ui := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && !msgs[i].Hidden {
			ui = i
			break
		}
	}
	if ui < 0 {
		return Message{}, nil, errors.New("还没有可以重新生成的消息")
	}
	for _, m := range msgs[ui+1:] {
		if m.Role == "assistant" && !m.Hidden && m.Card == nil && m.Secret == nil {
			replies = append(replies, m.ID)
		}
	}
	return msgs[ui], replies, nil
}

func (b *Bot) chatBusy() bool {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return b.turnCancel != nil
}

// Regenerate 让聊天层重新回答最后一条用户消息（旧回复从对话里隐藏；已经派出的任务不受影响）。
func (b *Bot) Regenerate() error {
	if b.chatBusy() {
		return errors.New("正在回复中，稍等一下")
	}
	u, replies, err := b.lastExchange()
	if err != nil {
		return err
	}
	if len(replies) > 0 {
		b.store.Append("message.hidden", replies, false)
	}
	b.notify(fmt.Sprintf("用户点了「重新生成」：请换一种方式重新回答用户最后一条消息「%s」。之前因为这条消息派出的任务仍然有效，不要重复派出。", firstLine(u.Text, 200)))
	return nil
}

// EditLast 编辑并重发最后一条用户消息：原消息和它后面的回复从对话里隐藏，再当成新消息发送。
func (b *Bot) EditLast(id, text string) (Message, error) {
	u, replies, err := b.lastExchange()
	if err != nil {
		return Message{}, err
	}
	if u.ID != id {
		return Message{}, errors.New("只能编辑最后一条消息")
	}
	if strings.TrimSpace(text) == "" {
		return Message{}, errors.New("消息为空")
	}
	if b.chatBusy() {
		b.StopReply()
	}
	b.store.Append("message.hidden", append([]string{u.ID}, replies...), false)
	b.contextNote(fmt.Sprintf("用户修改了上一条消息（原文：「%s」）。如果之前因为原文派出了任务而新要求不同，问用户要不要停掉旧任务，不要自作主张。", firstLine(u.Text, 200)))
	return b.Send(text, u.Files...)
}

// React 用户给某条消息回应表情（再点一次同样的表情 = 取消）。只记进上下文，不叫醒聊天层。
func (b *Bot) React(id, emoji string) error {
	m, ok := b.store.Message(id)
	if !ok || m.Role == "notice" {
		return errors.New("没有这条消息")
	}
	if emoji == "" || utf8.RuneCountInString(emoji) > 4 {
		return errors.New("需要一个表情")
	}
	on := true
	for _, r := range m.Reactions {
		if r.By == "user" && r.Emoji == emoji {
			on = false
		}
	}
	b.store.Append("message.reaction", map[string]any{"id": id, "by": "user", "emoji": emoji, "on": on}, false)
	return nil
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}._ -]+`)

const maxUpload = 25 << 20

// Upload 把用户上传的文件存进工作区的 uploads/ 目录，返回附件信息。
func (b *Bot) Upload(name string, r io.Reader) (Attachment, error) {
	name = strings.TrimSpace(unsafeName.ReplaceAllString(filepath.Base(name), "_"))
	if name == "" || name == "." || strings.HasPrefix(name, ".") {
		name = "file" + name
	}
	dir := filepath.Join(b.cfg.Workspace, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Attachment{}, err
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	final := name
	for i := 2; ; i++ { // 重名就加编号，不覆盖
		if _, err := os.Stat(filepath.Join(dir, final)); os.IsNotExist(err) {
			break
		}
		final = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	f, err := os.OpenFile(filepath.Join(dir, final), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return Attachment{}, err
	}
	n, err := io.Copy(f, io.LimitReader(r, maxUpload+1))
	f.Close()
	if err == nil && n > maxUpload {
		err = fmt.Errorf("文件太大（最多 %d MB）", maxUpload>>20)
	}
	if err != nil {
		os.Remove(filepath.Join(dir, final))
		return Attachment{}, err
	}
	return Attachment{Path: path.Join("uploads", final), Name: final, Size: n, Mime: mimeOf(final)}, nil
}

func mimeOf(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	return "application/octet-stream"
}

// serveFile 让界面打开/下载工作区里的文件（agent 生成的产物、上传的附件）。路径不能越出工作区。
func (b *Bot) serveFile(w http.ResponseWriter, r *http.Request) {
	p, err := onefrog.ResolvePath(b.cfg.Workspace, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		http.Error(w, "文件不存在", 404)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, "打不开文件", 500)
		return
	}
	defer f.Close()
	ct := mimeOf(p)
	if strings.HasPrefix(ct, "text/html") || strings.Contains(ct, "svg") || strings.Contains(ct, "javascript") {
		ct = "text/plain; charset=utf-8" // 不在本站域名下渲染 HTML/SVG/脚本，避免 XSS
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	disp := "inline"
	if r.URL.Query().Get("download") != "" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": filepath.Base(p)}))
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// TaskAction 是界面上任务按钮的统一入口（确定性操作，不经过模型）。
func (b *Bot) TaskAction(id, action string) (string, error) {
	switch action {
	case "pause":
		return b.PauseTask(id, "user")
	case "resume":
		return b.ResumeTask(id, "")
	case "cancel":
		if err := b.CancelTask(id, "user"); err != nil {
			return "", err
		}
		t, _ := b.store.Task(id)
		b.contextNote(fmt.Sprintf("用户在界面上停止了任务「%s」(%s)。", t.Title, id))
		return "已停止。", nil
	case "retry":
		nid, err := b.RetryTask(id)
		if err == nil {
			t, _ := b.store.Task(id)
			b.contextNote(fmt.Sprintf("用户在界面上重试了任务「%s」，新任务 %s。", t.Title, nid))
		}
		return nid, err
	case "top":
		return b.Reprioritize(id, 1)
	case "up":
		pos := b.QueuePosition(id)
		if pos == 0 {
			return "", errors.New("这个任务不在排队中")
		}
		return b.Reprioritize(id, pos-1)
	}
	return "", fmt.Errorf("未知操作: %s", action)
}
