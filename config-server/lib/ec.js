// lib/ec.js – Edge Config REST API helpers (no external packages)
// Reads:  https://edge-config.vercel.com/{EC_ID}/item/{key}?token={EC_READ_TOKEN}
// Writes: PATCH https://api.vercel.com/v1/edge-config/{EC_ID}/items

const EC_READ_BASE = `https://edge-config.vercel.com/${process.env.EC_ID}`;
const VERCEL_API   = `https://api.vercel.com/v1/edge-config/${process.env.EC_ID}`;

function apiRequest(url, method = 'GET', body) {
  return new Promise((resolve, reject) => {
    const parsed = new URL(url);
    const https  = require('https');
    const data   = body ? JSON.stringify(body) : undefined;

    const opts = {
      hostname: parsed.hostname,
      path: parsed.pathname + parsed.search,
      method,
      headers: {
        'Authorization': `Bearer ${process.env.EC_WRITE_TOKEN}`,
        ...(data ? { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data) } : {}),
      },
    };

    const req = https.request(opts, (res) => {
      let buf = '';
      res.on('data', (chunk) => { buf += chunk; });
      res.on('end', () => {
        try { resolve({ status: res.statusCode, body: JSON.parse(buf) }); }
        catch  { resolve({ status: res.statusCode, body: buf }); }
      });
    });
    req.on('error', reject);
    if (data) req.write(data);
    req.end();
  });
}

function readRequest(url) {
  return new Promise((resolve, reject) => {
    const parsed = new URL(url);
    const https  = require('https');
    const opts   = { hostname: parsed.hostname, path: parsed.pathname + parsed.search, method: 'GET', headers: {} };
    const req = https.request(opts, (res) => {
      let buf = '';
      res.on('data', (d) => { buf += d; });
      res.on('end', () => {
        try { resolve({ status: res.statusCode, body: JSON.parse(buf) }); }
        catch  { resolve({ status: res.statusCode, body: buf }); }
      });
    });
    req.on('error', reject);
    req.end();
  });
}

// Read a single key from Edge Config
async function ecGet(key) {
  const url  = `${EC_READ_BASE}/item/${key}?token=${process.env.EC_READ_TOKEN}`;
  const resp = await readRequest(url);
  if (resp.status === 404) return null;
  if (resp.status !== 200) throw new Error(`EC read error ${resp.status} for key ${key}`);
  return resp.body; // already parsed
}

// Read all items from Edge Config → returns { key: value, ... }
async function ecGetAll() {
  const url  = `${EC_READ_BASE}/items?token=${process.env.EC_READ_TOKEN}`;
  const resp = await readRequest(url);
  if (resp.status !== 200) throw new Error(`EC read-all error ${resp.status}`);
  // Edge Config returns an array of { key, value } objects
  const items = Array.isArray(resp.body) ? resp.body : [];
  return Object.fromEntries(items.map((i) => [i.key, i.value]));
}

// Upsert one or more keys: ecSet({ key1: val1, key2: val2 })
async function ecSet(kvPairs) {
  const teamId = process.env.TEAM_ID;
  const url    = `${VERCEL_API}/items?teamId=${teamId}`;
  const items  = Object.entries(kvPairs).map(([key, value]) => ({ operation: 'upsert', key, value }));
  const resp   = await apiRequest(url, 'PATCH', { items });
  if (resp.status !== 200 && resp.status !== 201) {
    throw new Error(`EC write error ${resp.status}: ${JSON.stringify(resp.body)}`);
  }
  return resp.body;
}

// Delete a key: ecDel('mykey')
async function ecDel(key) {
  const teamId = process.env.TEAM_ID;
  const url    = `${VERCEL_API}/items?teamId=${teamId}`;
  const resp   = await apiRequest(url, 'PATCH', { items: [{ operation: 'delete', key }] });
  if (resp.status !== 200 && resp.status !== 201) {
    throw new Error(`EC delete error ${resp.status}: ${JSON.stringify(resp.body)}`);
  }
}

module.exports = { ecGet, ecGetAll, ecSet, ecDel };
