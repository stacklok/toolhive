#!/usr/bin/env node
// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Extracts each CRD's openAPIV3Schema from deploy/charts/operator-crds/files/crds.
// For each CRD this writes:
//   <plural>.schema.json  - JSON Schema (apiVersion/kind/metadata stripped)
//   <plural>.example.yaml - Minimal YAML skeleton covering required fields
// Plus a shared index.json with metadata and an inter-CRD reference graph.
//
// Usage:
//   node hack/extract-crd-schemas.mjs --src <crds-dir> --out <out-dir>
//
// Intended to run during release CI to produce a pre-generated schema
// bundle consumable by docs sites and other downstream tooling.

import fs from 'node:fs';
import path from 'node:path';
import yaml from 'yaml';

function parseArgs(argv) {
  const args = { src: null, out: null };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--src') {
      args.src = argv[i + 1];
      i++;
    } else if (argv[i] === '--out') {
      args.out = argv[i + 1];
      i++;
    } else if (argv[i] === '--help' || argv[i] === '-h') {
      console.log(
        'Usage: node extract-crd-schemas.mjs --src <crds-dir> --out <out-dir>'
      );
      process.exit(0);
    }
  }
  return args;
}

const args = parseArgs(process.argv.slice(2));
if (!args.src || !args.out) {
  console.error(
    'Both --src and --out are required. See --help for usage.'
  );
  process.exit(1);
}

if (!fs.existsSync(args.src)) {
  console.error(`CRD source directory not found: ${args.src}`);
  process.exit(1);
}

fs.mkdirSync(args.out, { recursive: true });

// Placeholder values for leaf types with no default/enum.
function placeholder(schema) {
  const t = schema.type;
  if (schema.default !== undefined) return schema.default;
  if (Array.isArray(schema.enum) && schema.enum.length) return schema.enum[0];
  if (t === 'string') return '<string>';
  if (t === 'integer' || t === 'number') return 0;
  if (t === 'boolean') return false;
  if (t === 'array') return [];
  if (t === 'object') return {};
  return '<value>';
}

function buildRequiredExample(schema) {
  if (schema.type !== 'object' || !schema.properties)
    return placeholder(schema);
  const required = Array.isArray(schema.required) ? schema.required : [];
  const out = {};
  for (const key of required) {
    const child = schema.properties[key];
    if (!child) continue;
    if (child.type === 'object' && child.properties) {
      out[key] = buildRequiredExample(child);
    } else if (child.type === 'array') {
      out[key] = [];
    } else {
      out[key] = placeholder(child);
    }
  }
  return out;
}

function buildYamlSkeleton({ group, version, kind, scope, schema }) {
  const example = {
    apiVersion: `${group}/${version}`,
    kind,
    metadata: {
      name: `my-${kind.toLowerCase()}`,
      ...(scope === 'Namespaced' ? { namespace: 'default' } : {}),
    },
  };
  if (schema.properties?.spec) {
    example.spec = buildRequiredExample(schema.properties.spec);
    if (example.spec === undefined) example.spec = {};
  }
  return yaml.stringify(example, { indent: 2, lineWidth: 0 });
}

// Walk a schema and collect outgoing references to other CRDs.
// A field is a reference when its name ends in "Ref" or "Refs" AND its
// description (or its nested `name` subfield's description) mentions a
// known CRD Kind. Returns [{ path, targetKind }] — multiple paths per
// target are preserved so consumers can list every field that points
// at a given Kind.
function findReferences(schema, knownKinds, ownKind) {
  const results = [];
  const seen = new Set();

  function check(name, node) {
    if (!/Refs?$/.test(name)) return null;
    const textParts = [];
    if (node?.description) textParts.push(node.description);
    if (node?.items?.description) textParts.push(node.items.description);
    if (node?.properties?.name?.description) {
      textParts.push(node.properties.name.description);
    }
    if (node?.items?.properties?.name?.description) {
      textParts.push(node.items.properties.name.description);
    }
    const text = textParts.join(' ');
    for (const kind of knownKinds) {
      if (kind === ownKind) continue;
      if (new RegExp(`\\b${kind}\\b`).test(text)) return kind;
    }
    return null;
  }

  function walk(node, jsonPtr) {
    if (!node || typeof node !== 'object') return;
    if (node.properties) {
      for (const [key, child] of Object.entries(node.properties)) {
        const target = check(key, child);
        if (target) {
          const dedupeKey = `${target}@${jsonPtr}.${key}`;
          if (!seen.has(dedupeKey)) {
            seen.add(dedupeKey);
            results.push({ path: `${jsonPtr}.${key}`, targetKind: target });
          }
        }
        walk(child, `${jsonPtr}.${key}`);
      }
    }
    if (node.items) walk(node.items, `${jsonPtr}[]`);
  }

  walk(schema, '');
  return results;
}

