package db

import (
	"context"
	"errors"

	"github.com/liuran001/MusicBot-Go/bot"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// One small row per confirmed private chat, kept in data.db. No announcements,
// message history or per-delivery records are stored. Never seed from analytics
// or user settings: those also contain users seen only in groups/inline mode.
type broadcastRecipientModel struct {
	UserID  int64 `gorm:"primaryKey;autoIncrement:false"`
	Blocked bool  `gorm:"not null;default:false"`
}

func (broadcastRecipientModel) TableName() string { return "broadcast_recipients" }

var _ bot.BroadcastRepository = (*Repository)(nil)

func (r *Repository) RecordBroadcastRecipient(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("invalid private chat ID")
	}
	// A new private interaction proves reachability again, but must not reset
	// the separate, explicit opt-out in plugin_settings.
	return r.dataDB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{"blocked": false}),
		// Ordinary interactions need not rewrite an already reachable row.
		Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "blocked", Value: true}}},
	}).Create(&broadcastRecipientModel{UserID: id}).Error
}

func (r *Repository) broadcastRecipients(ctx context.Context) *gorm.DB {
	optedOut := r.dataDB.Model(&PluginSettingModel{}).Select("scope_id").Where(
		"scope_type = ? AND plugin = ? AND setting_key = ? AND setting_value = ?",
		bot.PluginScopeUser, bot.BroadcastSettingPlugin, bot.BroadcastSettingKey, "off",
	)
	return r.dataDB.WithContext(ctx).Model(&broadcastRecipientModel{}).
		Where("blocked = ? AND user_id > 0", false).Where("user_id NOT IN (?)", optedOut)
}

func (r *Repository) ListBroadcastRecipients(ctx context.Context) ([]int64, error) {
	var ids []int64
	err := r.broadcastRecipients(ctx).Order("user_id ASC").Pluck("user_id", &ids).Error
	return ids, err
}

func (r *Repository) CanBroadcastTo(ctx context.Context, id int64) (bool, error) {
	var count int64
	err := r.broadcastRecipients(ctx).Where("user_id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *Repository) BlockBroadcastRecipient(ctx context.Context, id int64) error {
	return r.dataDB.WithContext(ctx).Model(&broadcastRecipientModel{}).Where("user_id = ?", id).Update("blocked", true).Error
}
