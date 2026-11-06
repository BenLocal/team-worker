package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/benlocal/team-worker/internal/model"
)

// OperatorInbox is the inbox decision requests are delivered to.
const OperatorInbox = "operator"

const (
	maxDecisionSummary = 500
	maxDecisionOptions = 20
	maxOptionKey       = 32
	maxOptionLabel     = 500
)

type CreateDecisionInput struct {
	From        string                 `json:"from"`
	Summary     string                 `json:"summary"`
	Body        string                 `json:"body"`
	Options     []model.DecisionOption `json:"options"`
	Recommended string                 `json:"recommended"`
	HumanOnly   bool                   `json:"humanOnly"`
}

// DecideInput is one decision on a request. Action is approve (with Choice
// when the request has options), reject (Reply is the reason) or answer
// (Reply is the answer).
type DecideInput struct {
	Action    string `json:"action"`
	Choice    string `json:"choice"`
	Reply     string `json:"reply"`
	DecidedBy string `json:"decidedBy"`
}

const decisionColumns = "id, team, from_name, summary, body, options, recommended, human_only, status, choice, reply, " +
	"decided_by, auto_reason, auto_note, created_at, decided_at"

func scanDecision(row scanner) (model.DecisionRequest, error) {
	var r model.DecisionRequest
	var options string
	err := row.Scan(&r.ID, &r.Team, &r.From, &r.Summary, &r.Body, &options, &r.Recommended, &r.HumanOnly, &r.Status,
		&r.Choice, &r.Reply, &r.DecidedBy, &r.AutoReason, &r.AutoNote, &r.CreatedAt, &r.DecidedAt)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(options), &r.Options); err != nil {
		return r, fmt.Errorf("decision request %s options: %w", r.ID, err)
	}
	if r.Options == nil {
		r.Options = []model.DecisionOption{}
	}
	return r, nil
}

func validateDecisionInput(in *CreateDecisionInput) error {
	if err := validate(in.From, "requester name"); err != nil {
		return err
	}
	in.Summary = strings.TrimSpace(in.Summary)
	if in.Summary == "" {
		return invalidf("Decision request summary must not be empty")
	}
	if strings.ContainsAny(in.Summary, "\r\n") || len(in.Summary) > maxDecisionSummary {
		return invalidf("Decision request summary must be one line of at most %d bytes; put details in the body", maxDecisionSummary)
	}
	if len(in.Body) > MaxPlanLength {
		return invalidf("Decision request body must be text of at most %d bytes", MaxPlanLength)
	}
	if len(in.Options) > maxDecisionOptions {
		return invalidf("A decision request takes at most %d options", maxDecisionOptions)
	}
	seen := map[string]bool{}
	for i := range in.Options {
		o := &in.Options[i]
		o.Key, o.Label = strings.TrimSpace(o.Key), strings.TrimSpace(o.Label)
		if o.Key == "" || len(o.Key) > maxOptionKey || strings.ContainsAny(o.Key, " \t\r\n=") {
			return invalidf("Option key '%s' must be 1-%d characters without spaces or '='", o.Key, maxOptionKey)
		}
		if o.Label == "" || len(o.Label) > maxOptionLabel {
			return invalidf("Option '%s' needs a label of at most %d bytes", o.Key, maxOptionLabel)
		}
		if seen[o.Key] {
			return invalidf("Option key '%s' is used twice", o.Key)
		}
		seen[o.Key] = true
	}
	in.Recommended = strings.TrimSpace(in.Recommended)
	if in.Recommended != "" && !seen[in.Recommended] {
		return invalidf("Recommended option '%s' is not one of the options", in.Recommended)
	}
	return nil
}

// CreateDecisionRequest stores a pending request and tells the operator in
// the same transaction.
func (s *Store) CreateDecisionRequest(ctx context.Context, team string, in CreateDecisionInput) (model.DecisionRequest, error) {
	if err := validateDecisionInput(&in); err != nil {
		return model.DecisionRequest{}, err
	}
	if in.Options == nil {
		in.Options = []model.DecisionOption{}
	}
	options, err := json.Marshal(in.Options)
	if err != nil {
		return model.DecisionRequest{}, err
	}
	r := model.DecisionRequest{ID: randomHex(12), Team: team, From: in.From, Summary: in.Summary, Body: in.Body,
		Options: in.Options, Recommended: in.Recommended, HumanOnly: in.HumanOnly, Status: model.DecisionPending,
		CreatedAt: s.timestamp()}
	err = s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO decision_requests
			(team, id, from_name, summary, body, options, recommended, human_only, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			team, r.ID, r.From, r.Summary, r.Body, string(options), r.Recommended, r.HumanOnly, r.Status, r.CreatedAt); err != nil {
			return err
		}
		return deliver(ctx, tx, team, OperatorInbox, model.Message{Type: "decision_request", From: r.From, To: OperatorInbox,
			RequestID: r.ID, Timestamp: r.CreatedAt, Summary: r.Summary, Content: requestText(r)})
	})
	return r, err
}

