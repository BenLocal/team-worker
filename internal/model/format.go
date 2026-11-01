package model

import (
	"fmt"
	"strings"
	"time"
)

// FormatMessages renders an agent's new messages as one block of text for
// the agent to read: what the Stop and PostToolUse hooks hand over and what
// the daemon pastes into a waiting agent's pane.
func FormatMessages(team string, msgs []Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You have %d new team message(s) in team %s:\n", len(msgs), team)
	shutdown := false
	for _, m := range msgs {
		kind := string(m.Type)
		if m.Type == "shutdown_request" {
			kind += ", request " + m.RequestID
			shutdown = true
		}
		fmt.Fprintf(&b, "\n[%s] from %s (%s):\n%s\n", messageTime(m.Timestamp), m.From, kind, m.Content)
	}
	b.WriteString("\nHandle these messages now, then continue with your work.")
	if shutdown {
		fmt.Fprintf(&b, " For a shutdown request: finish or hand back your current task, run "+
			"`team-worker lifecycle approve-shutdown %s <request-id>`, then exit.", team)
	}
	return b.String()
}

// messageTime shows a stored timestamp in local time.
func messageTime(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
