// pages/api/release.js
// GET  – agent checks for the latest release (version + download URLs)
// POST – admin publishes a new release (called by CI or bot /setrelease)
import { ecGet, ecSet } from '../../lib/db';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN;

export default async function handler(req, res) {
  const token = req.headers['x-admin-token'];
  if (!token || token !== ADMIN_TOKEN) return res.status(401).json({ error: 'unauthorized' });

  if (req.method === 'GET') {
    const release = (await ecGet('release')) ?? { version: '', arm64Url: '', amd64Url: '' };
    return res.status(200).json(release);
  }

  if (req.method === 'POST') {
    const { version, arm64Url, amd64Url, arm64Sha256, amd64Sha256 } = req.body ?? {};
    if (!version) return res.status(400).json({ error: 'version required' });
    const release = {
      version,
      arm64Url:    arm64Url    ?? '',
      amd64Url:    amd64Url    ?? '',
      arm64Sha256: arm64Sha256 ?? '',
      amd64Sha256: amd64Sha256 ?? '',
      publishedAt: new Date().toISOString(),
    };
    await ecSet({ release });
    return res.status(200).json({ ok: true, release });
  }

  return res.status(405).json({ error: 'method not allowed' });
}
