<p float="left">
  <picture>
    <img src="docs/images/toolhive-icon-1024.png" alt="ToolHive logo" height="100" align="middle" />
  </picture>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/toolhive-wordmark-white.png">
    <img src="docs/images/toolhive-wordmark-black.png" alt="ToolHive wordmark" width="500" align="middle" hspace="20" />
  </picture>
  <picture>
    <img src="docs/images/toolhive-mascot.png" alt="ToolHive mascot" width="125" align="middle"/>
  </picture>
</p>

[![Release][release-img]][release] [![Build status][ci-img]][ci]
[![Coverage Status][coveralls-img]][coveralls]
[![License: Apache 2.0][license-img]][license]
[![Star on GitHub][stars-img]][stars] [![Discord][discord-img]][discord]

# ToolHive - simplify and secure MCP servers

**Run any Model Context Protocol (MCP) server: securely, instantly, anywhere.**

ToolHive includes everything you need to use MCP servers in production. Rather than build or combine components yourself, use ToolHive's Registry Server, Runtime, Gateway, and Portal to get up and running quickly and safely.

ToolHive keeps you in control of your MCP estate. Integrate with popular clients in seconds and deploy pre-vetted MCP servers in locked-down containers with a single click or command. ToolHive is available as a desktop app, web app, CLI, and Kubernetes operator.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/toolhive-diagram-dark.svg">
  <img src="docs/images/toolhive-diagram-light.svg" alt="ToolHive diagram" width="800" style="padding: 20px 0" />
</picture>

---

<table>
<tr>
<td width="50%">

## Why ToolHive?

- **Instant deployment:** Start any MCP server with one click or command, using Docker or Kubernetes.
- **Secure by default:** Every server runs in an isolated container with only the permissions it needs. Secrets are managed securely, never in plaintext.
- **Works everywhere:** Use the UI and CLI for local development, or the Kubernetes Operator for production and scale.
- **Seamless integration:** ToolHive auto-configures popular clients like GitHub Copilot, Cursor, and more.

<br>
</td>
<td width="50%" align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/toolhive-sources-dark.svg">
    <img src="docs/images/toolhive-sources-light.svg" alt="ToolHive sources diagram" width="400px" />
  </picture>
</td>
</tr>
</table>

## Quick links

