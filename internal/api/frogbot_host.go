package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"onefrog/internal/automation"
	frog "onefrog/internal/frogbot/bot"
	"onefrog/internal/frogbot/vault"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

// FrogBotID is the automation target that runs in the account's Frog Bot
// instead of a classic OneFrog Bot session.
const FrogBotID = "frog-bot"

// ---------- 保险箱：Frog Bot 直接使用 OneFrog 的密钥保险箱 ----------

// panelVault implements frog.Vault on top of the OneFrog vault (Postgres +
// the panel master key). There is no second vault file.
type panelVault struct {
	st  store.Store
	box *secret.Box

	mu     sync.Mutex
	repl   *strings.Replacer
	loaded time.Time
}

const vaultCacheTTL = 3 * time.Second

func vaultCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// usable reports whether Frog Bot can reference the name as {{secret:NAME}}.
func usable(name string) bool { return vault.ValidName(name) == nil }

// plain decrypts every vault entry. It is only used to build the redaction
// table and to resolve values at execution time.
func (v *panelVault) plain() (map[string]string, error) {
	ctx, cancel := vaultCtx()
	defer cancel()
	recs, err := v.st.AllSecrets(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(recs))
	for _, r := range recs {
		p, err := v.box.Decrypt(r.EncryptedValue)
		if err != nil {
			return nil, fmt.Errorf("密钥 %s 无法解密", r.Name)
		}
		out[r.Name] = p
	}
	return out, nil
}

func (v *panelVault) refresh() error {
	p, err := v.plain()
	if err != nil {
		return err
	}
	r := vault.NewReplacer(p)
	v.mu.Lock()
	v.repl, v.loaded = r, time.Now()
	v.mu.Unlock()
	return nil
}

func (v *panelVault) replacer() *strings.Replacer {
	v.mu.Lock()
	fresh := time.Since(v.loaded) < vaultCacheTTL
	r := v.repl
	v.mu.Unlock()
	if fresh {
		return r
	}
	if err := v.refresh(); err != nil {
		return r // keep the last table; the host redactor still masks raw values
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.repl
}

func (v *panelVault) Redact(s string) string {
	if s == "" {
		return s
	}
	if r := v.replacer(); r != nil {
		return r.Replace(s)
	}
	return s
}

func (v *panelVault) RedactBytes(b []byte) []byte {
	if r := v.replacer(); r != nil {
		return []byte(r.Replace(string(b)))
	}
	return b
}

func (v *panelVault) List() []vault.Info {
	ctx, cancel := vaultCtx()
	defer cancel()
	items, err := v.st.ListSecrets(ctx)
	if err != nil {
		return nil
	}
	out := make([]vault.Info, 0, len(items))
	for _, it := range items {
		if !usable(it.Name) {
			continue
		}
		out = append(out, vault.Info{Name: it.Name, Description: it.Description, CreatedAt: it.CreatedAt, UpdatedAt: it.CreatedAt})
	}
	return out
}

func (v *panelVault) Has(name string) bool {
	ctx, cancel := vaultCtx()
	defer cancel()
	_, err := v.st.SecretByName(ctx, name)
	return err == nil
}

func (v *panelVault) Allowed(_ string, _ json.RawMessage, names []string) error {
	for _, n := range names {
		if !v.Has(n) {
			return fmt.Errorf("密钥 %s 不存在，先用 request_secret 让用户保存", n)
		}
	}
	return nil
}

func (v *panelVault) Values(names []string) (map[string]string, error) {
	all, err := v.plain()
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.repl, v.loaded = vault.NewReplacer(all), time.Now()
	v.mu.Unlock()
	out := make(map[string]string, len(names))
	for _, n := range names {
		p, ok := all[n]
		if !ok {
			return nil, fmt.Errorf("密钥 %s 不存在", n)
		}
		out[n] = p
	}
	return out, nil
}

func (v *panelVault) Set(name, value, desc string, _ *vault.Scope) error {
	clean, err := secret.ValidName(name)
	if err != nil {
		return err
	}
	if err := secret.ValidValue(value); err != nil {
		return err
	}
	enc, err := v.box.Encrypt(value)
	if err != nil {
		return errors.New("加密失败")
	}
	ctx, cancel := vaultCtx()
	defer cancel()
	d := secret.CleanDescription(desc)
	if old, err := v.st.SecretByName(ctx, clean); err == nil {
		var dp *string
		if d != "" {
			dp = &d
		}
		_, err = v.st.UpdateSecret(ctx, old.ID, enc, dp)
		if err == nil {
			err = v.refresh()
		}
		return err
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	_, err = v.st.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: clean, Description: d}, EncryptedValue: enc})
	if err == nil {
		err = v.refresh()
	}
	return err
}

