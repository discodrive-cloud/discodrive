import { describe, expect, it } from 'vitest'
import { openRegion, type MapLib } from './mapLib'

// A region that is missing at first (a new one still downloading), then present.
function fakeLib() {
  let present = false
  const added: any[] = []
  class PMTiles {
    read?: Promise<any>
    constructor(public source: any) {}
    // Like pmtiles' cache: the first read, failed or not, is kept for good.
    getHeader() {
      this.read ??= present ? Promise.resolve({ maxZoom: 12 }) : Promise.reject(new Error('404'))
      return this.read
    }
  }
  const lib = {
    maplibregl: null, basemaps: null, source: {},
    pm: { PMTiles },
    protocol: { add: (a: any) => added.push(a) },
    archive: new PMTiles({}),
  } as unknown as MapLib
  return { lib, added, arrive: () => { present = true } }
}

describe('openRegion', () => {
  it('reads the region again for a new map after a failed read', async () => {
    const { lib, added, arrive } = fakeLib()
    await expect(openRegion(lib)).rejects.toThrow('404')
    arrive()
    await expect(openRegion(lib)).resolves.toEqual({ maxZoom: 12 })
    expect(added).toHaveLength(2)
    expect(added[1]).toBe(lib.archive)
  })
})
