package store

import "context"

func (s *Store) CountStewardDecisions(ctx context.Context, status string) (int, error) {
	query := `SELECT COUNT(*) FROM steward_decisions`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	var count int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}
