// lib/db.js – Firebase Firestore helpers (replaces lib/ec.js)
// Each logical key ('devices', 'config') is stored as a Firestore document
// in the 'mymac' collection, with the data nested under a 'value' field.
//
// Required env vars:
//   FIREBASE_PROJECT_ID   – Firebase project ID
//   FIREBASE_CLIENT_EMAIL – Service account e-mail
//   FIREBASE_PRIVATE_KEY  – Service account private key (newlines as \n)

'use strict';

let _db;

function getDb() {
  if (_db) return _db;
  // Lazy-require so the module can be imported without crashing during build
  // if the env vars are not yet set.
  const admin = require('firebase-admin');
  if (!admin.apps.length) {
    admin.initializeApp({
      credential: admin.credential.cert({
        projectId:   process.env.FIREBASE_PROJECT_ID,
        clientEmail: process.env.FIREBASE_CLIENT_EMAIL,
        privateKey:  (process.env.FIREBASE_PRIVATE_KEY ?? '').replace(/\\n/g, '\n'),
      }),
    });
  }
  _db = admin.firestore();
  return _db;
}

const COLLECTION = 'mymac';

// Read a single key; returns the stored value or null if not found.
async function ecGet(key) {
  const doc = await getDb().collection(COLLECTION).doc(key).get();
  if (!doc.exists) return null;
  return doc.data().value ?? null;
}

// Upsert one or more keys: ecSet({ key1: val1, key2: val2 })
async function ecSet(kvPairs) {
  const db    = getDb();
  const batch = db.batch();
  for (const [key, value] of Object.entries(kvPairs)) {
    batch.set(db.collection(COLLECTION).doc(key), { value });
  }
  await batch.commit();
}

// Delete a single key.
async function ecDel(key) {
  await getDb().collection(COLLECTION).doc(key).delete();
}

// Read all keys → { key: value, … }
async function ecGetAll() {
  const snap   = await getDb().collection(COLLECTION).get();
  const result = {};
  snap.forEach((doc) => { result[doc.id] = doc.data().value; });
  return result;
}

module.exports = { ecGet, ecSet, ecDel, ecGetAll };
