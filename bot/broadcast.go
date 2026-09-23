package bot

import "context"

const (
	BroadcastSettingPlugin = "telegram"
	BroadcastSettingKey    = "update_notifications"
)

// BroadcastRepository is independent of music caches and analytics. Only an
// observed private conversation establishes a recipient; a user ID alone does not.
type BroadcastRepository interface {
	RecordBroadcastRecipient(context.Context, int64) error
	ListBroadcastRecipients(context.Context) ([]int64, error)
	CanBroadcastTo(context.Context, int64) (bool, error)
	BlockBroadcastRecipient(context.Context, int64) error
}
