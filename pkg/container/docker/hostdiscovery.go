// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"
	"time"

	mobyclient "github.com/moby/moby/client"
)

var hostGatewayNames = [...]string{dockerGatewayHostname, dockerAltGatewayHostname, podmanGatewayHostname}

// CopyFromContainer uses the runtime's archive endpoint, not the client's host
// filesystem. Podman only exposes its generated hosts file after container start.
type hostDiscoveryAPI interface {
	CopyFromContainer(context.Context, string, mobyclient.CopyFromContainerOptions) (mobyclient.CopyFromContainerResult, error)
}

// discoverHostAliases reads the running DNS sidecar's runtime-generated hosts.
// ToolHive's DNS and proxy sidecars use bridge networking without hosts overrides;
// all bridge gateways separately cover Podman's rootful gateway selection.
func (c *Client) discoverHostAliases(ctx context.Context, dnsContainerID string) (_ []netip.Addr, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	archive, err := c.hostAPI.CopyFromContainer(ctx, dnsContainerID, mobyclient.CopyFromContainerOptions{SourcePath: "/etc/hosts"})
	if err != nil {
		return nil, fmt.Errorf("read running DNS hosts: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, archive.Content.Close()) }()
	return parseHostAliasArchive(archive.Content)
}

// Bound the entire archive as well as the member, so extended headers, padding,
// and unexpected files cannot cause unbounded reads. Never extract to disk.
func parseHostAliasArchive(r io.Reader) ([]netip.Addr, error) {
	const maxHostsSize = 1 << 20
	const maxArchiveSize = 2 << 20
	data, err := io.ReadAll(io.LimitReader(r, maxArchiveSize+1))
	if err != nil {
		return nil, fmt.Errorf("read hosts archive: %w", err)
	}
	if len(data) > maxArchiveSize {
		return nil, errors.New("hosts archive exceeds size limit")
	}
	tr := tar.NewReader(bytes.NewReader(data))
	header, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("read hosts archive header: %w", err)
	}
	if header.Name != "hosts" || header.Typeflag != tar.TypeReg || header.Size > maxHostsSize {
		return nil, errors.New("hosts archive must contain one bounded regular hosts file")
	}
	addresses, err := parseHostAliases(tr)
	if err != nil {
		return nil, err
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		return nil, errors.New("hosts archive contains extra or malformed entries")
	}
	return addresses, nil
}

func parseHostAliases(r io.Reader) ([]netip.Addr, error) {
	var foundPodmanAlias bool
	var addresses []netip.Addr
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), (1<<20)+1)
	for scanner.Scan() {
		line, _, _ := strings.Cut(scanner.Text(), "#")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		addr, err := netip.ParseAddr(fields[0])
		if err != nil || addr.Zone() != "" || len(fields) < 2 {
			return nil, errors.New("malformed hosts entry")
		}
		for _, name := range fields[1:] {
			name = strings.TrimSuffix(strings.ToLower(name), ".")
			if slices.Contains(hostGatewayNames[:], name) {
				foundPodmanAlias = foundPodmanAlias || name == podmanGatewayHostname
				addresses = append(addresses, addr.Unmap())
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read hosts: %w", err)
	}
	// Only Podman's own alias is required: older Podman releases do not write
	// host.docker.internal, and an absent alias cannot resolve to the host.
	if !foundPodmanAlias {
		return nil, errors.New("hosts missing " + podmanGatewayHostname + "; check Podman hosts configuration")
	}
	slices.SortFunc(addresses, netip.Addr.Compare)
	return slices.Compact(addresses), nil
}
