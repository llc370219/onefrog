package model

import (
	"encoding/json"
	"time"
)

type User struct {
	ID                    string    `json:"id"`
	Username              string    `json:"username"`
	DisplayName           string    `json:"display_name"`
	Avatar                string    `json:"avatar"`
	MustChangeCredentials bool      `json:"must_change_credentials"`
	CreatedAt             time.Time `json:"created_at"`
}

type Server struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Username    string    `json:"username"`
	AuthType    string    `json:"auth_type"`
	Status      string    `json:"status"`
	Role        string    `json:"role"`
	DiskPercent *int      `json:"disk_percent"`
	Load        string    `json:"load"`
	CreatedAt   time.Time `json:"created_at"`
}

type Bot struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	ModelProvider  string    `json:"model_provider"`
	ModelName      string    `json:"model_name"`
	SystemPrompt   string    `json:"system_prompt"`
	BaseURL        string    `json:"base_url"`
	APIKeySecretID string    `json:"api_key_secret_id"`
	CreatedAt      time.Time `json:"created_at"`
}

type Secret struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type Module struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

type History struct {
	ID          string    `json:"id"`
	BotID       string    `json:"bot_id"`
	ServerID    string    `json:"server_id"`
	BotName     string    `json:"bot_name"`
	ServerName  string    `json:"server_name"`
	Action      string    `json:"action"`
	Command     string    `json:"command"`
	Status      string    `json:"status"`
	CanRollback bool      `json:"can_rollback"`
	CreatedAt   time.Time `json:"created_at"`
}

type Session struct {
	Options   json.RawMessage `json:"options,omitempty"`
	ID        string          `json:"id"`
	BotID     string          `json:"bot_id"`
	Title     string          `json:"title"`
	CreatedAt time.Time       `json:"created_at"`
}

type SessionSummary struct {
	Session
	Preview   string    `json:"preview"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int    `json:"size"`
}

// ChoiceOption is one button on a choice card.
type ChoiceOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ChoiceAnswer is the operator's single, final answer to a choice card.
type ChoiceAnswer struct {
	Values     []string  `json:"values,omitempty"`
	Custom     string    `json:"custom,omitempty"`
	AnsweredAt time.Time `json:"answered_at"`
}

// Choice is a question the bot asked with ask_user, shown as a card of options.
type Choice struct {
	Prompt      string         `json:"prompt"`
	Options     []ChoiceOption `json:"options"`
	MultiSelect bool           `json:"multi_select,omitempty"`
	AllowCustom bool           `json:"allow_custom,omitempty"`
	Answer      *ChoiceAnswer  `json:"answer,omitempty"`
}

type Message struct {
	Choice      *Choice      `json:"choice,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ID          string       `json:"id"`
	SessionID   string       `json:"session_id"`
	Role        string       `json:"role"`
	Content     string       `json:"content"`
	CreatedAt   time.Time    `json:"created_at"`
}