func (v *panelVault) Delete(name string) error {
	ctx, cancel := vaultCtx()
	defer cancel()
	rec, err := v.st.SecretByName(ctx, name)
	if err != nil {
		return err
	}
	if err := v.st.DeleteSecret(ctx, rec.ID); err != nil {
		return err
	}
	return v.refresh()
}

// API keys are never stored by an embedded Frog Bot: models come from OneFrog.
func (v *panelVault) SealAPIKey(string) (string, error) {
	return "", errors.New("模型由 OneFrog 设置管理")
}
func (v *panelVault) OpenAPIKey(string) (string, error) {
	return "", errors.New("模型由 OneFrog 设置管理")
}

// ---------- 定时：Frog Bot 的计划就是 OneFrog 的自动化 ----------

type frogAutomations struct {
	book   *automation.Book
	owner  string
	hostID func() string // the OneFrog bot this runtime stands for
	main   bool          // the default runtime also owns legacy "frog-bot" automations
}

func (a frogAutomations) owns(it automation.Automation) bool {
	if it.OwnerID != a.owner {
		return false
	}
	return it.BotID == a.hostID() || (a.main && it.BotID == FrogBotID)
}

func describeSchedule(s automation.Schedule) string {
	week := []string{"日", "一", "二", "三", "四", "五", "六"}
	switch s.Kind {
	case automation.KindInterval:
		if s.Minutes%60 == 0 {
			return fmt.Sprintf("每 %d 小时", s.Minutes/60)
		}
		return fmt.Sprintf("每 %d 分钟", s.Minutes)
	case automation.KindDaily:
		return "每天 " + s.Time
	case automation.KindWeekly:
		if s.Weekday >= 0 && s.Weekday < 7 {
			return "每周" + week[s.Weekday] + " " + s.Time
		}
	case automation.KindOnce:
		if s.At != nil {
			return "单次 " + s.At.In(time.Local).Format("2006-01-02 15:04")
		}
	}
	return s.Kind
}

func hostAutomation(it automation.Automation) frog.HostAutomation {
	out := frog.HostAutomation{ID: it.ID, Title: it.Name, Prompt: it.Prompt, Schedule: describeSchedule(it.Schedule),
		Enabled: it.Enabled, LastStatus: it.LastStatus}
	if it.NextRunAt != nil && it.Enabled {
		out.NextRun = it.NextRunAt.In(time.Local).Format("2006-01-02 15:04 MST")
	}
	return out
}

func (a frogAutomations) mine(id string) (automation.Automation, error) {
	it, err := a.book.Get(id)
	if err != nil || !a.owns(it) {
		return automation.Automation{}, fmt.Errorf("没有找到计划 %s", id)
	}
	return it, nil
}

