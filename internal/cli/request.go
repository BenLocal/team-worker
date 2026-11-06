package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (a *app) requestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "request", Short: "Decision requests: the leader asks the operator to decide, the operator (or the AI auto reviewer) decides"}
	cmd.AddCommand(a.requestCreateCmd(), a.requestListCmd(), a.requestShowCmd(), a.requestDecideCmd(), a.requestAutoCmd())
	return cmd
}

func (a *app) requestCreateCmd() *cobra.Command {
	var body, bodyFile, recommend, from string
	var options []string
	var human bool
	cmd := &cobra.Command{
		Use:   "create TEAM SUMMARY",
		Short: "Ask the operator to decide something; the decision comes back as a decision_result message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if bodyFile != "" {
				if body != "" {
					return a.fail(fmt.Errorf("use --body or --body-file, not both"))
				}
				var data []byte
				var err error
				if bodyFile == "-" {
					data, err = io.ReadAll(cmd.InOrStdin())
				} else {
					data, err = os.ReadFile(bodyFile)
				}
				if err != nil {
					return a.fail(err)
				}
				body = string(data)
			}
			in := store.CreateDecisionInput{From: from, Summary: args[1], Body: body, Recommended: recommend, HumanOnly: human}
			if in.From == "" {
				in.From = config.IdentityFromEnv().AgentName
			}
			for _, o := range options {
				key, label, ok := strings.Cut(o, "=")
				if !ok {
					return a.fail(fmt.Errorf("invalid option %q: use KEY=LABEL, e.g. A=\"keep it as is\"", o))
				}
				in.Options = append(in.Options, model.DecisionOption{Key: key, Label: label})
			}
			r, err := a.client().CreateDecisionRequest(cmd.Context(), args[0], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "requested", "requestId": r.ID, "from": r.From}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Decision request %s sent to the operator; wait for the decision_result message\n", r.ID)
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&body, "body", "d", "", "Full text: background, what each option means, risks")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Read the body from this file (- for stdin)")
	cmd.Flags().StringArrayVar(&options, "option", nil, "An option as KEY=LABEL, e.g. A=\"keep it as is\" (repeat, in order)")
	cmd.Flags().StringVar(&recommend, "recommend", "", "Key of the option you recommend")
	cmd.Flags().BoolVar(&human, "human", false, "Only a human may decide; the AI auto reviewer leaves it alone")
	cmd.Flags().StringVar(&from, "from", "", "Requester (default: this agent's name)")
	return cmd
}

func (a *app) requestListCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "list TEAM",
		Short: "List a team's decision requests, pending first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := a.client().ListDecisionRequests(cmd.Context(), args[0], model.DecisionStatus(status))
			if err != nil {
				return a.fail(err)
			}
			a.emit(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "No decision requests")
					return
				}
				tw := newTable(w, "ID", "FROM", "STATUS", "CREATED", "SUMMARY")
				for _, r := range list {
					status := string(r.Status)
					if r.HumanOnly {
						status += " (human)"
					}
					row(tw, r.ID, r.From, status, formatTimestamp(r.CreatedAt), truncate(r.Summary, 60))
				}
				tw.Flush()
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&status, "status", "s", "", "Only requests with this status (pending, approved, rejected, answered)")
	return cmd
}

