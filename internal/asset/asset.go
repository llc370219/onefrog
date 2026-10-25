package asset

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"onefrog/internal/id"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	StatusActive    = "active"
	StatusExpiring  = "expiring"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"
)

type Record struct {
	ID          string    `json:"id"`
	ServerID    string    `json:"server_id,omitempty"`
	Name        string    `json:"name"`
	Provider    string    `json:"provider"`
	Region      string    `json:"region"`
	Plan        string    `json:"plan"`
	Cycle       string    `json:"cycle"`
	Currency    string    `json:"currency"`
	AmountCents int64     `json:"amount_cents"`
	PaidAt      string    `json:"paid_at"`
	StartsAt    string    `json:"starts_at"`
	ExpiresAt   string    `json:"expires_at"`
	AutoRenew   bool      `json:"auto_renew"`
	Status      string    `json:"status"`
	IP          string    `json:"ip"`
	Specs       string    `json:"specs"`
	OrderNo     string    `json:"order_no"`
	Account     string    `json:"account"`
	InvoiceURL  string    `json:"invoice_url"`
	Contact     string    `json:"contact"`
	Tags        []string  `json:"tags"`
	Notes       string    `json:"notes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type View struct {
	Record
	DaysTotal     int     `json:"days_total"`
	DaysLeft      int     `json:"days_left"`
	ElapsedRatio  float64 `json:"elapsed_ratio"`
	ResidualCents int64   `json:"residual_cents"`
	DailyCents    int64   `json:"daily_cents"`
	DerivedStatus string  `json:"derived_status"`
}

type Summary struct {
	Count         int              `json:"count"`
	Active        int              `json:"active"`
	Expiring      int              `json:"expiring"`
	Expired       int              `json:"expired"`
	AutoRenew     int              `json:"auto_renew"`
	MonthlyCents  map[string]int64 `json:"monthly_cents"`
	ResidualCents map[string]int64 `json:"residual_cents"`
	NextExpiry    string           `json:"next_expiry"`
	NextName      string           `json:"next_name"`
}

type Input struct {
	ServerID    string   `json:"server_id"`
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Region      string   `json:"region"`
	Plan        string   `json:"plan"`
	Cycle       string   `json:"cycle"`
	Currency    string   `json:"currency"`
	AmountCents int64    `json:"amount_cents"`
	PaidAt      string   `json:"paid_at"`
	StartsAt    string   `json:"starts_at"`
	ExpiresAt   string   `json:"expires_at"`
	AutoRenew   bool     `json:"auto_renew"`
	Status      string   `json:"status"`
	IP          string   `json:"ip"`
	Specs       string   `json:"specs"`
	OrderNo     string   `json:"order_no"`
	Account     string   `json:"account"`
	InvoiceURL  string   `json:"invoice_url"`
	Contact     string   `json:"contact"`
	Tags        []string `json:"tags"`
	Notes       string   `json:"notes"`
}

type Ledger struct {
	mu   sync.Mutex
	path string
	box  *secret.Box
	rows map[string]Record
	now  func() time.Time
}

func New(path string, box *secret.Box) (*Ledger, error) {
	l := &Ledger{path: path, box: box, rows: map[string]Record{}, now: func() time.Time { return time.Now().UTC() }}
	if path == "" || box == nil {
		return l, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := box.Decrypt(string(raw))
	if err != nil {
		return nil, err
	}
	var rows []Record
	if err = json.Unmarshal([]byte(plain), &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		l.rows[row.ID] = normalize(row)
	}
	return l, nil
}

func (l *Ledger) List() []View {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]View, 0, len(l.rows))
	for _, row := range l.rows {
		out = append(out, present(row, l.now()))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ExpiresAt == out[j].ExpiresAt {
			return out[i].Name < out[j].Name
		}
		if out[i].ExpiresAt == "" {
			return false
		}
		if out[j].ExpiresAt == "" {
			return true
		}
		return out[i].ExpiresAt < out[j].ExpiresAt
	})
	return out
}

func (l *Ledger) Summary() Summary {
	rows := l.List()
	sum := Summary{Count: len(rows), MonthlyCents: map[string]int64{}, ResidualCents: map[string]int64{}}
	for _, row := range rows {
		switch row.DerivedStatus {
		case StatusActive:
			sum.Active++
		case StatusExpiring:
			sum.Expiring++
			sum.Active++
		case StatusExpired:
			sum.Expired++
		}
		if row.AutoRenew && row.DerivedStatus != StatusCancelled {
			sum.AutoRenew++
		}
		cur := row.Currency
		if cur == "" {
			cur = "CNY"
		}
		sum.MonthlyCents[cur] += monthly(row.AmountCents, row.Cycle)
		if row.DerivedStatus != StatusCancelled && row.DerivedStatus != StatusExpired {
			sum.ResidualCents[cur] += row.ResidualCents
		}
		if row.ExpiresAt != "" && (row.DerivedStatus == StatusActive || row.DerivedStatus == StatusExpiring) && (sum.NextExpiry == "" || row.ExpiresAt < sum.NextExpiry) {
			sum.NextExpiry = row.ExpiresAt
			sum.NextName = row.Name
		}
	}
	return sum
}

func (l *Ledger) Get(idv string) (View, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, ok := l.rows[idv]
	if !ok {
		return View{}, errors.New("没有这条资产")
	}
	return present(row, l.now()), nil
}

func (l *Ledger) Create(in Input) (View, error) {
	row, err := build(Record{}, in)
	if err != nil {
		return View{}, err
	}
	row.ID = id.New()
	row.CreatedAt = l.now()
	row.UpdatedAt = row.CreatedAt
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows[row.ID] = row
	if err = l.saveLocked(); err != nil {
		delete(l.rows, row.ID)
		return View{}, err
	}
	return present(row, l.now()), nil
}

func (l *Ledger) Update(idv string, in Input) (View, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	old, ok := l.rows[idv]
	if !ok {
		return View{}, errors.New("没有这条资产")
	}
	row, err := build(old, in)
	if err != nil {
		return View{}, err
	}
	row.ID = old.ID
	row.CreatedAt = old.CreatedAt
	row.UpdatedAt = l.now()
	l.rows[idv] = row
	if err = l.saveLocked(); err != nil {
		l.rows[idv] = old
		return View{}, err
	}
	return present(row, l.now()), nil
}

func (l *Ledger) Delete(idv string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	old, ok := l.rows[idv]
	if !ok {
		return errors.New("没有这条资产")
	}
	delete(l.rows, idv)
	if err := l.saveLocked(); err != nil {
		l.rows[idv] = old
		return err
	}
	return nil
}

func (l *Ledger) saveLocked() error {
	if l.path == "" || l.box == nil {
		return nil
	}
	rows := make([]Record, 0, len(l.rows))
	for _, row := range l.rows {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	enc, err := l.box.Encrypt(string(raw))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".assets-*")
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
	return os.Rename(name, l.path)
}

func build(old Record, in Input) (Record, error) {
	name := clip(in.Name, 80)
	if name == "" {
		return Record{}, errors.New("名称必填")
	}
	currency := strings.ToUpper(clip(in.Currency, 8))
	if currency == "" {
		currency = "CNY"
	}
	if len(currency) < 3 || len(currency) > 8 {
		return Record{}, errors.New("币种需要 3 到 8 位字母")
	}
	for _, r := range currency {
		if r < 'A' || r > 'Z' {
			return Record{}, errors.New("币种只能是字母")
		}
	}
	if in.AmountCents < 0 || in.AmountCents > 10_000_000_000 {
		return Record{}, errors.New("金额超出可记录范围")
	}
	cycle := clip(in.Cycle, 20)
	if cycle == "" {
		cycle = "monthly"
	}
	if cycle != "monthly" && cycle != "quarterly" && cycle != "yearly" && cycle != "once" {
		return Record{}, errors.New("计费周期只能是 monthly、quarterly、yearly、once")
	}
	status := clip(in.Status, 20)
	if status == "" {
		status = StatusActive
	}
	if status != StatusActive && status != StatusCancelled {
		return Record{}, errors.New("状态只能是 active 或 cancelled")
	}
	paid, err := day(in.PaidAt, false)
	if err != nil {
		return Record{}, fmt.Errorf("付款日期%s", err.Error())
	}
	start, err := day(in.StartsAt, false)
	if err != nil {
		return Record{}, fmt.Errorf("开始日期%s", err.Error())
	}
	end, err := day(in.ExpiresAt, cycle != "once")
	if err != nil {
		return Record{}, fmt.Errorf("到期日期%s", err.Error())
	}
	if start != "" && end != "" && end < start {
		return Record{}, errors.New("到期日期不能早于开始日期")
	}
	tags := make([]string, 0, len(in.Tags))
	seen := map[string]bool{}
	for _, tag := range in.Tags {
		tag = clip(tag, 24)
		key := strings.ToLower(tag)
		if tag == "" || seen[key] {
			continue
		}
		seen[key] = true
		tags = append(tags, tag)
		if len(tags) == 8 {
			break
		}
	}
	row := old
	row.ServerID = clip(in.ServerID, 80)
	row.Name = name
	row.Provider = clip(in.Provider, 80)
	row.Region = clip(in.Region, 80)
	row.Plan = clip(in.Plan, 80)
	row.Cycle = cycle
	row.Currency = currency
	row.AmountCents = in.AmountCents
	row.PaidAt = paid
	row.StartsAt = start
	row.ExpiresAt = end
	row.AutoRenew = in.AutoRenew
	row.Status = status
	row.IP = clip(in.IP, 80)
	row.Specs = clip(in.Specs, 160)
	row.OrderNo = clip(in.OrderNo, 80)
	row.Account = clip(in.Account, 120)
	row.InvoiceURL = clip(in.InvoiceURL, 300)
	row.Contact = clip(in.Contact, 120)
	row.Tags = tags
	row.Notes = clip(in.Notes, 500)
	if row.StartsAt == "" {
		row.StartsAt = row.PaidAt
	}
	return row, nil
}

func present(row Record, now time.Time) View {
	row = normalize(row)
	view := View{Record: row}
	start, startOK := parseDay(row.StartsAt)
	end, endOK := parseDay(row.ExpiresAt)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if startOK && endOK && !end.Before(start) {
		view.DaysTotal = int(end.Sub(start).Hours()/24) + 1
		if today.After(end) {
			view.DaysLeft = 0
		} else if today.Before(start) {
			view.DaysLeft = view.DaysTotal
		} else {
			view.DaysLeft = int(end.Sub(today).Hours()/24) + 1
		}
		used := view.DaysTotal - view.DaysLeft
		if view.DaysTotal > 0 {
			view.ElapsedRatio = math.Round(float64(used)/float64(view.DaysTotal)*1000) / 1000
			view.DailyCents = int64(math.Round(float64(row.AmountCents) / float64(view.DaysTotal)))
			view.ResidualCents = int64(math.Round(float64(row.AmountCents) * float64(view.DaysLeft) / float64(view.DaysTotal)))
		}
	} else if endOK {
		if today.After(end) {
			view.DaysLeft = 0
		} else {
			view.DaysLeft = int(end.Sub(today).Hours()/24) + 1
		}
	}
	switch {
	case row.Status == StatusCancelled:
		view.DerivedStatus = StatusCancelled
		view.ResidualCents = 0
	case endOK && today.After(end):
		view.DerivedStatus = StatusExpired
		view.ResidualCents = 0
	case endOK && view.DaysLeft > 0 && view.DaysLeft <= 14:
		view.DerivedStatus = StatusExpiring
	default:
		view.DerivedStatus = StatusActive
	}
	return view
}

func monthly(amount int64, cycle string) int64 {
	switch cycle {
	case "yearly":
		return int64(math.Round(float64(amount) / 12))
	case "quarterly":
		return int64(math.Round(float64(amount) / 3))
	case "once":
		return 0
	default:
		return amount
	}
}

func day(v string, required bool) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		if required {
			return "", errors.New("必填，格式 YYYY-MM-DD")
		}
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return "", errors.New("格式应为 YYYY-MM-DD")
	}
	return v, nil
}

func parseDay(v string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", v)
	return t, err == nil
}

func clip(v string, n int) string {
	v = strings.TrimSpace(v)
	r := []rune(v)
	if len(r) > n {
		return string(r[:n])
	}
	return v
}

func normalize(row Record) Record {
	if row.Tags == nil {
		row.Tags = []string{}
	}
	if row.Currency == "" {
		row.Currency = "CNY"
	}
	if row.Cycle == "" {
		row.Cycle = "monthly"
	}
	if row.Status == "" {
		row.Status = StatusActive
	}
	return row
}