// Pass 1: parse all CRDs and collect metadata.
const files = fs.readdirSync(args.src).filter((f) => f.endsWith('.yaml'));
const crds = [];

for (const file of files) {
  const full = path.join(args.src, file);
  const doc = yaml.parse(fs.readFileSync(full, 'utf8'));

  if (doc?.kind !== 'CustomResourceDefinition') {
    console.warn(`Skipping ${file}: not a CRD`);
    continue;
  }

  const kind = doc.spec?.names?.kind;
  const plural = doc.spec?.names?.plural;
  const group = doc.spec?.group;
  const shortNames = doc.spec?.names?.shortNames || [];
  const scope = doc.spec?.scope;

  const versions = doc.spec?.versions || [];
  const served = versions.find((v) => v.storage) || versions[0];
  if (!served?.schema?.openAPIV3Schema) {
    console.warn(`Skipping ${file}: no openAPIV3Schema`);
    continue;
  }

  const schema = { ...served.schema.openAPIV3Schema };

  // Strip Kubernetes boilerplate; these fields are identical on every CRD.
  if (schema.properties) {
    const stripped = { ...schema.properties };
    for (const key of ['apiVersion', 'kind', 'metadata']) delete stripped[key];
    schema.properties = stripped;
  }

  crds.push({
    file,
    kind,
    plural,
    group,
    version: served.name,
    shortNames,
    scope,
    schema,
  });
}

const knownKinds = crds.map((c) => c.kind);
const outgoingByKind = new Map();
for (const crd of crds) {
  outgoingByKind.set(
    crd.kind,
    findReferences(crd.schema, knownKinds, crd.kind)
  );
}

// Build inverse: for each kind, which other kinds reference it.
const incomingByKind = new Map();
for (const kind of knownKinds) incomingByKind.set(kind, []);
for (const [src, refs] of outgoingByKind) {
  for (const ref of refs) {
    incomingByKind
      .get(ref.targetKind)
      .push({ sourceKind: src, path: ref.path });
  }
}

// Pass 2: write output files.
const index = [];
for (const crd of crds) {
  const { kind, plural, group, version, shortNames, scope, schema } = crd;

  const wrapped = {
    $schema: 'http://json-schema.org/draft-07/schema#',
    title: kind,
    description: schema.description || `${kind} custom resource`,
    'x-kubernetes-group': group,
    'x-kubernetes-kind': kind,
    'x-kubernetes-version': version,
    'x-kubernetes-plural': plural,
    'x-kubernetes-short-names': shortNames,
    'x-kubernetes-scope': scope,
    ...schema,
  };

  const schemaFile = path.join(args.out, `${plural}.schema.json`);
  fs.writeFileSync(schemaFile, JSON.stringify(wrapped, null, 2) + '\n');

  const exampleFile = path.join(args.out, `${plural}.example.yaml`);
  fs.writeFileSync(
    exampleFile,
    buildYamlSkeleton({ group, version, kind, scope, schema })
  );

  const outgoing = outgoingByKind.get(kind) || [];
  const incoming = incomingByKind.get(kind) || [];

  const refByTarget = new Map();
  for (const r of outgoing) {
    if (!refByTarget.has(r.targetKind)) refByTarget.set(r.targetKind, []);
    refByTarget.get(r.targetKind).push(r.path.replace(/^\./, ''));
  }
  const incomingByKindSrc = new Map();
  for (const r of incoming) {
    if (!incomingByKindSrc.has(r.sourceKind))
      incomingByKindSrc.set(r.sourceKind, []);
    incomingByKindSrc.get(r.sourceKind).push(r.path.replace(/^\./, ''));
  }

  index.push({
    kind,
    plural,
    group,
    version,
    shortNames,
    scope,
    description: schema.description || '',
    references: [...refByTarget.entries()]
      .map(([targetKind, paths]) => ({
        targetKind,
        paths: [...new Set(paths)].sort(),
      }))
      .sort((a, b) => a.targetKind.localeCompare(b.targetKind)),
    referencedBy: [...incomingByKindSrc.entries()]
      .map(([sourceKind, paths]) => ({
        sourceKind,
        paths: [...new Set(paths)].sort(),
      }))
      .sort((a, b) => a.sourceKind.localeCompare(b.sourceKind)),
  });
}

fs.writeFileSync(
  path.join(args.out, 'index.json'),
  JSON.stringify(index, null, 2) + '\n'
);
console.log(`Extracted ${index.length} CRD schema(s) to ${args.out}.`);