// requestText is the operator's message for a new request.
func requestText(r model.DecisionRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Decision request %s from %s: %s\n", r.ID, r.From, r.Summary)
	if r.HumanOnly {
		b.WriteString("(human decision only)\n")
	}
	if r.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimRight(r.Body, "\n"))
	}
	if len(r.Options) > 0 {
		b.WriteString("\nOptions:\n")
		for _, o := range r.Options {
			mark := ""
			if o.Key == r.Recommended {
				mark = " (recommended)"
			}
			fmt.Fprintf(&b, "- %s: %s%s\n", o.Key, o.Label, mark)
		}
	}
	fmt.Fprintf(&b, "\nDecide with `team-worker request decide %s %s --choice <key> | --reject -m <reason> | --reply <text>`, or on the board.",
		r.Team, r.ID)
	return b.String()
}

// DecideRequest records the operator's decision and tells the requester in
// the same transaction. Only a pending request can be decided, except that
// the operator may override a decision of the AI auto reviewer.
func (s *Store) DecideRequest(ctx context.Context, team, id string, in DecideInput) (model.DecisionRequest, error) {
	if model.IsAutoReviewer(in.DecidedBy) {
		return model.DecisionRequest{}, invalidf("'%s' is reserved for the AI auto reviewer", in.DecidedBy)
	}
	return s.decide(ctx, team, id, in, "")
}

// AutoDecideRequest records the AI auto reviewer's decision on a pending
// request, with its rationale. in.DecidedBy must be the reviewer's name.
func (s *Store) AutoDecideRequest(ctx context.Context, team, id string, in DecideInput, reason string) (model.DecisionRequest, error) {
	if !model.IsAutoReviewer(in.DecidedBy) {
		return model.DecisionRequest{}, invalidf("'%s' is not the AI auto reviewer", in.DecidedBy)
	}
	if strings.TrimSpace(reason) == "" {
		return model.DecisionRequest{}, invalidf("An AI decision needs a reason")
	}
	return s.decide(ctx, team, id, in, reason)
}

func (s *Store) decide(ctx context.Context, team, id string, in DecideInput, reason string) (model.DecisionRequest, error) {
	auto := reason != ""
	// The reviewer's name carries the model, e.g. auto:group/x.
	if !auto {
		if err := validate(in.DecidedBy, "decider name"); err != nil {
			return model.DecisionRequest{}, err
		}
	}
	if len(in.Reply) > MaxPlanLength {
		return model.DecisionRequest{}, invalidf("Reply must be text of at most %d bytes", MaxPlanLength)
	}
	in.Choice, in.Reply = strings.TrimSpace(in.Choice), strings.TrimSpace(in.Reply)
	var r model.DecisionRequest
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		if r, err = getDecision(ctx, tx, team, id); err != nil {
			return err
		}
		if !auto {
			members, err := listMembers(ctx, tx, team)
			if err != nil {
				return err
			}
			for _, m := range members {
				if m.Name == in.DecidedBy {
					return conflictf("Decision requests are decided by the operator; '%s' is a member of team '%s'", in.DecidedBy, team)
				}
			}
		}
		previous := r
		override := false
		switch {
		case r.Status == model.DecisionPending:
		case !auto && model.IsAutoReviewer(r.DecidedBy):
			override = true
		default:
			return conflictf("Decision request '%s' is already %s by %s", id, r.Status, r.DecidedBy)
		}
		if auto && r.HumanOnly {
			return conflictf("Decision request '%s' must be decided by a human", id)
		}
		status, err := decisionStatus(r, in)
		if err != nil {
			return err
		}
		r.Status, r.Choice, r.Reply, r.DecidedBy, r.DecidedAt = status, in.Choice, in.Reply, in.DecidedBy, s.timestamp()
		if auto {
			r.AutoReason = reason
		}
		if _, err := tx.ExecContext(ctx, `UPDATE decision_requests SET status = ?, choice = ?, reply = ?, decided_by = ?,
			decided_at = ?, auto_reason = ? WHERE team = ? AND id = ?`,
			r.Status, r.Choice, r.Reply, r.DecidedBy, r.DecidedAt, r.AutoReason, team, id); err != nil {
			return err
		}
		inbox, err := resolveInbox(ctx, tx, team, r.From, "")
		if err != nil {
			return err
		}
		msg := model.Message{Type: "decision_result", From: r.DecidedBy, To: inbox, RequestID: r.ID, Timestamp: r.DecidedAt,
			Summary: r.Summary, Status: string(r.Status), Choice: r.Choice, Feedback: r.Reply, Content: resultText(r, previous, override)}
		if auto {
			msg.Reason = reason
		}
		return deliver(ctx, tx, team, inbox, msg)
	})
	return r, err
}