func (a *app) requestShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show TEAM REQUEST_ID",
		Short: "Show one decision request and its decision",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := a.client().GetDecisionRequest(cmd.Context(), args[0], args[1])
			if err != nil {
				return a.fail(err)
			}
			a.emit(r, func(w io.Writer) {
				fmt.Fprintf(w, "Request:   %s\nFrom:      %s\nStatus:    %s\nCreated:   %s\nSummary:   %s\n",
					r.ID, r.From, r.Status, formatTimestamp(r.CreatedAt), r.Summary)
				if r.HumanOnly {
					fmt.Fprintln(w, "Human:     only a human may decide")
				}
				for _, o := range r.Options {
					mark := ""
					if o.Key == r.Recommended {
						mark = " (recommended)"
					}
					fmt.Fprintf(w, "Option:    %s = %s%s\n", o.Key, o.Label, mark)
				}
				if r.DecidedBy != "" {
					fmt.Fprintf(w, "Decided:   %s by %s\n", formatTimestamp(r.DecidedAt), r.DecidedBy)
				}
				if r.Choice != "" {
					fmt.Fprintf(w, "Choice:    %s (%s)\n", r.Choice, r.OptionLabel(r.Choice))
				}
				if r.Reply != "" {
					fmt.Fprintf(w, "Reply:     %s\n", r.Reply)
				}
				if r.AutoReason != "" {
					fmt.Fprintf(w, "AI reason: %s\n", r.AutoReason)
				}
				if r.AutoNote != "" && r.Status == model.DecisionPending {
					fmt.Fprintf(w, "AI note:   %s\n", r.AutoNote)
				}
				if r.Body != "" {
					fmt.Fprintf(w, "\n%s\n", strings.TrimRight(r.Body, "\n"))
				}
			})
			return nil
		},
	}
}

func (a *app) requestDecideCmd() *cobra.Command {
	var choice, message, reply string
	var approve, reject bool
	cmd := &cobra.Command{
		Use:   "decide TEAM REQUEST_ID (--choice KEY [-m note] | --approve [-m note] | --reject -m reason | --reply text)",
		Short: "Decide a pending request as the operator, or override the AI auto reviewer's decision",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := store.DecideInput{Choice: choice, Reply: message, DecidedBy: a.decider()}
			actions := 0
			if choice != "" || approve {
				in.Action, actions = "approve", actions+1
			}
			if reject {
				in.Action, actions = "reject", actions+1
			}
			if reply != "" {
				if message != "" {
					return a.fail(fmt.Errorf("--reply is the answer itself; drop -m"))
				}
				in.Action, in.Reply, actions = "answer", reply, actions+1
			}
			if actions != 1 {
				return a.fail(fmt.Errorf("choose one of --choice KEY (or --approve without options), --reject -m <reason> or --reply <text>"))
			}
			r, err := a.client().DecideRequest(cmd.Context(), args[0], args[1], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": string(r.Status), "requestId": r.ID, "choice": r.Choice, "decidedBy": r.DecidedBy},
				func(w io.Writer) {
					fmt.Fprintf(w, "OK Decision request %s %s; '%s' has been told\n", r.ID, r.Status, r.From)
				})
			return nil
		},
	}
	cmd.Flags().StringVar(&choice, "choice", "", "Approve with this option key")
	cmd.Flags().BoolVar(&approve, "approve", false, "Approve a request that has no options")
	cmd.Flags().BoolVar(&reject, "reject", false, "Reject the request (needs -m with the reason)")
	cmd.Flags().StringVarP(&message, "message", "m", "", "Note with an approval, or the reason for a rejection")
	cmd.Flags().StringVar(&reply, "reply", "", "Answer the request with free text")
	return cmd
}

// decider is who decides from this CLI: the operator, or with --remote the
// remote's sender name. A spawned agent decides under its own name, which
// the daemon refuses, so agents cannot decide requests.
func (a *app) decider() string {
	if a.remote != "" || os.Getenv("TEAM_WORKER_AGENT_ID") != "" {
		return config.IdentityFromEnv().AgentName
	}
	return store.OperatorInbox
}