- 📥 [Downloads](https://toolhive.dev/download/)
- 📚 [Documentation](https://docs.stacklok.com/toolhive/)
- 🚀 Quickstart guides:
  - [Desktop app](https://docs.stacklok.com/toolhive/tutorials/quickstart-ui)
  - [CLI](https://docs.stacklok.com/toolhive/tutorials/quickstart-cli)
  - [Kubernetes Operator](https://docs.stacklok.com/toolhive/tutorials/quickstart-k8s)
- 💬 [Discord](https://discord.gg/stacklok)
- 🤝 [Contributing](#contributing)

---

## Core capabilities

**ToolHive architecture: Gateway, Registry Server, Runtime, and Portal**

ToolHive is built on a [modular architecture](./docs/arch/README.md) to streamline secure MCP server management and integration. Here's how the main components work.

### 🔌 Gateway

Define dedicated endpoints from which your teams can securely and efficiently access tools.

- Orchestrate multiple tools into a virtual MCP with a deterministic workflow engine
- Define access policies and network endpoints
- Centralize control of security policy, authentication, authorization, auditing, etc.
- Integrate with your IdP for SSO (OIDC/OAuth compatible)
- Customize and filter tools and descriptions to improve performance and reduce token usage
- Connect with local clients like Claude Desktop, Cursor, and VS Code

### 📦 [Registry Server](https://github.com/stacklok/toolhive-registry-server)

Curate a catalog of trusted servers your teams can quickly discover and deploy.

- Integrate with the official MCP registry
- Add custom MCP servers
- Group servers based on role or use case
- Manage your registry with an API-driven interface (or embed in existing workflows for seamless integration and governance)
- Verify provenance and sign servers with built-in security controls
- Preset configurations and permissions for a frictionless user experience

### ⚙️ Runtime

Deploy, run, and manage MCP servers locally or in a Kubernetes cluster with security guardrails.

- Deploy MCP servers in the cloud via Kubernetes for enterprise scalability
- Run MCP servers locally via Docker or Podman
- Proxy remote MCP servers securely for unified management
- Kubernetes Operator for fleet and resource management
- Leverage OpenTelemetry and Prometheus for monitoring and audit logging

### 💻 Portal

Simplify MCP adoption for developers and knowledge workers across your enterprise

- Cross-platform [desktop app](https://github.com/stacklok/toolhive-studio) and browser-based [cloud UI](https://github.com/stacklok/toolhive-cloud-ui)
- Make it easy for admins to curate MCP servers and tools
- Automate server discovery
- Install MCP servers with a single click
- Compatible with hundreds of AI clients

### How it works together

1. **Admins** curate and organize MCP servers in the **Registry**, configuring access and policies.
2. **Users** discover and request MCP servers from the **Portal**, and ToolHive orchestrates installation and access.
3. **Runtime** securely deploys and manages MCP servers across local and cloud environments, integrating seamlessly with existing SDLC workflows, exporting analytics, and enforcing fine-grained access control.
4. **Gateway** handles all inbound traffic, secures context and credentials, optimizes tool selection, and applies organizational policies.

---

## Flexible deployment

### Desktop experience

Individual developers can get started in minutes with the desktop UI or CLI, then apply the same concepts in enterprise environments.

**Key features:**

- Run any MCP server from a container image, or build one dynamically from common package managers
- Manage encrypted secrets and control network isolation with simple, local tooling
- Test and validate MCP servers using built-in tools like the official MCP Inspector
- Optimize token usage and tool execution with the MCP Optimizer

**Get started with the UI:** [Quickstart](https://docs.stacklok.com/toolhive/tutorials/quickstart-ui), [How-to guides](https://docs.stacklok.com/toolhive/guides-ui)  
**Get started with the CLI:** [Quickstart](https://docs.stacklok.com/toolhive/tutorials/quickstart-cli), [How-to guides](https://docs.stacklok.com/toolhive/guides-cli), [Command reference](https://docs.stacklok.com/toolhive/reference/cli/thv)

[**MCP guides**](https://docs.stacklok.com/toolhive/guides-mcp): learn how to run common MCP servers with ToolHive

### Kubernetes Operator

Teams and organizations manage MCP servers and registries centrally using familiar Kubernetes workflows.

**Key features:**

- Custom Resource Definitions for MCP servers, registries, and other ToolHive components
- Secure execution with container-based isolation and multi-namespace support
- Automated service creation and discovery, with ingress integration for secure access
- Enterprise-grade security and observability: OIDC/OAuth SSO, secure token exchange, audit logging, OpenTelemetry, and Prometheus metrics
- Hybrid registry server: curate from upstream registries, dynamically register local MCP servers, or proxy trusted remote services

**Get started:** [Quickstart](https://docs.stacklok.com/toolhive/tutorials/quickstart-k8s), [How-to guides](https://docs.stacklok.com/toolhive/guides-k8s), [CRD reference](https://docs.stacklok.com/toolhive/reference/crd-spec), [Example manifests](./examples/operator/)

### Hybrid

ToolHive's complete solution for teams and enterprises supports MCP servers across all environments: on developer machines, inside your Kubernetes clusters, or hosted externally by trusted SaaS providers.

End users access approved MCP servers through a secure, browser-based cloud UI. Developers can also connect using the ToolHive CLI or desktop UI for advanced integration and testing workflows.

Enterprise teams can also leverage ToolHive to integrate MCP servers into custom internal tools, agentic workflows, or chat-based interfaces, using the same runtime and access controls.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/toolhive-platform-dark.svg">
  <img src="docs/images/toolhive-platform-light.svg" alt="ToolHive platform diagram" width="800" style="padding: 20px 0" />
</picture>

---

## Contributing

We welcome contributions and feedback from the community!

- 🐛 [Report issues](https://github.com/stacklok/toolhive/issues)
- 💬 [Join our Discord](https://discord.gg/stacklok)

If you have ideas, suggestions, or want to get involved, check out our contributing guide or open an issue. Join us in making ToolHive even better!

Contribute to the CLI, API, and Kubernetes Operator (this repo):

- 🤝 [Contributing guide](./CONTRIBUTING.md)
- 📖 [Developer guides](./docs/README.md)
- 📐 [Architecture documentation](./docs/arch/README.md)

Contribute to the UI, registry, and docs:

- 💻 [Desktop UI repository](https://github.com/stacklok/toolhive-studio)
- ☁️ [Cloud UI repository](https://github.com/stacklok/toolhive-cloud-ui)
- 📦 [ToolHive registry server repository](https://github.com/stacklok/toolhive-registry-server)
- 🛠️ [ToolHive's built-in registry](https://github.com/stacklok/toolhive-registry)
- 📚 [Documentation repository](https://github.com/stacklok/docs-website)

---

## License

This project is licensed under the [Apache 2.0 License](./LICENSE).

<!-- Badge links -->
<!-- prettier-ignore-start -->
[release-img]: https://img.shields.io/github/v/release/stacklok/toolhive?style=flat&label=Latest%20version
[release]: https://github.com/stacklok/toolhive/releases/latest
[ci-img]: https://img.shields.io/github/actions/workflow/status/stacklok/toolhive/run-on-main.yml?style=flat&logo=github&label=Build
[ci]: https://github.com/stacklok/toolhive/actions/workflows/run-on-main.yml
[coveralls-img]: https://coveralls.io/repos/github/stacklok/toolhive/badge.svg?branch=main
[coveralls]: https://coveralls.io/github/stacklok/toolhive?branch=main
[license-img]: https://img.shields.io/badge/License-Apache2.0-blue.svg?style=flat
[license]: https://opensource.org/licenses/Apache-2.0
[stars-img]: https://img.shields.io/github/stars/stacklok/toolhive.svg?style=flat&logo=github&label=Stars
[stars]: https://github.com/stacklok/toolhive
[discord-img]: https://img.shields.io/discord/1184987096302239844?style=flat&logo=discord&logoColor=white&label=Discord
[discord]: https://discord.gg/stacklok
<!-- prettier-ignore-end -->

<!-- markdownlint-disable-file first-line-heading no-inline-html no-emphasis-as-heading -->
