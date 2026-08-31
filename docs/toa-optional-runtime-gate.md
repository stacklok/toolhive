# Optional TOA verify for runtime health / promote

ToolHive runs MCP servers in isolated containers, applies identity and access
policy, and verifies software provenance (Sigstore / attestations) for registry
entries. That answers secure run and supply-chain trust. It does not prove that
a tool recently delivered a real result under an outside probe.

[TOA](https://github.com/Carmel-Labs-Inc/toa) (`toa/0.1`) is an Apache-2.0 signed
JSON evidence format for MCP tool delivery (reach, invoke, functional, shape,
and related layers). It is not a wire protocol. It is not meant to run on every
live `tools/call`.

## Suggested fit

Optional, off by default. Before promoting a workload to a production group, or
as a CI check after `thv` run / health, require a recent attestation and verify
it offline with a pinned emitter public key.

- Any party can emit if they sign the schema.
- AgentStatus is one optional emitter.
- No AgentStatus account is required to verify.

```yaml
      # After your ToolHive deploy / health checks.
      - name: Verify tool delivery attestation
        if: hashFiles('toa.json') != ''
        run: |
          pip install "git+https://github.com/Carmel-Labs-Inc/toa.git@345f24607919b5bdf143719b9ea062543cdfe88e#subdirectory=python"
          toa-verify toa.json --require-layer functional=pass
```

Copy-paste workflow: [`examples/toa-after-runtime.yml`](../examples/toa-after-runtime.yml).

## Out of scope

- Replacing Sigstore provenance, Cedar authz, or the runtime hot path
- Signing every production `tools/call`
- Changing ToolHive runtime code

Related: [Registry inclusion heuristics](registry/heuristics.md) (supply-chain)
vs TOA (delivery evidence).
