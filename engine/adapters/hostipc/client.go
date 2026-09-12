// SPDX-License-Identifier: Apache-2.0

// Package hostipc implements the engine side of the same-host Unix-socket
// transport to the policy-free Director for Paseo connector.
package hostipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/mcuadros/director-engine/ports/host"
)

const maximumHostBytes = 1 << 20

type Client struct {
	http *http.Client
}

var _ host.Port = (*Client)(nil)

func New(socketPath string) (*Client, error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return nil, errors.New("Paseo host socket path is invalid")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DisableKeepAlives: false, MaxIdleConns: 4,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		}}
	return &Client{http: &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func decodeBounded(response *http.Response, destination any) error {
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" {
		return errors.New("Paseo host transport refused the request")
	}
	definition, err := host.EmbeddedDefinition()
	if err != nil {
		return err
	}
	hash, err := host.SchemaSHA256()
	if err != nil || response.Header.Get("X-Director-Contract-Version") != definition.ContractVersion ||
		response.Header.Get("X-Director-Contract-Hash") != hash {
		return errors.New("Paseo host transport contract mismatch")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumHostBytes+1))
	if err != nil || len(body) == 0 || len(body) > maximumHostBytes {
		return errors.New("Paseo host transport response is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("Paseo host transport response is invalid")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("Paseo host transport response is invalid")
	}
	return nil
}

func (client *Client) request(ctx context.Context, path string, value any, destination any) error {
	var body io.Reader
	method := http.MethodGet
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil || len(encoded) > maximumHostBytes {
			return errors.New("Paseo host command is invalid")
		}
		body, method = bytes.NewReader(encoded), http.MethodPost
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://director-host"+path, body)
	if err != nil {
		return errors.New("Paseo host request is invalid")
	}
	definition, err := host.EmbeddedDefinition()
	if err != nil {
		return err
	}
	hash, err := host.SchemaSHA256()
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Director-Contract-Version", definition.ContractVersion)
	request.Header.Set("X-Director-Contract-Hash", hash)
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return errors.New("Paseo host transport is unavailable")
	}
	return decodeBounded(response, destination)
}

func (client *Client) Describe(ctx context.Context) (host.Descriptor, error) {
	var result host.Descriptor
	err := client.request(ctx, "/v1/host/describe", nil, &result)
	return result, err
}

func (client *Client) Invoke(ctx context.Context, command host.Command) (host.Observation, error) {
	var result host.Observation
	err := client.request(ctx, "/v1/host/invoke", command, &result)
	return result, err
}