// decisionStatus checks in against r and returns the status it leads to.
func decisionStatus(r model.DecisionRequest, in DecideInput) (model.DecisionStatus, error) {
	switch in.Action {
	case "approve":
		if len(r.Options) == 0 {
			if in.Choice != "" {
				return "", invalidf("Decision request '%s' has no options to choose from", r.ID)
			}
		} else if r.OptionLabel(in.Choice) == "" {
			keys := make([]string, len(r.Options))
			for i, o := range r.Options {
				keys[i] = o.Key
			}
			return "", invalidf("Choose one of the options: %s", strings.Join(keys, ", "))
		}
		return model.DecisionApproved, nil
	case "reject":
		if in.Choice != "" {
			return "", invalidf("A rejection takes no choice")
		}
		if in.Reply == "" {
			return "", invalidf("A rejection needs a reason")
		}
		return model.DecisionRejected, nil
	case "answer":
		if in.Choice != "" {
			return "", invalidf("An answer takes no choice; approve to choose an option")
		}
		if in.Reply == "" {
			return "", invalidf("An answer must not be empty")
		}
		return model.DecisionAnswered, nil
	}
	return "", invalidf("Invalid action '%s'; use approve, reject or answer", in.Action)
}

// resultText is the requester's message for a decision.
func resultText(r, previous model.DecisionRequest, override bool) string {
	var b strings.Builder
	if override {
		fmt.Fprintf(&b, "This overrides the earlier AI decision (%s by %s).\n", decisionLine(previous), previous.DecidedBy)
	}
	fmt.Fprintf(&b, "Decision request %s (%s): %s by %s", r.ID, r.Summary, decisionLine(r), r.DecidedBy)
	if model.IsAutoReviewer(r.DecidedBy) {
		b.WriteString(" (AI auto review on behalf of the operator)")
	}
	b.WriteString("\n")
	if r.Reply != "" {
		label := "Note"
		switch r.Status {
		case model.DecisionRejected:
			label = "Reason"
		case model.DecisionAnswered:
			label = "Answer"
		}
		fmt.Fprintf(&b, "%s: %s\n", label, r.Reply)
	}
	if model.IsAutoReviewer(r.DecidedBy) && r.AutoReason != "" {
		fmt.Fprintf(&b, "AI reason: %s\n", r.AutoReason)
	}
	return strings.TrimRight(b.String(), "\n")
}

func decisionLine(r model.DecisionRequest) string {
	if r.Status == model.DecisionApproved && r.Choice != "" {
		return fmt.Sprintf("approved %s (%s)", r.Choice, r.OptionLabel(r.Choice))
	}
	return string(r.Status)
}

func (s *Store) GetDecisionRequest(ctx context.Context, team, id string) (model.DecisionRequest, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return model.DecisionRequest{}, err
	}
	return getDecision(ctx, s.db, team, id)
}

