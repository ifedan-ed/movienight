// Package settings reads and writes the JSON values kept per person
// (user_settings) and for the install (app_settings).
//
// One difference on purpose: a stored value that is not valid JSON is an
// error here (logged by the caller), not silently the default; the Python
// returned the default, which hid a corrupted row forever.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/store"
)

// Service is the settings store.
type Service struct{ q *store.Queries }

// New makes the service.
func New(q *store.Queries) *Service { return &Service{q: q} }

// User reads one person's setting into out; found false when unset.
func (s *Service) User(ctx context.Context, userID int32, key string, out any) (found bool, err error) {
	raw, err := s.q.GetUserSetting(ctx, store.GetUserSettingParams{UserID: userID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return false, fmt.Errorf("user setting %q is not valid JSON: %w", key, err)
	}
	return true, nil
}

// SetUser writes one person's setting.
func (s *Service) SetUser(ctx context.Context, userID int32, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.q.SetUserSetting(ctx, store.SetUserSettingParams{UserID: userID, Key: key, Value: string(raw)})
}

// UserBool is a true/false setting with its default; unset and JSON null
// both mean the default (the Python stored None for "not chosen").
func (s *Service) UserBool(ctx context.Context, userID int32, key string, def bool) (bool, error) {
	var v *bool
	found, err := s.User(ctx, userID, key, &v)
	if err != nil || !found || v == nil {
		return def, err
	}
	return *v, nil
}

// UserString is a text setting; "" when unset or null.
func (s *Service) UserString(ctx context.Context, userID int32, key string) (string, error) {
	var v *string
	found, err := s.User(ctx, userID, key, &v)
	if err != nil || !found || v == nil {
		return "", err
	}
	return *v, nil
}

// DeleteUser removes one person's setting.
func (s *Service) DeleteUser(ctx context.Context, userID int32, key string) error {
	return s.q.DeleteUserSetting(ctx, store.DeleteUserSettingParams{UserID: userID, Key: key})
}

// App reads an install-wide setting into out; found false when unset.
func (s *Service) App(ctx context.Context, key string, out any) (found bool, err error) {
	raw, err := s.q.GetAppSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return false, fmt.Errorf("app setting %q is not valid JSON: %w", key, err)
	}
	return true, nil
}

// SetApp writes an install-wide setting.
func (s *Service) SetApp(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.q.SetAppSetting(ctx, store.SetAppSettingParams{Key: key, Value: string(raw)})
}

// MergeApp merges fields into the JSON object kept under key (or stores
// them as a new one) in one statement, so two writers at once cannot drop
// each other's fields, which a read-then-write (the Python's) could. A nil
// value stores JSON null.
func (s *Service) MergeApp(ctx context.Context, key string, fields map[string]any) error {
	patch, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = s.q.MergeAppSetting(ctx, store.MergeAppSettingParams{Key: key, Patch: string(patch)})
	return err
}

// DeleteApp removes an install-wide setting.
func (s *Service) DeleteApp(ctx context.Context, key string) error {
	return s.q.DeleteAppSetting(ctx, key)
}

// Queries is the generated store, for callers that need a prefix listing.
func (s *Service) Queries() *store.Queries { return s.q }
