package db

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This table lives in data.db, independently of removable song caches.
// No message bodies, chat history or per-request timestamps are retained.
type userActivityModel struct {
	UserID       int64 `gorm:"primaryKey;autoIncrement:false"`
	Username     string
	DisplayName  string
	FirstSeenAt  time.Time `gorm:"not null"`
	LastSeenAt   time.Time `gorm:"not null;index"`
	RequestCount int64     `gorm:"not null"`
}

func (userActivityModel) TableName() string { return "user_activity" }

var _ bot.UserActivityRepository = (*Repository)(nil)

func (r *Repository) RecordUserActivity(ctx context.Context, userID int64, username, displayName string, at time.Time) error {
	if userID <= 0 || at.IsZero() {
		return errors.New("invalid user activity")
	}
	if r == nil || r.dataDB == nil {
		return errors.New("activity repository not configured")
	}
	compact := func(s string, limit int) string {
		value := []rune(strings.Join(strings.Fields(s), " "))
		if len(value) > limit {
			value = value[:limit]
		}
		return string(value)
	}
	row := userActivityModel{
		UserID: userID, Username: compact(username, 64), DisplayName: compact(displayName, 128),
		FirstSeenAt: at.UTC(), LastSeenAt: at.UTC(), RequestCount: 1,
	}
	// Atomic upsert: concurrent requests must not lose increments or move the
	// latest timestamp/profile backwards when workers finish out of order.
	return r.dataDB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"first_seen_at": gorm.Expr("MIN(user_activity.first_seen_at, excluded.first_seen_at)"),
			"last_seen_at":  gorm.Expr("MAX(user_activity.last_seen_at, excluded.last_seen_at)"),
			"username":      gorm.Expr("CASE WHEN excluded.last_seen_at >= user_activity.last_seen_at THEN excluded.username ELSE user_activity.username END"),
			"display_name":  gorm.Expr("CASE WHEN excluded.last_seen_at >= user_activity.last_seen_at THEN excluded.display_name ELSE user_activity.display_name END"),
			"request_count": gorm.Expr("user_activity.request_count + 1"),
		}),
	}).Create(&row).Error
}

// Calendar windows use the supplied server-local time, stored/computed in UTC.
// The last seven days means today plus the preceding six calendar days.
func (r *Repository) GetUserActivityStats(ctx context.Context, now time.Time, excludedUserIDs ...int64) (bot.UserActivityStats, error) {
	var result bot.UserActivityStats
	if r == nil || r.dataDB == nil {
		return result, errors.New("activity repository not configured")
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	err := userActivityQuery(r.dataDB.WithContext(ctx), excludedUserIDs).Select(
		`COUNT(*) AS total_users,
		 COALESCE(SUM(CASE WHEN last_seen_at >= ? AND last_seen_at <= ? THEN 1 ELSE 0 END), 0) AS active_today,
		 COALESCE(SUM(CASE WHEN last_seen_at >= ? AND last_seen_at <= ? THEN 1 ELSE 0 END), 0) AS active7_days`,
		today.UTC(), now.UTC(), today.AddDate(0, 0, -6).UTC(), now.UTC(),
	).Scan(&result).Error
	return result, err
}

func (r *Repository) ListUserActivity(ctx context.Context, page, pageSize int, excludedUserIDs ...int64) (bot.UserActivityPage, error) {
	result := bot.UserActivityPage{Page: max(page, 1), TotalPages: 1}
	if r == nil || r.dataDB == nil {
		return result, errors.New("activity repository not configured")
	}
	pageSize = min(max(pageSize, 1), 50)
	err := r.dataDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := userActivityQuery(tx, excludedUserIDs).Count(&result.TotalUsers).Error; err != nil {
			return err
		}
		result.TotalPages = max(1, int((result.TotalUsers+int64(pageSize)-1)/int64(pageSize)))
		result.Page = min(result.Page, result.TotalPages)
		var rows []userActivityModel
		if err := userActivityQuery(tx, excludedUserIDs).Order("last_seen_at DESC, user_id ASC").Limit(pageSize).Offset((result.Page - 1) * pageSize).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			result.Users = append(result.Users, bot.UserActivity{
				UserID: row.UserID, Username: row.Username, DisplayName: row.DisplayName,
				FirstSeenAt: row.FirstSeenAt, LastSeenAt: row.LastSeenAt, RequestCount: row.RequestCount,
			})
		}
		return nil
	})
	return result, err
}

// Keep existing rows intact; applying the same filter before COUNT and LIMIT
// avoids inflated totals or partially empty pages when admins have old records.
func userActivityQuery(tx *gorm.DB, excludedUserIDs []int64) *gorm.DB {
	query := tx.Model(&userActivityModel{})
	if len(excludedUserIDs) > 0 {
		query = query.Where("user_id NOT IN ?", excludedUserIDs)
	}
	return query
}
