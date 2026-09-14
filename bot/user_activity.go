package bot

import (
	"context"
	"time"
)

// UserActivity is one aggregate per Telegram user, not an interaction log.
type UserActivity struct {
	UserID       int64
	Username     string
	DisplayName  string
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	RequestCount int64
}

type UserActivityStats struct {
	TotalUsers  int64
	ActiveToday int64
	Active7Days int64
}

type UserActivityPage struct {
	Users      []UserActivity
	TotalUsers int64
	Page       int
	TotalPages int
}

// Kept separate from SongRepository so upstream platform and cache interfaces
// do not need to know about optional administrative analytics.
type UserActivityRepository interface {
	RecordUserActivity(context.Context, int64, string, string, time.Time) error
	// Optional excluded IDs are filtered before aggregation and pagination.
	GetUserActivityStats(context.Context, time.Time, ...int64) (UserActivityStats, error)
	ListUserActivity(context.Context, int, int, ...int64) (UserActivityPage, error)
}