func (a *app) requestAutoCmd() *cobra.Command {
	var modelName, from, to, timeZone string
	var allDay, includeHuman bool
	cmd := &cobra.Command{
		Use:   "auto TEAM [on|off]",
		Short: "Show or switch the AI auto review of a team's decision requests",
		Long: `Show or switch the AI auto review of a team's decision requests.

With --from and --to the review only runs inside a daily window that
includes --from and excludes --to; an end before the start crosses
midnight (22:00-08:00 is 22:00 today until 08:00 tomorrow). Requests made
outside the window wait and are reviewed once it opens.

Human-only requests are left to a person unless --include-human=true; the
reviewer may then only approve them or leave them to the operator. Switching
on or off keeps the model, the window and --include-human set earlier unless
they are given.`,
		Example: `  team-worker request auto my-team on --from 22:00 --to 08:00 --tz Asia/Shanghai
  team-worker request auto my-team on --all-day
  team-worker request auto my-team on --include-human=true
  team-worker request auto my-team`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := a.client()
			flags := cmd.Flags()
			changed := flags.Changed("model") || flags.Changed("from") || flags.Changed("to") || flags.Changed("tz") || allDay ||
				flags.Changed("include-human")
			if allDay && (flags.Changed("from") || flags.Changed("to") || flags.Changed("tz")) {
				return a.fail(fmt.Errorf("--all-day clears the window; do not combine it with --from, --to or --tz"))
			}
			var setting model.AutoReviewStatus
			var err error
			switch {
			case len(args) == 1:
				if changed {
					return a.fail(fmt.Errorf("give on or off with --model, --from, --to, --tz, --all-day or --include-human"))
				}
				setting, err = c.AutoReview(cmd.Context(), args[0])
			case args[1] == "on" || args[1] == "off":
				// Switching on or off keeps the model and window set earlier.
				var current model.AutoReviewStatus
				if current, err = c.AutoReview(cmd.Context(), args[0]); err != nil {
					return a.fail(err)
				}
				next := current.AutoReview
				next.Enabled = args[1] == "on"
				if flags.Changed("model") {
					next.Model = modelName
				}
				if flags.Changed("from") {
					next.From = from
				}
				if flags.Changed("to") {
					next.To = to
				}
				if flags.Changed("tz") {
					next.TimeZone = timeZone
				}
				if allDay {
					next.From, next.To, next.TimeZone = "", "", ""
				}
				if flags.Changed("include-human") {
					next.IncludeHuman = includeHuman
				}
				setting, err = c.SetAutoReview(cmd.Context(), args[0], next)
			default:
				return a.fail(fmt.Errorf("invalid state %q: use on or off", args[1]))
			}
			if err != nil {
				return a.fail(err)
			}
			a.emit(setting, func(w io.Writer) {
				fmt.Fprintf(w, "Auto review for %s: %s\n", args[0], describeAutoReview(setting))
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&modelName, "model", "", "Model the reviewer runs with; \"\" for the reviewer command's default (default: keep the current one)")
	cmd.Flags().StringVar(&from, "from", "", "Start of the daily review window, HH:MM 24-hour (default: keep the current one)")
	cmd.Flags().StringVar(&to, "to", "", "End of the daily review window, HH:MM 24-hour, excluded; before --from crosses midnight (default: keep the current one)")
	cmd.Flags().StringVar(&timeZone, "tz", "", "IANA time zone of the window, e.g. Asia/Shanghai; \"\" for the daemon's local time (default: keep the current one)")
	cmd.Flags().BoolVar(&allDay, "all-day", false, "Clear the window and review all day")
	cmd.Flags().BoolVar(&includeHuman, "include-human", false,
		"Also review human-only requests, which the AI may only approve; --include-human=false leaves them to a person (default: keep the current one)")
	return cmd
}

// describeAutoReview is the one-line state of an auto review setting, e.g.
// "on (model: default), 22:00-08:00 Asia/Shanghai, human-only requests too,
// active now".
func describeAutoReview(s model.AutoReviewStatus) string {
	state := "off"
	if s.Enabled {
		state = "on"
	}
	m := s.Model
	if m == "" {
		m = "default"
	}
	text := fmt.Sprintf("%s (model: %s)", state, m)
	if s.HasWindow() {
		zone := s.TimeZone
		if zone == "" {
			zone = "daemon time (" + s.DaemonZone + ")"
		}
		text += fmt.Sprintf(", %s-%s %s", s.From, s.To, zone)
	}
	if s.IncludeHuman {
		text += ", human-only requests too"
	}
	switch {
	case s.Active:
		text += ", active now"
	case s.Enabled:
		text += ", outside the window now"
	}
	return text
}
