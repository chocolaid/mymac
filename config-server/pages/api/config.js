// pages/api/config.js
// GET  – agents fetch current server config
// POST – bot/admin updates the server config
import { kv } from '@vercel/kv';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN;

function unauthorized(res) {
  return res.status(401).json({ error: 'unauthorized' });
}

export default async function handler(req, res) {
  const token = req.headers['x-admin-token'];
  if (!token || token !== ADMIN_TOKEN) return unauthorized(res);

  if (req.method === 'GET') {
    // Return current config. Seed defaults if first run.
    let cfg = await kv.get('config');
    if (!cfg) {
      cfg = {
        serverUrl: '',
        agentSecret: '',
        updatedAt: new Date().toISOString(),
        version: 1,
      };
      await kv.set('config', cfg);
    }
    return res.status(200).json(cfg);
  }

  if (req.method === 'POST') {
    const { serverUrl, agentSecret } = req.body ?? {};
    const current = (await kv.get('config')) ?? { version: 0 };

    const updated = {
      ...current,
      ...(serverUrl   !== undefined && { serverUrl }),
      ...(agentSecret !== undefined && { agentSecret }),
      updatedAt: new Date().toISOString(),
      version: (current.version ?? 0) + 1,
    };

    await kv.set('config', updated);
    // Signal all agents to re-fetch immediately
    await kv.set('config:changed', Date.now());
    return res.status(200).json({ ok: true, config: updated });
  }

  return res.status(405).json({ error: 'method not allowed' });
}
