// pages/api/config.js
// GET  – agents fetch current server config
// POST – bot updates the server config
import { ecGet, ecSet } from '../../lib/ec';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN;

export default async function handler(req, res) {
  const token = req.headers['x-admin-token'];
  if (!token || token !== ADMIN_TOKEN) return res.status(401).json({ error: 'unauthorized' });

  if (req.method === 'GET') {
    let cfg = await ecGet('config');
    if (!cfg) {
      cfg = { serverUrl: '', agentSecret: '', updatedAt: new Date().toISOString(), version: 1 };
      await ecSet({ config: cfg });
    }
    return res.status(200).json(cfg);
  }

  if (req.method === 'POST') {
    const { serverUrl, agentSecret } = req.body ?? {};
    const current = (await ecGet('config')) ?? { version: 0 };
    const updated = {
      ...current,
      ...(serverUrl   !== undefined && { serverUrl }),
      ...(agentSecret !== undefined && { agentSecret }),
      updatedAt: new Date().toISOString(),
      version: (current.version ?? 0) + 1,
    };
    await ecSet({ config: updated });
    return res.status(200).json({ ok: true, config: updated });
  }

  return res.status(405).json({ error: 'method not allowed' });
}