func (a frogAutomations) List() ([]frog.HostAutomation, error) {
	var out []frog.HostAutomation
	for _, it := range a.book.List() {
		if a.owns(it) {
			out = append(out, hostAutomation(it))
		}
	}
	return out, nil
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (a frogAutomations) Create(d frog.AutomationDraft) (frog.HostAutomation, error) {
	botID := a.hostID()
	if botID == "" {
		botID = FrogBotID
	}
	in := automation.Automation{Name: clipRunes(d.Title, automation.MaxName), Prompt: d.Prompt, BotID: botID, Enabled: true}
	clock := fmt.Sprintf("%02d:%02d", d.Hour, d.Minute)
	switch d.Kind {
	case "once":
		at, err := time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(d.AtLocal), time.Local)
		if err != nil {
			return frog.HostAutomation{}, errors.New("单次计划需要 at_local，例如 2026-11-10 09:00")
		}
		in.Schedule = automation.Schedule{Kind: automation.KindOnce, At: &at}
	case "daily":
		in.Schedule = automation.Schedule{Kind: automation.KindDaily, Time: clock}
	case "weekly":
		in.Schedule = automation.Schedule{Kind: automation.KindWeekly, Time: clock, Weekday: d.Weekday}
	case "interval":
		in.Schedule = automation.Schedule{Kind: automation.KindInterval, Minutes: d.Interval}
	default:
		return frog.HostAutomation{}, errors.New("kind 只能是 once/daily/weekly/interval")
	}
	it, err := a.book.Create(in, a.owner)
	if err != nil {
		return frog.HostAutomation{}, err
	}
	return hostAutomation(it), nil
}

func (a frogAutomations) SetEnabled(id string, enabled bool) (frog.HostAutomation, error) {
	it, err := a.mine(id)
	if err != nil {
		return frog.HostAutomation{}, err
	}
	it.Enabled = enabled
	it, err = a.book.Update(id, it)
	if err != nil {
		return frog.HostAutomation{}, err
	}
	return hostAutomation(it), nil
}

func (a frogAutomations) Delete(id string) error {
	if _, err := a.mine(id); err != nil {
		return err
	}
	return a.book.Delete(id)
}

func (a frogAutomations) RunNow(id string) error {
	if _, err := a.mine(id); err != nil {
		return err
	}
	_, err := a.book.RunNow(id)
	return err
}

// frogTaskID derives a stable task id from the automation firing, so a retry
// after a crash adopts the same task instead of starting a second one.
func frogTaskID(it automation.Automation, runID string) string {
	sum := sha256.Sum256([]byte("automation:" + it.ID + ":" + runID))
	return "task_auto" + hex.EncodeToString(sum[:8])
}

func frogObservation(st frog.TaskState) automation.Observation {
	obs := automation.Observation{SessionID: FrogBotID, Status: st.Status}
	switch st.Status {
	case "completed":
		obs.Done, obs.Text = true, st.Summary
	case "failed", "cancelled":
		obs.Done, obs.Text = true, st.Error
	case "paused":
		obs.Waiting = true
	}
	if !st.Found {
		obs.Done, obs.Status, obs.Text = true, "failed", "任务不存在"
	}
	if st.Waiting && !obs.Done {
		obs.Waiting = true
	}
	return obs
}

// frogTarget finds the runtime that runs an automation: the bot it names,
// or the leader for legacy "frog-bot" automations. ok is false when the bot
// is not an AI-assistant bot (disabled, deleted or the module builder), in
// which case the classic runner keeps it.
func (f *FrogBots) frogTarget(ctx context.Context, it automation.Automation) (*frogAccount, string, bool) {
	if f == nil || strings.TrimSpace(it.OwnerID) == "" {
		return nil, "", false
	}
	acct, err := f.account(it.OwnerID)
	if err != nil {
		return nil, "", false
	}
	if err := f.sync(ctx, acct); err != nil {
		return nil, "", false
	}
	for _, p := range acct.bots.Profiles() {
		if (it.BotID == FrogBotID && p.ID == "main") || (p.HostID != "" && p.HostID == it.BotID) {
			return acct, p.ID, true
		}
	}
	return nil, "", false
}

