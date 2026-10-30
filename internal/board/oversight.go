package board

import (
	"context"
	"errors"
	"net/http"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/model"
)

// oversightView is everything the page shows about approvals and spending.
type oversightView struct {
	Plans        []model.Plan        `json:"plans"`
	JoinRequests []model.JoinRequest `json:"joinRequests"`
	Budget       model.Budget        `json:"budget"`
	Cost         model.CostSummary   `json:"cost"`
	// Harness is the team's latest run, or nil when it never started one.
	Harness *model.HarnessStatus `json:"harness"`
}

func oversight(ctx context.Context, c *client.Client, team string) (oversightView, error) {
	var v oversightView
	var err error
	if v.Plans, err = c.ListPlans(ctx, team, "", ""); err != nil {
		return v, err
	}
	if v.JoinRequests, err = c.ListJoinRequests(ctx, team, ""); err != nil {
		return v, err
	}
	if v.Budget, err = c.Budget(ctx, team); err != nil {
		return v, err
	}
	costs, err := c.Costs(ctx, team, "")
	if err != nil {
		return v, err
	}
	v.Cost = costs.Summary
	h, err := c.HarnessStatus(ctx, team, "current")
	var apiErr *client.APIError
	switch {
	case err == nil:
		v.Harness = &h
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
	default:
		return v, err
	}
	if v.Plans == nil {
		v.Plans = []model.Plan{}
	}
	if v.JoinRequests == nil {
		v.JoinRequests = []model.JoinRequest{}
	}
	return v, nil
}
