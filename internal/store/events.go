package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
)

// EventHookInput adds a user event hook.
type EventHookInput struct {
	Event string `json:"event"`
	// Action must be empty or "shell".
	Action   string `json:"action"`
	Command  string `json:"command"`
	Team     string `json:"team"`
	Priority int    `json:"priority"`
}

// AddEventHook stores a hook that runs Command when an Event happens.
func (s *Store) AddEventHook(ctx context.Context, in EventHookInput) (model.EventHook, error) {
	if _, ok := model.LookupEventType(in.Event); !ok {
		return model.EventHook{}, invalidf("Unknown event type '%s'; see `team-worker event types`", in.Event)
	}
	if in.Action != "" && in.Action != "shell" {
		return model.EventHook{}, invalidf("Unsupported hook action '%s': only shell hooks are supported", in.Action)
	}
	if strings.TrimSpace(in.Command) == "" {
		return model.EventHook{}, invalidf("Hook command must not be empty")
	}
	if err := validateOptional(in.Team, "team name"); err != nil {
		return model.EventHook{}, err
	}
	h := model.EventHook{ID: randomHex(8), Event: in.Event, Action: "shell", Command: in.Command,
		Priority: in.Priority, Enabled: true, Team: in.Team, CreatedAt: s.timestamp()}
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO event_hooks (id, event, command, team, priority, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			h.ID, h.Event, h.Command, h.Team, h.Priority, h.CreatedAt)
		return err
	})
	return h, err
}

// ListEventHooks returns every hook in the order they run: by priority,
// then by when they were added.
func (s *Store) ListEventHooks(ctx context.Context) ([]model.EventHook, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, event, command, team, priority, created_at FROM event_hooks ORDER BY priority, rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hooks := []model.EventHook{}
	for rows.Next() {
		h := model.EventHook{Action: "shell", Enabled: true}
		if err := rows.Scan(&h.ID, &h.Event, &h.Command, &h.Team, &h.Priority, &h.CreatedAt); err != nil {
			return nil, err
		}
		hooks = append(hooks, h)
	}
	return hooks, rows.Err()
}

// RemoveEventHooks removes the hooks of an event type, only those with the
// given command when it is not empty, and returns how many it removed.
func (s *Store) RemoveEventHooks(ctx context.Context, event, command string) (int, error) {
	if event == "" {
		return 0, invalidf("An event type is required")
	}
	var removed int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		query, args := "DELETE FROM event_hooks WHERE event = ?", []any{event}
		if command != "" {
			query, args = query+" AND command = ?", append(args, command)
		}
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		removed, err = res.RowsAffected()
		return err
	})
	return int(removed), err
}

// PendingEvents returns up to limit events whose hooks have not run yet,
// oldest first.
func (s *Store) PendingEvents(ctx context.Context, limit int) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, team, type, body FROM event_outbox ORDER BY id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []model.Event
	for rows.Next() {
		var e model.Event
		var body string
		if err := rows.Scan(&e.ID, &e.Team, &e.Type, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &e.Payload); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// EventDone forgets an event whose hooks have run. It does not count as a
// change: nothing a client reads depends on the outbox.
func (s *Store) EventDone(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM event_outbox WHERE id = ?", id)
	return err
}

// TestEvent builds a made-up event of the given type for `event hook test`
// and returns the hooks it would run. Fields are empty except agent_name.
func (s *Store) TestEvent(ctx context.Context, event, team, agent string) (map[string]any, []model.EventHook, error) {
	typ, ok := model.LookupEventType(event)
	if !ok {
		return nil, nil, invalidf("Unknown event type '%s'; see `team-worker event types`", event)
	}
	hooks, err := s.ListEventHooks(ctx)
	if err != nil {
		return nil, nil, err
	}
	fields := map[string]any{}
	for _, f := range typ.Fields {
		fields[f] = ""
	}
	if _, ok := fields["agent_name"]; ok {
		fields["agent_name"] = agent
	}
	matching := []model.EventHook{}
	for _, h := range hooks {
		if h.Matches(typ.Name, team) {
			matching = append(matching, h)
		}
	}
	return EventPayload(typ.Name, team, s.timestamp(), fields), matching, nil
}

// EventPayload builds what a hook receives: the event's fields plus type,
// team_name and timestamp.
func EventPayload(typ, team, timestamp string, fields map[string]any) map[string]any {
	p := make(map[string]any, len(fields)+3)
	for k, v := range fields {
		p[k] = v
	}
	p["type"], p["team_name"], p["timestamp"] = typ, team, timestamp
	return p
}

// emit records an event in the caller's transaction, so it is kept exactly
// when the change that caused it commits. Events no hook listens to are not
// recorded.
func (s *Store) emit(ctx context.Context, tx *sql.Tx, team, typ string, fields map[string]any) error {
	now := s.timestamp()
	body, err := json.Marshal(EventPayload(typ, team, now, fields))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO event_outbox (team, type, body, created_at)
		 SELECT ?, ?, ?, ? WHERE EXISTS (
		   SELECT 1 FROM event_hooks WHERE event = ? AND (team = '' OR team = ?))`,
		team, typ, string(body), now, typ, team)
	return err
}
