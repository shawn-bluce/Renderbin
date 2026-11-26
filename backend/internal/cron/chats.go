package cron

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// CollectChats removes "Ask AI" conversations whose file was permanently
// deleted or whose account was, and the messages of any conversation that is
// gone. The schema has no cascade for these on purpose (see
// migrations/0013_ai_chat.sql: a trigger would undo a backup restore), and
// rows left behind are invisible, since ids are never reused; this only frees
// the space they take.
//
// It also removes document comments (file_comments) whose file or author is
// gone, or whose parent comment is: the same schema rule, the same job. They are
// counted with the conversations.
func CollectChats(ctx context.Context, conn *sql.DB) (conversations, messages int64, err error) {
	queries := sqlcgen.New(conn)
	if conversations, err = queries.DeleteOrphanedChatConversations(ctx); err != nil {
		return 0, 0, fmt.Errorf("delete orphaned conversations: %w", err)
	}
	if messages, err = queries.DeleteOrphanedChatMessages(ctx); err != nil {
		return conversations, 0, fmt.Errorf("delete orphaned messages: %w", err)
	}
	comments, err := queries.DeleteOrphanedFileComments(ctx)
	if err != nil {
		return conversations, messages, fmt.Errorf("delete orphaned comments: %w", err)
	}
	return conversations + comments, messages, nil
}
