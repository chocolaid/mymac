// pages/api/devices/heartbeat.js
// POST – agents call this every 60 s so the bot can show online/offline status
import { kv } from '@vercel/kv';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN;

export default async function handler(req, res) {
  if (req.method !== 'POST') return res.status(405).end();

  const token = req.headers['x-admin-token'];
  if (!token || token !== ADMIN_TOKEN) return res.status(401).json({ error: 'unauthorized' });

  const { deviceId } = req.body ?? {};
  if (!deviceId) return res.status(400).json({ error: 'deviceId required' });

  const existing = await kv.get(`device:${deviceId}`);
  if (!existing) return res.status(404).json({ error: 'unknown device' });

  await kv.set(`device:${deviceId}`, {
    ...existing,
    lastSeen: new Date().toISOString(),
    online: true,
  });

  // Also return current config version so agent knows to re-fetch if version changed
  const cfg = (await kv.get('config')) ?? {};
  return res.status(200).json({ ok: true, configVersion: cfg.version ?? 1 });
}
