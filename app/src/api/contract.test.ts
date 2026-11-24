/// <reference types="node" />
import * as fs from 'fs';
import * as path from 'path';
import * as yaml from 'js-yaml';
import { API_PREFIX } from '../config';

/**
 * Frontend side of the API contract: every route client.ts and socket.ts
 * call must be documented in api/openapi.yaml. The backend side (the router
 * coverage tests in cmd/api and cmd/worker) guarantees the spec matches what
 * the server actually serves, so this closes the loop — the app cannot call
 * a route that does not exist in the contract.
 */

const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete'];

interface OpenAPISpec {
  paths: Record<string, Record<string, unknown>>;
}

function documentedOperations(): Set<string> {
  const specPath = path.resolve(__dirname, '..', '..', '..', 'api', 'openapi.yaml');
  const spec = yaml.load(fs.readFileSync(specPath, 'utf8')) as OpenAPISpec;
  const ops = new Set<string>();
  for (const [route, item] of Object.entries(spec.paths)) {
    for (const method of Object.keys(item)) {
      if (HTTP_METHODS.includes(method)) ops.add(`${method.toUpperCase()} ${route}`);
    }
  }
  return ops;
}

// '/chat/threads/${this.threadID}/ws' -> '/chat/threads/{threadID}/ws';
// a '${...}' not preceded by '/' is a query suffix ('/sessions${query}') — dropped.
function normalizePathTemplate(raw: string): string {
  return raw
    .replace(/\/\$\{[^}]+\}/g, (m) => `/{${m.slice(3, -1).split('.').pop()}}`)
    .replace(/\$\{[^}]+\}.*$/g, '');
}

function callsIn(source: string, file: string): Array<[string, string]> {
  const calls: Array<[string, string]> = [];
  // request<T>('METHOD', '/path' | `/path`) — the client prefixes API_PREFIX at fetch time.
  const requestRe = /request(?:<[^>]*>)?\(\s*'([A-Z]+)'\s*,\s*([`'"])([^`'"]+)\2/g;
  for (const m of source.matchAll(requestRe)) {
    calls.push([m[1], `${API_PREFIX}${normalizePathTemplate(m[3]).split('?')[0]}`]);
  }
  // wsURL(`/path`, ticket) — the socket upgrade is a GET.
  if (file === 'socket.ts') {
    const wsRe = /wsURL\(\s*([`'"])([^`'"]+)\1/g;
    for (const m of source.matchAll(wsRe)) {
      calls.push(['GET', `${API_PREFIX}${normalizePathTemplate(m[2]).split('?')[0]}`]);
    }
  }
  return calls;
}

describe('API contract', () => {
  const ops = documentedOperations();

  it.each(['client.ts', 'socket.ts'])('%s only calls documented routes', (file) => {
    const source = fs.readFileSync(path.resolve(__dirname, file), 'utf8');
    const calls = callsIn(source, file);
    expect(calls.length).toBeGreaterThan(0);
    for (const [method, route] of calls) {
      expect(ops).toContain(`${method} ${route}`);
    }
  });
});
