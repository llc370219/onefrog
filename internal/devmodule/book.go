package devmodule

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"onefrog/internal/id"
	"onefrog/internal/model"
	"onefrog/internal/module"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

const (
	BotID          = "develop"
	BotName        = "自定义模块"
	LegacyBotName  = "二次开发"
	ModuleID       = "develop"
	ModuleName     = "自定义模块"
	CustomPrefix   = "custom-"
	maxModules     = 40
	maxRecords     = 500
	maxNameRunes   = 40
	maxDescRunes   = 200
	maxHTMLBytes   = 64 * 1024
	maxStateBytes  = 128 * 1024
	maxRecordBytes = 8 * 1024
)

// Record is one structured data row belonging to a custom module.
type Record struct {
	ID        string         `json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Data      map[string]any `json:"data"`
}

// CustomModule is a user/AI-built interactive module stored encrypted on disk.
type CustomModule struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	HTML        string         `json:"html"`
	State       map[string]any `json:"state,omitempty"`
	Records     []Record       `json:"records,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// View summarizes a custom module together with its HTML source.
type View struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	HTML         string    `json:"html"`
	RecordCount  int       `json:"record_count"`
	StateKeys    []string  `json:"state_keys,omitempty"`
	LastRecordAt string    `json:"last_record_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DataPayload is the runtime state + records exposed to a custom module's sandbox.
type DataPayload struct {
	State   map[string]any `json:"state"`
	Records []Record       `json:"records"`
	Record  *Record        `json:"record,omitempty"`
}

// Input creates a new custom module.
type Input struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	HTML        string `json:"html"`
}

// Patch updates an existing custom module without erasing its saved State or Records.
type Patch struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	HTML        *string `json:"html,omitempty"`
}

type fileData struct {
	Modules []CustomModule `json:"modules"`
}

// Book persists custom modules encrypted with AES-GCM next to servers.enc and assets.enc.
type Book struct {
	mu      sync.Mutex
	path    string
	box     *secret.Box
	modules map[string]CustomModule
	now     func() time.Time
}

func New(path string, box *secret.Box) (*Book, error) {
	b := &Book{
		path:    path,
		box:     box,
		modules: map[string]CustomModule{},
		now:     func() time.Time { return time.Now().UTC() },
	}
	if path == "" || box == nil {
		return b, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := box.Decrypt(string(raw))
	if err != nil {
		return nil, err
	}
	var data fileData
	if err = json.Unmarshal([]byte(plain), &data); err != nil {
		return nil, err
	}
	for _, item := range data.Modules {
		if item.ID == "" {
			continue
		}
		if item.State == nil {
			item.State = map[string]any{}
		}
		if item.Records == nil {
			item.Records = []Record{}
		}
		b.modules[item.ID] = item
	}
	return b, nil
}

// Bot returns the stock 自定义模块 Bot definition.
func Bot() model.Bot {
	return model.Bot{
		ID:          BotID,
		Name:        BotName,
		Description: "按需求编写和改造自定义交互模块，直接挂载到面板与模块目录",
	}
}

// Ensure creates the stock 自定义模块 Bot if missing, and migrates legacy 自定义模块 name.
func Ensure(list func() ([]model.Bot, error), get func(string) (model.Bot, error), create func(model.Bot) (model.Bot, error), update func(model.Bot) (model.Bot, error)) error {
	if item, err := get(BotID); err == nil {
		if item.Name == LegacyBotName && update != nil {
			item.Name = BotName
			if strings.Contains(item.SystemPrompt, LegacyBotName) {
				item.SystemPrompt = strings.ReplaceAll(item.SystemPrompt, LegacyBotName, BotName)
			}
			_, err = update(item)
			return err
		}
		return nil
	}
	bots, err := list()
	if err != nil {
		return err
	}
	for _, item := range bots {
		if item.Name == LegacyBotName && update != nil {
			item.Name = BotName
			if strings.Contains(item.SystemPrompt, LegacyBotName) {
				item.SystemPrompt = strings.ReplaceAll(item.SystemPrompt, LegacyBotName, BotName)
			}
			_, err = update(item)
			return err
		}
		if item.ID == BotID || item.Name == BotName {
			return nil
		}
	}
	_, err = create(Bot())
	return err
}

// SyncStore ensures all custom modules in Book are registered as Type="custom"
// in Store, and removes stale custom module entries no longer in Book.
func SyncStore(ctx context.Context, db store.Store, book *Book) error {
	if db == nil || book == nil {
		return nil
	}
	items := book.List()
	existing, err := db.ListModules(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]model.Module, len(existing))
	for _, m := range existing {
		byID[m.ID] = m
	}
	keep := make(map[string]bool, len(items))
	for _, item := range items {
		keep[item.ID] = true
		enabled := true
		createdAt := item.CreatedAt
		if old, ok := byID[item.ID]; ok {
			enabled = old.Enabled
			if !old.CreatedAt.IsZero() {
				createdAt = old.CreatedAt
			}
		}
		if _, err := db.SaveModule(ctx, model.Module{
			ID:          item.ID,
			Name:        item.Name,
			Version:     "1.0.0",
			Type:        "custom",
			Description: item.Description,
			Enabled:     enabled,
			CreatedAt:   createdAt,
		}); err != nil {
			return err
		}
	}
	for _, m := range existing {
		if m.Type == "custom" && !keep[m.ID] {
			_ = db.DeleteModule(ctx, m.ID)
		}
	}
	return nil
}

func toView(item CustomModule) View {
	keys := make([]string, 0, len(item.State))
	for k := range item.State {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lastAt := ""
	if n := len(item.Records); n > 0 {
		lastAt = item.Records[n-1].CreatedAt.Format(time.RFC3339)
	}
	return View{
		ID:           item.ID,
		Name:         item.Name,
		Description:  item.Description,
		HTML:         item.HTML,
		RecordCount:  len(item.Records),
		StateKeys:    keys,
		LastRecordAt: lastAt,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
	}
}

func (b *Book) List() []View {
	if b == nil {
		return []View{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]View, 0, len(b.modules))
	for _, item := range b.modules {
		out = append(out, toView(item))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (b *Book) Get(idOrName string) (CustomModule, error) {
	if b == nil {
		return CustomModule{}, errors.New("自定义模块未就绪")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item, err := b.findLocked(idOrName)
	if err != nil {
		return CustomModule{}, err
	}
	return cloneModule(item), nil
}

func (b *Book) Create(in Input) (CustomModule, error) {
	if b == nil {
		return CustomModule{}, errors.New("自定义模块未就绪")
	}
	name, err := cleanText(in.Name, maxNameRunes, true, "模块名称")
	if err != nil {
		return CustomModule{}, err
	}
	if isBuiltinName(name) {
		return CustomModule{}, errors.New("不能与内置模块同名")
	}
	desc, err := cleanText(in.Description, maxDescRunes, false, "模块说明")
	if err != nil {
		return CustomModule{}, err
	}
	html, err := cleanHTML(in.HTML)
	if err != nil {
		return CustomModule{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.modules) >= maxModules {
		return CustomModule{}, errors.New("自定义模块已达 40 个上限，请先删除不再使用的模块")
	}
	for _, existing := range b.modules {
		if existing.Name == name {
			return CustomModule{}, errors.New("已经有同名的自定义模块，可直接更新它")
		}
	}
	now := b.now()
	rawID := id.New()
	if len(rawID) > 12 {
		rawID = rawID[:12]
	}
	modID := CustomPrefix + rawID
	for {
		if _, taken := b.modules[modID]; !taken {
			break
		}
		r := id.New()
		if len(r) > 12 {
			r = r[:12]
		}
		modID = CustomPrefix + r
	}
	item := CustomModule{
		ID:          modID,
		Name:        name,
		Description: desc,
		HTML:        html,
		State:       map[string]any{},
		Records:     []Record{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	b.modules[item.ID] = item
	if err := b.saveLocked(); err != nil {
		delete(b.modules, item.ID)
		return CustomModule{}, err
	}
	return cloneModule(item), nil
}

func (b *Book) Update(idOrName string, patch Patch) (CustomModule, error) {
	if b == nil {
		return CustomModule{}, errors.New("自定义模块未就绪")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item, err := b.findLocked(idOrName)
	if err != nil {
		return CustomModule{}, err
	}
	next := cloneModule(item)
	if patch.Name != nil {
		name, e := cleanText(*patch.Name, maxNameRunes, true, "模块名称")
		if e != nil {
			return CustomModule{}, e
		}
		if isBuiltinName(name) {
			return CustomModule{}, errors.New("不能与内置模块同名")
		}
		for _, other := range b.modules {
			if other.ID != item.ID && other.Name == name {
				return CustomModule{}, errors.New("已经有同名的自定义模块")
			}
		}
		next.Name = name
	}
	if patch.Description != nil {
		desc, e := cleanText(*patch.Description, maxDescRunes, false, "模块说明")
		if e != nil {
			return CustomModule{}, e
		}
		next.Description = desc
	}
	if patch.HTML != nil {
		html, e := cleanHTML(*patch.HTML)
		if e != nil {
			return CustomModule{}, e
		}
		next.HTML = html
	}
	next.UpdatedAt = b.now()
	b.modules[item.ID] = next
	if err := b.saveLocked(); err != nil {
		b.modules[item.ID] = item
		return CustomModule{}, err
	}
	return cloneModule(next), nil
}

func (b *Book) Delete(idOrName string) (CustomModule, error) {
	if b == nil {
		return CustomModule{}, errors.New("自定义模块未就绪")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item, err := b.findLocked(idOrName)
	if err != nil {
		return CustomModule{}, err
	}
	delete(b.modules, item.ID)
	if err := b.saveLocked(); err != nil {
		b.modules[item.ID] = item
		return CustomModule{}, err
	}
	return cloneModule(item), nil
}

func (b *Book) GetData(idOrName string) (DataPayload, error) {
	item, err := b.Get(idOrName)
	if err != nil {
		return DataPayload{}, err
	}
	return DataPayload{State: item.State, Records: recentFirst(item.Records)}, nil
}

func (b *Book) SaveState(idOrName string, patch map[string]any, merge bool) (DataPayload, error) {
	if b == nil {
		return DataPayload{}, errors.New("自定义模块未就绪")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item, err := b.findLocked(idOrName)
	if err != nil {
		return DataPayload{}, err
	}
	next := cloneModule(item)
	if !merge || next.State == nil {
		next.State = map[string]any{}
	}
	for k, v := range patch {
		if v == nil && merge {
			delete(next.State, k)
		} else {
			next.State[k] = v
		}
	}
	raw, err := json.Marshal(next.State)
	if err != nil || len(raw) > maxStateBytes {
		return DataPayload{}, errors.New("状态数据超出 128 KB 限制")
	}
	next.UpdatedAt = b.now()
	b.modules[item.ID] = next
	if err := b.saveLocked(); err != nil {
		b.modules[item.ID] = item
		return DataPayload{}, err
	}
	cp := cloneModule(next)
	return DataPayload{State: cp.State, Records: recentFirst(cp.Records)}, nil
}

func (b *Book) AddRecord(idOrName string, data map[string]any) (DataPayload, error) {
	if b == nil {
		return DataPayload{}, errors.New("自定义模块未就绪")
	}
	if data == nil {
		data = map[string]any{}
	}
	cleanData := sanitizeDataMap(data)
	raw, err := json.Marshal(cleanData)
	if err != nil || len(raw) > maxRecordBytes {
		return DataPayload{}, errors.New("单条记录超出 8 KB 限制")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item, err := b.findLocked(idOrName)
	if err != nil {
		return DataPayload{}, err
	}
	next := cloneModule(item)
	now := b.now()
	rid := id.New()
	if len(rid) > 12 {
		rid = rid[:12]
	}
	rec := Record{
		ID:        rid,
		CreatedAt: now,
		UpdatedAt: now,
		Data:      cleanData,
	}
	next.Records = append(next.Records, rec)
	if len(next.Records) > maxRecords {
		next.Records = next.Records[len(next.Records)-maxRecords:]
	}
	next.UpdatedAt = now
	b.modules[item.ID] = next
	if err := b.saveLocked(); err != nil {
		b.modules[item.ID] = item
		return DataPayload{}, err
	}
	cp := cloneModule(next)
	recCopy := rec
	return DataPayload{State: cp.State, Records: recentFirst(cp.Records), Record: &recCopy}, nil
}

func (b *Book) UpdateRecord(idOrName, recordID string, patch map[string]any) (DataPayload, error) {
	if b == nil {
		return DataPayload{}, errors.New("自定义模块未就绪")
	}
	recordID = strings.TrimSpace(recordID)
	if recordID == "" {
		return DataPayload{}, errors.New("缺少记录 id")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item, err := b.findLocked(idOrName)
	if err != nil {
		return DataPayload{}, err
	}
	next := cloneModule(item)
	idx := -1
	for i := range next.Records {
		if next.Records[i].ID == recordID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return DataPayload{}, errors.New("没有找到这条记录")
	}
	merged := map[string]any{}
	for k, v := range next.Records[idx].Data {
		merged[k] = v
	}
	for k, v := range sanitizeDataMap(patch) {
		if v == nil {
			delete(merged, k)
		} else {
			merged[k] = v
		}
	}
	raw, err := json.Marshal(merged)
	if err != nil || len(raw) > maxRecordBytes {
		return DataPayload{}, errors.New("单条记录超出 8 KB 限制")
	}
	now := b.now()
	next.Records[idx].Data = merged
	next.Records[idx].UpdatedAt = now
	next.UpdatedAt = now
	b.modules[item.ID] = next
	if err := b.saveLocked(); err != nil {
		b.modules[item.ID] = item
		return DataPayload{}, err
	}
	cp := cloneModule(next)
	updated := cp.Records[idx]
	return DataPayload{State: cp.State, Records: recentFirst(cp.Records), Record: &updated}, nil
}

func (b *Book) DeleteRecord(idOrName, recordID string) (DataPayload, error) {
	if b == nil {
		return DataPayload{}, errors.New("自定义模块未就绪")
	}
	recordID = strings.TrimSpace(recordID)
	if recordID == "" {
		return DataPayload{}, errors.New("缺少记录 id")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item, err := b.findLocked(idOrName)
	if err != nil {
		return DataPayload{}, err
	}
	next := cloneModule(item)
	kept := make([]Record, 0, len(next.Records))
	found := false
	for _, rec := range next.Records {
		if rec.ID == recordID {
			found = true
			continue
		}
		kept = append(kept, rec)
	}
	if !found {
		return DataPayload{}, errors.New("没有找到这条记录")
	}
	next.Records = kept
	next.UpdatedAt = b.now()
	b.modules[item.ID] = next
	if err := b.saveLocked(); err != nil {
		b.modules[item.ID] = item
		return DataPayload{}, err
	}
	cp := cloneModule(next)
	return DataPayload{State: cp.State, Records: recentFirst(cp.Records)}, nil
}

func (b *Book) ClearRecords(idOrName string) (DataPayload, error) {
	if b == nil {
		return DataPayload{}, errors.New("自定义模块未就绪")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item, err := b.findLocked(idOrName)
	if err != nil {
		return DataPayload{}, err
	}
	next := cloneModule(item)
	next.Records = []Record{}
	next.State = map[string]any{}
	next.UpdatedAt = b.now()
	b.modules[item.ID] = next
	if err := b.saveLocked(); err != nil {
		b.modules[item.ID] = item
		return DataPayload{}, err
	}
	cp := cloneModule(next)
	return DataPayload{State: cp.State, Records: []Record{}}, nil
}

func (b *Book) findLocked(idOrName string) (CustomModule, error) {
	q := strings.TrimSpace(idOrName)
	if q == "" {
		return CustomModule{}, errors.New("要指定模块")
	}
	if item, ok := b.modules[q]; ok {
		return item, nil
	}
	for _, item := range b.modules {
		if item.Name == q {
			return item, nil
		}
	}
	return CustomModule{}, errors.New("没有找到这个自定义模块")
}

func (b *Book) saveLocked() error {
	if b.path == "" || b.box == nil {
		return nil
	}
	data := fileData{Modules: make([]CustomModule, 0, len(b.modules))}
	for _, item := range b.modules {
		data.Modules = append(data.Modules, item)
	}
	sort.Slice(data.Modules, func(i, j int) bool {
		if data.Modules[i].CreatedAt.Equal(data.Modules[j].CreatedAt) {
			return data.Modules[i].ID < data.Modules[j].ID
		}
		return data.Modules[i].CreatedAt.Before(data.Modules[j].CreatedAt)
	})
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	enc, err := b.box.Encrypt(string(raw))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(b.path), ".devmodules-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.WriteString(enc); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, b.path)
}

func recentFirst(in []Record) []Record {
	out := make([]Record, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}

func cloneModule(item CustomModule) CustomModule {
	raw, _ := json.Marshal(item)
	var cp CustomModule
	_ = json.Unmarshal(raw, &cp)
	if cp.State == nil {
		cp.State = map[string]any{}
	}
	if cp.Records == nil {
		cp.Records = []Record{}
	}
	return cp
}

func sanitizeDataMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch k {
		case "id", "created_at", "updated_at", "data":
			continue
		}
		out[k] = v
	}
	return out
}

func isBuiltinName(name string) bool {
	_, ok := module.Find(name)
	return ok
}

func cleanText(v string, limit int, required bool, label string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		if required {
			return "", errors.New(label + "不能为空")
		}
		return "", nil
	}
	if strings.ContainsAny(v, "\r\n\t") {
		return "", errors.New(label + "不能包含换行")
	}
	if utf8.RuneCountInString(v) > limit {
		return "", errors.New(label + "字数超出限制")
	}
	return v, nil
}

func cleanHTML(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("模块界面 HTML 不能为空")
	}
	if len(v) > maxHTMLBytes {
		return "", errors.New("模块代码超出 64 KB 限制")
	}
	return v, nil
}
