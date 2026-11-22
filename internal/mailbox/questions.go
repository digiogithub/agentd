package mailbox

import "context"

// OpenQuestions returns the questions still waiting for an answer (kind
// question, state not replied), oldest first. sender and recipient filter
// when non-empty. A question is closed by Reply, like any message.
func (s *Store) OpenQuestions(ctx context.Context, sender, recipient string) ([]Message, error) {
	q := `SELECT ` + selectCols + ` FROM messages WHERE kind = 'question' AND state <> 'replied'`
	var args []any
	if sender != "" {
		q += ` AND sender = ?`
		args = append(args, sender)
	}
	if recipient != "" {
		q += ` AND recipient = ?`
		args = append(args, recipient)
	}
	return s.list(ctx, q+` ORDER BY id`, args...)
}

// ValidTaskID reports whether id is a valid question task id
// ([A-Za-z0-9._-]{1,64}).
func ValidTaskID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
