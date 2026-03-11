// pages/api/devices/heartbeat.js
// POST – agent calls every 60 s to update lastSeen and get config version
import { ecGet, ecSet } from '../../../lib/db';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN;

export default async function handler(req, res) {
  if (req.method !== 'POST') return res.status(405).end();

  const token = req.headers['x-admin-token'];
  if (!token || token !== ADMIN_TOKEN) return res.status(401).json({ error: 'unauthorized' });

  const { deviceId } = req.body ?? {};
  if (!deviceId) return res.status(400).json({ error: 'deviceId required' });

  const { hostname, arch, agentVersion } = req.body ?? {};
  const store = (await ecGet('devices')) ?? {};

  // Auto-register if unknown — handles agents that started when writes were broken
  if (!store[deviceId]) {
    const now = new Date().toISOString();
    store[deviceId] = {
      deviceId,
      hostname:     hostname     ?? deviceId,
      arch:         arch         ?? 'unknown',
      agentVersion: agentVersion ?? '0',
      firstSeen: now, lastSeen: now,
    };
  }

  store[deviceId] = { ...store[deviceId], lastSeen: new Date().toISOString() };
  await ecSet({ devices: store });

  const cfg = (await ecGet('config')) ?? {};
  return res.status(200).json({ ok: true, configVersion: cfg.version ?? 1 });
}
