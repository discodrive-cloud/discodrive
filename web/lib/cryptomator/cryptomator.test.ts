/// <reference types="node" />
/**
 * cryptomator.test.ts — interop tests against a real Cryptomator vault.
 *
 * Fixture: daemon/internal/vault/testdata/cmvault/ (password: password123)
 * Expected: find a file whose content is "Привет, мой верный друг!"
 *
 * Run: cd web && npm run test
 */

import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

import { openVault, dirIdHash, decryptName, decryptContent, WrongPasswordError } from './index.js';

// Path to the fixture relative to web/
const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
// The daemon fixture may live in a separate discodrive-apps checkout.
const VAULT_DIR = process.env.CRYPTOMATOR_TEST_VAULT ?? join(__dirname, '../../../daemon/internal/vault/testdata/cmvault');

describe('Cryptomator JS reader — interop against a real vault', () => {
  it('openVault — opens with correct password', async () => {
    const masterkeyJson = readFileSync(join(VAULT_DIR, 'masterkey.cryptomator'), 'utf-8');
    const vaultJwt = readFileSync(join(VAULT_DIR, 'vault.cryptomator'), 'utf-8');

    const keys = await openVault(masterkeyJson, vaultJwt, 'password123');

    expect(keys.encKey).toBeInstanceOf(Uint8Array);
    expect(keys.macKey).toBeInstanceOf(Uint8Array);
    expect(keys.encKey.length).toBe(32);
    expect(keys.macKey.length).toBe(32);
  });

  it('openVault — throws WrongPasswordError on wrong password', async () => {
    const masterkeyJson = readFileSync(join(VAULT_DIR, 'masterkey.cryptomator'), 'utf-8');
    const vaultJwt = readFileSync(join(VAULT_DIR, 'vault.cryptomator'), 'utf-8');

    await expect(openVault(masterkeyJson, vaultJwt, 'wrong')).rejects.toBeInstanceOf(WrongPasswordError);
  });

  it('dirIdHash("") === fixture root directory', async () => {
    const masterkeyJson = readFileSync(join(VAULT_DIR, 'masterkey.cryptomator'), 'utf-8');
    const vaultJwt = readFileSync(join(VAULT_DIR, 'vault.cryptomator'), 'utf-8');
    const keys = await openVault(masterkeyJson, vaultJwt, 'password123');

    const hash = await dirIdHash(keys, '');
    expect(hash).toBe('d/2R/YAZTOIGW6OAZ3R7TNII63JLZV7FBSV');
  });

  it('decrypts real vault — finds "Привет, мой верный друг!"', async () => {
    const masterkeyJson = readFileSync(join(VAULT_DIR, 'masterkey.cryptomator'), 'utf-8');
    const vaultJwt = readFileSync(join(VAULT_DIR, 'vault.cryptomator'), 'utf-8');
    const keys = await openVault(masterkeyJson, vaultJwt, 'password123');

    // Start from the vault root
    const rootHash = await dirIdHash(keys, '');
    expect(rootHash).toBe('d/2R/YAZTOIGW6OAZ3R7TNII63JLZV7FBSV');

    // Walk the d-structure and collect all .c9r files
    const TARGET = 'Привет, мой верный друг!';
    let found = false;

    // Recursive walk: collect d/XX/YYY directories
    const dDir = join(VAULT_DIR, 'd');

    // Scan all d/XX/YYYY directories looking for .c9r data files
    const allDirs = collectDDirs(dDir);

    for (const [dirPath, _encDirFiles] of allDirs) {
      // Read dirid.c9r to determine the directory ID
      const diridPath = join(dirPath, 'dirid.c9r');
      let dirId: string | null = null;
      try {
        const diridCt = readFileSync(diridPath);
        const diridPt = await decryptContent(keys, diridCt);
        dirId = new TextDecoder().decode(diridPt);
      } catch {
        // dirid.c9r missing or failed to decrypt — skip parentDirId resolution
        dirId = null;
      }

      // Read all .c9r files (excluding dirid.c9r and directories)
      const entries = readdirSync(dirPath);
      for (const entry of entries) {
        if (entry === 'dirid.c9r') continue;
        if (!entry.endsWith('.c9r')) continue;

        const entryPath = join(dirPath, entry);
        const stat = statSync(entryPath);
        if (stat.isDirectory()) continue; // this is a .c9r/ directory (dir entry)

        // Try to decrypt as file content
        try {
          const ct = readFileSync(entryPath);
          const pt = await decryptContent(keys, ct);
          const text = new TextDecoder().decode(pt);
          if (text === TARGET) {
            found = true;
          }
        } catch {
          // not a content file — skip
        }
      }

      if (found) break;
    }

    expect(found).toBe(true);
  });

  it('decryptName decrypts names in the root folder', async () => {
    const masterkeyJson = readFileSync(join(VAULT_DIR, 'masterkey.cryptomator'), 'utf-8');
    const vaultJwt = readFileSync(join(VAULT_DIR, 'vault.cryptomator'), 'utf-8');
    const keys = await openVault(masterkeyJson, vaultJwt, 'password123');

    const rootDirPath = join(VAULT_DIR, 'd/2R/YAZTOIGW6OAZ3R7TNII63JLZV7FBSV');
    const entries = readdirSync(rootDirPath);

    const names: string[] = [];
    for (const entry of entries) {
      if (!entry.endsWith('.c9r')) continue;
      try {
        const name = await decryptName(keys, entry, '');
        names.push(name);
      } catch {
        // skip
      }
    }

    // At least one name should decrypt successfully
    expect(names.length).toBeGreaterThan(0);
    console.log('Decrypted names in root:', names);
  });
});

/** Collect all d/XX/YYY directories. */
function collectDDirs(dDir: string): Array<[string, string[]]> {
  const result: Array<[string, string[]]> = [];
  try {
    const level1 = readdirSync(dDir);
    for (const l1 of level1) {
      const l1Path = join(dDir, l1);
      if (!statSync(l1Path).isDirectory()) continue;
      const level2 = readdirSync(l1Path);
      for (const l2 of level2) {
        const l2Path = join(l1Path, l2);
        if (!statSync(l2Path).isDirectory()) continue;
        const files = readdirSync(l2Path);
        result.push([l2Path, files]);
      }
    }
  } catch {
    // ignore
  }
  return result;
}
