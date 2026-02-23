// pages/api/devices/index.js
// GET  – bot lists all registered devices
// POST – agent registers itself on boot
import { ecGet, ecSet } from '../../../lib/ec';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN;

export default async function handler(req, res) {
  const token = req.headers['x-admin-token'];
  if (!token || token !== ADMIN_TOKEN) return res.status(401).json({ error: 'unauthorized' });

  if (req.method === 'GET') {
    const store = (await ecGet('devices')) ?? {};
    const devices = Object.values(store).sort((a, b) => new Date(b.lastSeen) - new Date(a.lastSeen));
    return res.status(200).json({ devices });
  }

  if (req.method === 'POST') {
    const { deviceId, hostname, arch, agentVersion } = req.body ?? {};
    if (!deviceId || !hostname) return res.status(400).json({ error: 'deviceId and hostname required' });

    const store    = (await ecGet('devices')) ?? {};
    const existing = store[deviceId] ?? {};
    store[deviceId] = {
      ...existing,
      deviceId, hostname,
      arch:         arch         ?? existing.arch         ?? 'unknown',
      agentVersion: agentVersion ?? existing.agentVersion ?? '0',
      firstSeen:    existing.firstSeen ?? new Date().toISOString(),
      lastSeen:     new Date().toISOString(),
    };
    await ecSet({ devices: store });
    return res.status(200).json({ ok: true, device: store[deviceId] });
  }

  return res.status(405).json({ error: 'method not allowed' });
}
