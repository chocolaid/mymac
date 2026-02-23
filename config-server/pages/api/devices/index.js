// pages/api/devices/index.js
// GET  – list all registered devices (for the bot)
// POST – agent registers itself (called on every agent startup)
import { kv } from '@vercel/kv';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN;

export default async function handler(req, res) {
  const token = req.headers['x-admin-token'];
  if (!token || token !== ADMIN_TOKEN) {
    return res.status(401).json({ error: 'unauthorized' });
  }

  if (req.method === 'GET') {
    // Return all device records sorted by lastSeen desc
    const keys = await kv.keys('device:*');
    if (!keys.length) return res.status(200).json({ devices: [] });

    const devices = await Promise.all(keys.map((k) => kv.get(k)));
    devices.sort((a, b) => new Date(b.lastSeen) - new Date(a.lastSeen));
    return res.status(200).json({ devices });
  }

  if (req.method === 'POST') {
    // Agent registers: { deviceId, hostname, arch, os, agentVersion }
    const { deviceId, hostname, arch, agentVersion } = req.body ?? {};
    if (!deviceId || !hostname) {
      return res.status(400).json({ error: 'deviceId and hostname required' });
    }

    const existing = (await kv.get(`device:${deviceId}`)) ?? {};
    const record = {
      ...existing,
      deviceId,
      hostname,
      arch: arch ?? existing.arch ?? 'unknown',
      agentVersion: agentVersion ?? existing.agentVersion ?? '0',
      firstSeen: existing.firstSeen ?? new Date().toISOString(),
      lastSeen: new Date().toISOString(),
      online: true,
    };

    await kv.set(`device:${deviceId}`, record);
    // Devices expire as "online" if no heartbeat for 2 min (checked by bot)
    return res.status(200).json({ ok: true, device: record });
  }

  return res.status(405).json({ error: 'method not allowed' });
}
