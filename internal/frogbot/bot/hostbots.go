package bot

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// OpenHostBots 打开嵌入宿主的多 Bot：Bot 列表由宿主决定（宿主的「Bot」页），这里不能新建、改名或删除。
// profiles[0] 必须是默认 Bot（ID main，宿主的主控），它沿用原来的数据目录，旧的对话和任务继续可用。
// 其他 Bot 的数据在 DataDir/bots/<id>/，工作区在 Workspace-bots/<id>/。
func OpenHostBots(cfg Config, profiles []BotProfile) (*Bots, error) {
	if cfg.Host == nil {
		return nil, errors.New("只用于嵌入模式")
	}
	if err := validateHostProfiles(profiles); err != nil {
		return nil, err
	}
	workspace, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	first := profiles[0]
	main, err := newRuntime(cfg, nil, &first)
	if err != nil {
		return nil, err
	}
	bs := &Bots{base: cfg, main: main, workRoot: workspace + "-bots", profiles: []BotProfile{first},
		runtimes: map[string]*Bot{defaultBotID: main}, handlers: map[string]*botEndpoint{defaultBotID: {Handler: main.Handler("")}}}
	main.coordinator = bs
	for _, p := range profiles[1:] {
		b, err := bs.open(p)
		if err != nil {
			bs.Close()
			return nil, fmt.Errorf("打开 Bot %s: %w", p.Name, err)
		}
		b.coordinator = bs
		bs.runtimes[p.ID], bs.handlers[p.ID] = b, &botEndpoint{Handler: b.Handler("")}
		bs.profiles = append(bs.profiles, p)
	}
	for _, b := range bs.runtimes {
		b.Start()
	}
	return bs, nil
}

func validateHostProfiles(profiles []BotProfile) error {
	if len(profiles) == 0 || len(profiles) > maxBots || profiles[0].ID != defaultBotID {
		return errors.New("Bot 列表缺少默认 Bot 或数量超限")
	}
	ids := map[string]bool{}
	for _, p := range profiles {
		if ids[p.ID] || (p.ID != defaultBotID && !botIDPattern.MatchString(p.ID)) || strings.TrimSpace(p.Name) == "" {
			return errors.New("Bot 列表含无效或重复的 ID")
		}
		ids[p.ID] = true
	}
	return nil
}

// Hosted 表示 Bot 列表由宿主管理。
func (bs *Bots) Hosted() bool { return bs.base.Host != nil }

// Sync 按宿主的 Bot 列表增减运行时：新的 Bot 打开并启动；不在列表里的停下（数据保留，重新启用后接着用）；
// 名字和职责立刻生效。
func (bs *Bots) Sync(profiles []BotProfile) error {
	if !bs.Hosted() {
		return errors.New("只用于嵌入模式")
	}
	if err := validateHostProfiles(profiles); err != nil {
		return err
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.closed {
		return errors.New("服务正在关闭")
	}
	keep := map[string]bool{}
	var opened []*Bot
	for _, p := range profiles {
		keep[p.ID] = true
		p := p
		if b := bs.runtimes[p.ID]; b != nil {
			b.profile.Store(&p)
			continue
		}
		b, err := bs.open(p)
		if err != nil {
			for _, o := range opened {
				delete(bs.runtimes, o.ID())
				delete(bs.handlers, o.ID())
				o.Close()
			}
			return fmt.Errorf("打开 Bot %s: %w", p.Name, err)
		}
		b.coordinator = bs
		bs.runtimes[p.ID], bs.handlers[p.ID] = b, &botEndpoint{Handler: b.Handler("")}
		opened = append(opened, b)
	}
	for id, b := range bs.runtimes {
		if keep[id] || id == defaultBotID {
			continue
		}
		endpoint := bs.handlers[id]
		delete(bs.runtimes, id)
		delete(bs.handlers, id)
		bs.retired.Add(1)
		go func() {
			defer bs.retired.Done()
			b.stop()
			endpoint.requests.Wait()
			b.Close()
		}()
	}
	bs.profiles = slices.Clone(profiles)
	for _, b := range opened {
		b.Start()
	}
	return nil
}

// Profiles 返回当前的 Bot 列表（默认 Bot 在最前）。
func (bs *Bots) Profiles() []BotProfile {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	return slices.Clone(bs.profiles)
}

// Runtime 返回某个 Bot 的运行时；不存在时返回 nil。
func (bs *Bots) Runtime(id string) *Bot {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	return bs.runtimes[id]
}

// Main 返回默认 Bot。
func (bs *Bots) Main() *Bot { return bs.main }

// others 列出除自己以外的 Bot，给聊天层做协作参考。
func (bs *Bots) others(self string) []BotProfile {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	var out []BotProfile
	for _, p := range bs.profiles {
		if p.ID != self {
			out = append(out, p)
		}
	}
	return out
}
