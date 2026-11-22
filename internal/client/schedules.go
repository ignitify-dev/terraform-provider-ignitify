// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
)

// A recurring schedule starts a run of its task at each slot of a cron
// expression. It is addressed by its `sch_` id.

// CreateSchedule creates a recurring schedule. It is created active.
func (c *Client) CreateSchedule(ctx context.Context, body ScheduleCreate) (Schedule, error) {
	var out Schedule
	err := c.do(ctx, request{method: http.MethodPost, path: pathSchedules, body: body, out: &out})
	return out, err
}

// GetSchedule reads a live recurring schedule.
func (c *Client) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	var out Schedule
	path, err := fillPath(pathScheduleByID, id)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodGet, path: path, out: &out, gone: goneSchedule})
	return out, err
}

// PatchSchedule edits a live recurring schedule and answers it as it is now.
func (c *Client) PatchSchedule(ctx context.Context, id string, edit ScheduleEdit) (Schedule, error) {
	var out Schedule
	path, err := fillPath(pathScheduleByID, id)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodPatch, path: path, body: edit, out: &out, gone: goneSchedule})
	return out, err
}

// DeleteSchedule deletes a recurring schedule; it fires no more slots.
func (c *Client) DeleteSchedule(ctx context.Context, id string) error {
	path, err := fillPath(pathScheduleByID, id)
	if err != nil {
		return err
	}
	return c.do(ctx, request{method: http.MethodDelete, path: path, gone: goneSchedule})
}
