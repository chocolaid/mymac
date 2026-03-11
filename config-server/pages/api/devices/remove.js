// pages/api/devices/remove.js
// POST – remove a device registration
import { ecGet, ecSet } from '../../../lib/db';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN;

export default async function handler(req, res) {
  if (req.method !== 'POST') return res.status(405).end();

  const token = req.headers['x-admin-token'];
  if (!token || token !== ADMIN_TOKEN) return res.status(401).json({ error: 'unauthorized' });

  const { deviceId } = req.body ?? {};
  if (!deviceId) return res.status(400).json({ error: 'deviceId required' });

  const store = (await ecGet('devices')) ?? {};
  delete store[deviceId];
  await ecSet({ devices: store });
  return res.status(200).json({ ok: true });
}
