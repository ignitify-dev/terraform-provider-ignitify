// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"time"
)

const (
	// The deploy form: the task's server id and the source archive.
	formFieldTaskID    = "task_id"
	formFieldBundle    = "bundle"
	bundleFileName     = "bundle.tar"
	contentTypeTar     = "application/x-tar"
	headerDisposition  = "Content-Disposition"
	uploadTryTimeout   = 5 * time.Minute
	queryTaskID        = "task_id"
	queryLimit         = "limit"
	queryAfterID       = "after_id"
	deploymentPageSize = 100
)

// CreateDeployment uploads a source archive for a task (by its server id). The
// server reads the task config inside the archive. The answer is 202 BUILDING
// for a new build, or 200 with Reused for content that is already READY or
// building; poll GetDeployment until the status leaves BUILDING.
func (c *Client) CreateDeployment(ctx context.Context, taskID string, archive []byte) (DeploymentCreated, error) {
	var out DeploymentCreated
	form, contentType, err := deployForm(taskID, archive)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodPost, path: pathDeployments, raw: form, contentType: contentType, timeout: uploadTryTimeout, out: &out})
	return out, err
}

// deployForm is the multipart body of a deploy. Pure.
func deployForm(taskID string, archive []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField(formFieldTaskID, taskID); err != nil {
		return nil, "", err
	}
	part := textproto.MIMEHeader{}
	part.Set(headerDisposition, fmt.Sprintf(`form-data; name=%q; filename=%q`, formFieldBundle, bundleFileName))
	part.Set(headerContentType, contentTypeTar)
	fw, err := w.CreatePart(part)
	if err != nil {
		return nil, "", err
	}
	if _, err := fw.Write(archive); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// GetDeployment reads a live deployment.
func (c *Client) GetDeployment(ctx context.Context, id string) (Deployment, error) {
	var out Deployment
	path, err := fillPath(pathDeploymentByID, id)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodGet, path: path, out: &out, gone: goneDeployment})
	return out, err
}

// DeleteDeployment deletes a deployment with its runs; the server ends its VMs.
func (c *Client) DeleteDeployment(ctx context.Context, id string) error {
	path, err := fillPath(pathDeploymentByID, id)
	if err != nil {
		return err
	}
	return c.do(ctx, request{method: http.MethodDelete, path: path, gone: goneDeployment})
}

// ListTaskDeployments lists every live deployment of a task, oldest first. An
// unknown task lists none.
func (c *Client) ListTaskDeployments(ctx context.Context, taskSlug string) ([]Deployment, error) {
	var all []Deployment
	afterID := ""
	for {
		query := url.Values{queryTaskID: {taskSlug}, queryLimit: {strconv.Itoa(deploymentPageSize)}}
		if afterID != "" {
			query.Set(queryAfterID, afterID)
		}
		var page deploymentList
		if err := c.do(ctx, request{method: http.MethodGet, path: pathDeployments + "?" + query.Encode(), out: &page}); err != nil {
			return nil, err
		}
		all = append(all, page.Deployments...)
		if len(page.Deployments) < deploymentPageSize {
			return all, nil
		}
		afterID = page.Deployments[len(page.Deployments)-1].ID
	}
}

// SetNotify sets the URL a deployment calls when a run ends, signed with secret.
func (c *Client) SetNotify(ctx context.Context, id, notifyURL, secret string) error {
	return c.putNotify(ctx, id, notifyRequest{URL: &notifyURL, Secret: secret})
}

// ClearNotify removes a deployment's notify target.
func (c *Client) ClearNotify(ctx context.Context, id string) error {
	return c.putNotify(ctx, id, notifyRequest{})
}

func (c *Client) putNotify(ctx context.Context, id string, body notifyRequest) error {
	path, err := fillPath(pathDeploymentNotify, id)
	if err != nil {
		return err
	}
	return c.do(ctx, request{method: http.MethodPut, path: path, body: body})
}