// frogTask finds the runtime that holds a task, wherever the bot moved.
func (f *FrogBots) frogTask(it automation.Automation, taskID string) (*frog.Bot, error) {
	acct, err := f.account(it.OwnerID)
	if err != nil {
		return nil, err
	}
	for _, p := range acct.bots.Profiles() {
		if rt := acct.bots.Runtime(p.ID); rt != nil {
			if _, ok := rt.Store().Task(taskID); ok {
				return rt, nil
			}
		}
	}
	return nil, errors.New("任务不存在")
}

func isFrogTask(taskID string) bool { return strings.HasPrefix(taskID, "task_auto") }

// wrapAutomationRunner runs automations in the AI assistant: each run is a
// background task of the bot the automation names, in its owner's account.
// Runs that started on the classic runner before the upgrade finish there.
func (s *Server) wrapAutomationRunner(d *automation.DurableRunner) *automation.DurableRunner {
	f := s.FrogBots
	classicSubmit := func(ctx context.Context, it automation.Automation, runID string) (string, string, error) {
		if d == nil {
			return "", "", errors.New("自动化执行器未配置")
		}
		return d.Submit(ctx, it, runID)
	}
	return &automation.DurableRunner{
		Submit: func(ctx context.Context, it automation.Automation, runID string) (string, string, error) {
			if strings.TrimSpace(it.TaskID) != "" && !isFrogTask(it.TaskID) {
				// A classic run was already accepted for this firing: adopt it.
				return classicSubmit(ctx, it, runID)
			}
			acct, frogID, ok := f.frogTarget(ctx, it)
			if !ok {
				return classicSubmit(ctx, it, runID)
			}
			rt := acct.bots.Runtime(frogID)
			if rt == nil {
				return "", "", errors.New("Bot 未启动")
			}
			taskID := frogTaskID(it, runID)
			prompt := fmt.Sprintf("【定时任务「%s」· OneFrog 自动化】\n%s", it.Name, it.Prompt)
			if it.ServerID != "" {
				if rec, err := f.agent.Store.GetServer(ctx, it.ServerID); err == nil {
					prompt += fmt.Sprintf("\n\n目标服务器：%s（server_id=%s）。", rec.Name, rec.ID)
				}
			}
			var servers []string
			if it.ServerID != "" {
				servers = []string{it.ServerID}
			}
			if err := acct.scope.setTask(taskID, servers, it.ApprovalMode); err != nil {
				return "", "", err
			}
			id, err := rt.SubmitTask(taskID, it.Name, prompt)
			return id, FrogBotID + ":" + frogID, err
		},
		Wait: func(ctx context.Context, it automation.Automation, taskID string) (automation.Observation, error) {
			if !isFrogTask(taskID) {
				return d.Wait(ctx, it, taskID)
			}
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			for {
				rt, err := f.frogTask(it, taskID)
				if err != nil {
					return frogObservation(frog.TaskState{}), nil
				}
				obs := frogObservation(rt.TaskState(taskID))
				obs.SessionID = FrogBotID + ":" + rt.ID()
				if obs.Done || obs.Waiting {
					return obs, nil
				}
				select {
				case <-ctx.Done():
					return obs, ctx.Err()
				case <-tick.C:
				}
			}
		},
		Observe: func(ctx context.Context, it automation.Automation, taskID string) (automation.Observation, error) {
			if !isFrogTask(taskID) {
				if d == nil || d.Observe == nil {
					return automation.Observation{}, errors.New("自动化执行器未配置")
				}
				return d.Observe(ctx, it, taskID)
			}
			rt, err := f.frogTask(it, taskID)
			if err != nil {
				return frogObservation(frog.TaskState{}), nil
			}
			obs := frogObservation(rt.TaskState(taskID))
			obs.SessionID = FrogBotID + ":" + rt.ID()
			return obs, nil
		},
	}
}
