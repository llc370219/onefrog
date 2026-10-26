package store

import (
	"context"
	"encoding/json"
	"errors"
	"onefrog/internal/model"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type ServerRec struct {
	model.Server
	EncryptedKey string
	// HostKey is the confirmed SHA256 fingerprint of the server's SSH host key.
	HostKey string
	// ClearHostKey makes UpdateServer drop the stored HostKey (an empty HostKey
	// otherwise means "keep the old one"). Never persisted.
	ClearHostKey bool `json:"-"`
}

type SecretRec struct {
	model.Secret
	EncryptedValue string
}

type HistoryRec struct {
	model.History
	Snapshot string
}

type Store interface {
	CreateUser(ctx context.Context, username, passwordHash string) (model.User, error)
	UserByName(ctx context.Context, username string) (model.User, string, error)
	UpdateUser(ctx context.Context, user model.User, passwordHash, keepSession string) (model.User, error)
	CountUsers(ctx context.Context) (int, error)
	CreateSession(ctx context.Context, userID, tokenHash string, expires time.Time) error
	UserBySession(ctx context.Context, tokenHash string) (model.User, error)
	DeleteSession(ctx context.Context, tokenHash string) error

	ListServers(ctx context.Context) ([]model.Server, error)
	CountServers(ctx context.Context) (int, error)
	GetServer(ctx context.Context, id string) (ServerRec, error)
	CreateServer(ctx context.Context, rec ServerRec) (model.Server, error)
	UpdateServer(ctx context.Context, rec ServerRec) (model.Server, error)
	DeleteServer(ctx context.Context, id string) error

	ListBots(ctx context.Context) ([]model.Bot, error)
	GetBot(ctx context.Context, id string) (model.Bot, error)
	CreateBot(ctx context.Context, bot model.Bot) (model.Bot, error)
	UpdateBot(ctx context.Context, bot model.Bot) (model.Bot, error)
	DeleteBot(ctx context.Context, id string) error

	ListSecrets(ctx context.Context) ([]model.Secret, error)
	GetSecret(ctx context.Context, id string) (SecretRec, error)
	SecretByName(ctx context.Context, name string) (SecretRec, error)
	CreateSecret(ctx context.Context, rec SecretRec) (model.Secret, error)
	DeleteSecret(ctx context.Context, id string) error
	AllSecrets(ctx context.Context) ([]SecretRec, error)

	ListModules(ctx context.Context) ([]model.Module, error)
	GetModule(ctx context.Context, id string) (model.Module, error)
	SaveModule(ctx context.Context, item model.Module) (model.Module, error)
	SetModuleEnabled(ctx context.Context, id string, enabled bool) (model.Module, error)

	ListHistory(ctx context.Context, limit int) ([]model.History, error)
	GetHistory(ctx context.Context, id string) (HistoryRec, error)
	AddHistory(ctx context.Context, rec HistoryRec) (model.History, error)
	FinishRollback(ctx context.Context, id string) error

	EnsureSession(ctx context.Context, id, botID, title string) (model.Session, error)
	AddMessage(ctx context.Context, msg model.Message) (model.Message, error)
	SaveChatOptions(ctx context.Context, id string, options json.RawMessage) error
	ListChatSessions(ctx context.Context) ([]model.SessionSummary, error)
	ListMessages(ctx context.Context, sessionID string) ([]model.Message, error)
	// AnswerChoice records the one answer to a choice card. It returns
	// ErrNotFound when the message has no card, and the stored message with
	// ErrConflict when the card was already answered.
	AnswerChoice(ctx context.Context, sessionID, messageID string, answer model.ChoiceAnswer) (model.Message, error)
}
