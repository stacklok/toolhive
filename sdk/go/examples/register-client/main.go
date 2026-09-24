// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/stacklok/toolhive/sdk/go/client"
)

func main() {
	baseURL := os.Getenv("TOOLHIVE_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}

	// Configure this caller-owned client with the deployment's auth transport,
	// mTLS certificates, or proxy policy as appropriate.
	httpClient := &http.Client{Timeout: 30 * time.Second}
	api, err := client.NewClient(baseURL, client.WithClient(httpClient))
	if err != nil {
		panic(err)
	}

	response, err := api.RegisterClient(context.Background(), &client.CreateClientRequest{
		Name:   client.ClientClientAppCursor,
		Groups: []string{"default"},
	})
	if err != nil {
		panic(err)
	}

	switch response := response.(type) {
	case *client.CreateClientResponse:
		fmt.Printf("registered client in groups: %v\n", response.Groups)
	case *client.RegisterClientBadRequestApplicationJSON:
		fmt.Fprintf(os.Stderr, "server rejected registration: %s\n", *response)
	default:
		fmt.Fprintf(os.Stderr, "unexpected API response: %T\n", response)
	}
}
