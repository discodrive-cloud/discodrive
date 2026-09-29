import { describe, expect, it } from 'vitest';
import { checkScryptParams, openVault } from './keys';

const masterkey = (N: unknown, r: unknown) => JSON.stringify({
  scryptSalt: 'AAAAAAAAAAA=',
  scryptCostParam: N,
  scryptBlockSize: r,
  primaryMasterKey: 'A'.repeat(56),
  hmacMasterKey: 'A'.repeat(56),
});

describe('scrypt parameter bounds', () => {
  it('accepts what Cryptomator writes and the documented ceiling', () => {
    expect(() => checkScryptParams(32768, 8)).not.toThrow();
    expect(() => checkScryptParams(2 ** 20, 8)).not.toThrow();
  });

  it('rejects out-of-range or malformed parameters', () => {
    for (const [N, r] of [
      [2 ** 21, 8], [2 ** 30, 8], [3, 8], [1, 8], [0, 8], [-2, 8], [32768.5, 8], ['32768', 8], [null, 8],
      [32768, 0], [32768, 33], [32768, 1.5], [32768, '8'],
      [2 ** 20, 32], // each within its cap, but 4 GiB of memory
    ] as [unknown, unknown][]) {
      expect(() => checkScryptParams(N, r), `N=${String(N)} r=${String(r)}`).toThrow(/unsupported vault/);
    }
  });

  it('openVault refuses a hostile masterkey before running scrypt', async () => {
    const started = Date.now();
    await expect(openVault(masterkey(2 ** 30, 8), 'a.b.c', 'pw')).rejects.toThrow(/scryptCostParam/);
    await expect(openVault(masterkey(16384, 64), 'a.b.c', 'pw')).rejects.toThrow(/scryptBlockSize/);
    expect(Date.now() - started).toBeLessThan(1000);
  });
});
