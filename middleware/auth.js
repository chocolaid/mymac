// middleware/auth.js
// Express middleware for authenticating Mac agent requests.
'use strict';

const { createHash, timingSafeEqual: cryptoTSE } = require('crypto');

/**
 * agentAuth – validates the X-Agent-Secret header against process.env.AGENT_SECRET.
 * Mount on any route the agent POSTs/GETs to.
 */
function agentAuth(req, res, next) {
  const expected = process.env.AGENT_SECRET;
  const provided = req.headers['x-agent-secret'];

  if (!expected) {
    console.error('[auth] AGENT_SECRET env var not set');
    return res.status(500).json({ error: 'server misconfiguration' });
  }
  if (!provided || !timingSafeEqual(provided, expected)) {
    return res.status(401).json({ error: 'unauthorized' });
  }
  next();
}

/**
 * adminAuth – validates the X-Admin-Token header against process.env.ADMIN_TOKEN.
 * Used for config management endpoints called by the bot from the VPS.
 */
function adminAuth(req, res, next) {
  const expected = process.env.ADMIN_TOKEN;
  const provided = req.headers['x-admin-token'];

  if (!expected) {
    console.error('[auth] ADMIN_TOKEN env var not set');
    return res.status(500).json({ error: 'server misconfiguration' });
  }
  if (!provided || !timingSafeEqual(provided, expected)) {
    return res.status(401).json({ error: 'unauthorized' });
  }
  next();
}

/**
 * Constant-time string comparison (prevents timing attacks).
 */
function timingSafeEqual(a, b) {
  try {
    const ha = createHash('sha256').update(String(a)).digest();
    const hb = createHash('sha256').update(String(b)).digest();
    return cryptoTSE(ha, hb);
  } catch {
    return a === b;
  }
}

module.exports = { agentAuth, adminAuth, timingSafeEqual };