func getDecision(ctx context.Context, q queryer, team, id string) (model.DecisionRequest, error) {
	r, err := scanDecision(q.QueryRowContext(ctx, "SELECT "+decisionColumns+" FROM decision_requests WHERE team = ? AND id = ?", team, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, notFoundf("Decision request '%s' not found in team '%s'", id, team)
	}
	return r, err
}

// ListDecisionRequests returns a team's requests, pending first, then newest
// first, optionally only one status.
func (s *Store) ListDecisionRequests(ctx context.Context, team string, status model.DecisionStatus) ([]model.DecisionRequest, error) {
	if status != "" && !model.ValidDecisionStatus(status) {
		return nil, invalidf("Invalid decision request status '%s'; use pending, approved, rejected or answered", status)
	}
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	query := "SELECT " + decisionColumns + " FROM decision_requests WHERE team = ?"
	args := []any{team}
	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY status != 'pending', created_at DESC, id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DecisionRequest{}
	for rows.Next() {
		r, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AutoReviewSetting returns a team's AI auto review setting.
func (s *Store) AutoReviewSetting(ctx context.Context, team string) (model.AutoReview, error) {
	if err := validate(team, "team name"); err != nil {
		return model.AutoReview{}, err
	}
	a, err := scanAutoReview(s.db.QueryRowContext(ctx, "SELECT "+autoReviewColumns+" FROM teams WHERE name = ?", team))
	if errors.Is(err, sql.ErrNoRows) {
		return a, notFoundf("Team '%s' not found", team)
	}
	return a, err
}

const autoReviewColumns = "auto_review, auto_review_model, auto_review_from, auto_review_to, auto_review_time_zone"

func scanAutoReview(row scanner) (model.AutoReview, error) {
	var a model.AutoReview
	err := row.Scan(&a.Enabled, &a.Model, &a.From, &a.To, &a.TimeZone)
	return a, err
}

// SetAutoReview replaces a team's AI auto review setting.
func (s *Store) SetAutoReview(ctx context.Context, team string, a model.AutoReview) (model.AutoReview, error) {
	a.Model = strings.TrimSpace(a.Model)
	if len(a.Model) > 200 || strings.ContainsAny(a.Model, " \t\r\n") {
		return a, invalidf("Model must be a name of at most 200 characters without spaces")
	}
	if err := validateAutoReviewWindow(&a); err != nil {
		return a, err
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE teams SET auto_review = ?, auto_review_model = ?,
			auto_review_from = ?, auto_review_to = ?, auto_review_time_zone = ? WHERE name = ?`,
			a.Enabled, a.Model, a.From, a.To, a.TimeZone, team)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundf("Team '%s' not found", team)
		}
		return nil
	})
	return a, err
}

// validateAutoReviewWindow trims the window fields of a and checks them.
func validateAutoReviewWindow(a *model.AutoReview) error {
	a.From, a.To, a.TimeZone = strings.TrimSpace(a.From), strings.TrimSpace(a.To), strings.TrimSpace(a.TimeZone)
	if !a.HasWindow() {
		if a.TimeZone != "" {
			return invalidf("A time zone needs a window: set both from and to")
		}
		return nil
	}
	if a.From == "" || a.To == "" {
		return invalidf("Set both from and to for a window, or neither to review all day")
	}
	from, err := model.ParseClock(a.From)
	if err != nil {
		return invalidf("From: %v", err)
	}
	to, err := model.ParseClock(a.To)
	if err != nil {
		return invalidf("To: %v", err)
	}
	if from == to {
		return invalidf("From and to must differ; leave both empty to review all day")
	}
	if _, err := a.Location(); err != nil {
		return invalidf("Unknown time zone %q: use an IANA name like Asia/Shanghai", a.TimeZone)
	}
	return nil
}

// AutoReviewItem is a request waiting for the AI auto reviewer, with its
// team's setting.
type AutoReviewItem struct {
	Request model.DecisionRequest
	Setting model.AutoReview
}

// AutoReviewQueue returns, oldest first, the pending requests the AI auto
// reviewer has not looked at in teams that turned auto review on, whatever
// their window. Human-only requests are left out.
func (s *Store) AutoReviewQueue(ctx context.Context) ([]AutoReviewItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixed("t.", autoReviewColumns)+`, `+prefixed("d.", decisionColumns)+`
		FROM decision_requests d JOIN teams t ON t.name = d.team
		WHERE t.auto_review = 1 AND d.status = 'pending' AND d.human_only = 0 AND d.auto_attempted = 0
		ORDER BY d.created_at, d.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AutoReviewItem
	for rows.Next() {
		var item AutoReviewItem
		a := &item.Setting
		if item.Request, err = scanDecision(prefixScanner{rows, []any{&a.Enabled, &a.Model, &a.From, &a.To, &a.TimeZone}}); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// prefixScanner scans the leading columns into first, then the rest as the
// wrapped row's caller asks.
type prefixScanner struct {
	row   scanner
	first []any
}

func (p prefixScanner) Scan(dest ...any) error {
	return p.row.Scan(append(slices.Clip(p.first), dest...)...)
}

func prefixed(prefix, columns string) string {
	cols := strings.Split(columns, ", ")
	for i, c := range cols {
		cols[i] = prefix + c
	}
	return strings.Join(cols, ", ")
}

// ClaimAutoReview marks a pending request as looked at by the AI auto
// reviewer and reports whether this call claimed it, so a request is
// reviewed at most once. Nothing is claimed while the team's auto review is
// off or outside its window at now.
func (s *Store) ClaimAutoReview(ctx context.Context, team, id string, now time.Time) (bool, error) {
	var claimed bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		a, err := scanAutoReview(tx.QueryRowContext(ctx, "SELECT "+autoReviewColumns+" FROM teams WHERE name = ?", team))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return err
		case !a.Active(now):
			return nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE decision_requests SET auto_attempted = 1
			WHERE team = ? AND id = ? AND status = 'pending' AND human_only = 0 AND auto_attempted = 0`, team, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		claimed = n > 0
		return err
	})
	return claimed, err
}

// NoteAutoReview records why the AI auto reviewer left a request pending.
func (s *Store) NoteAutoReview(ctx context.Context, team, id, note string) error {
	if utf8.RuneCountInString(note) > 2000 {
		note = firstRunes(note, 2000)
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE decision_requests SET auto_note = ?, auto_attempted = 1 WHERE team = ? AND id = ?",
			note, team, id)
		return err
	})
}
