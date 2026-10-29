import { describe, it, expect } from 'vitest'
import { mergePins, formatArea, parseArea, paddedBbox, padFactor, clustersQuery, type Cluster } from './mapClusters'

// A flat projection: one degree is ten pixels.
const project = (lat: number, lon: number) => ({ x: lon * 10, y: -lat * 10 })

function c(cell: string, lat: number, lon: number, count: number, place: string | null = null, country: string | null = null): Cluster<{ id: string }> {
  return { cell, lat, lon, count, place, country, cover: { id: cell } }
}

describe('mergePins', () => {
  it('merges clusters closer than the minimum distance into the biggest', () => {
    const pins = mergePins([c('1.1', 0, 0, 10, 'Bishkek', 'KG'), c('2.1', 0, 3, 30, 'Bishkek', 'KG'), c('9.9', 0, 50, 5)], project)
    expect(pins).toHaveLength(2)
    const [big, far] = pins
    expect(big.count).toBe(40)
    expect(big.cells.sort()).toEqual(['1.1', '2.1'])
    expect(big.cover.id).toBe('2.1')
    expect(big.lon).toBeCloseTo((0 * 10 + 3 * 30) / 40)
    expect(big.place).toBe('Bishkek')
    expect(big.country).toBe('KG')
    expect(far.cells).toEqual(['9.9'])
  })

  it('keeps a place or country only when every merged cluster shares it', () => {
    const [p] = mergePins([c('1.1', 0, 0, 2, 'Bishkek', 'KG'), c('1.2', 0, 1, 1, 'Osh', 'KG')], project)
    expect(p.place).toBeNull()
    expect(p.country).toBe('KG')
    const [q] = mergePins([c('1.1', 0, 0, 2, 'Bishkek', 'KG'), c('1.2', 0, 1, 1, null, null)], project)
    expect(q.place).toBeNull()
    expect(q.country).toBeNull()
  })

  it('leaves clusters far apart alone', () => {
    expect(mergePins([c('1.1', 0, 0, 1), c('1.2', 0, 6, 1)], project)).toHaveLength(2)
  })
})

describe('area', () => {
  it('round-trips', () => {
    expect(formatArea(6, ['10.20', '11.20'])).toBe('6/10.20,11.20')
    expect(parseArea('6/10.20,11.20')).toEqual({ z: 6, cells: ['10.20', '11.20'] })
  })
  it('rejects junk', () => {
    for (const s of ['', '6', '6/', 'x/1.1', '6/1', '6/1.x', '21/1.1', '6/1.1,']) expect(parseArea(s)).toBeNull()
  })
})

describe('paddedBbox', () => {
  it('pads around the centre', () => {
    expect(paddedBbox({ w: 70, s: 40, e: 80, n: 44 }, 1.5)).toEqual({ w: 67.5, s: 39, e: 82.5, n: 45 })
  })
  it('stays inside the world', () => {
    // At the projection's edge the box runs to the pole: photos above 85° sit in the edge row.
    const b = paddedBbox({ w: -170, s: -80, e: 170, n: 84 }, 1.5)
    expect(b).toEqual({ w: -180, s: -90, e: 180, n: 90 })
  })
  it('wraps a box past the 180th meridian into w > e', () => {
    const b = paddedBbox({ w: 170, s: 0, e: 190, n: 10 }, 1)
    expect(b.w).toBe(170)
    expect(b.e).toBe(-170)
  })
})

describe('clustersQuery', () => {
  it('puts the box as w,s,e,n', () => {
    const q = new URLSearchParams(clustersQuery(4, { w: 170, s: -30, e: -170, n: 0 }))
    expect(q.get('z')).toBe('4')
    expect(q.get('bbox')).toBe('170,-30,-170,0')
  })
  it('rounds to 5 decimals', () => {
    expect(new URLSearchParams(clustersQuery(1, { w: 1.123456789, s: 0, e: 2, n: 3 })).get('bbox')).toBe('1.12346,0,2,3')
  })
})

describe('padFactor', () => {
  it('leaves at least two cells of margin on a narrow screen', () => {
    const k = padFactor(375, 700)
    expect(((k - 1) / 2) * 375).toBeGreaterThanOrEqual(256 - 1e-9)
  })
  it('never pads less than 1.5', () => {
    expect(padFactor(4000, 3000)).toBe(1.5)
    expect(padFactor(0, 0)).toBe(1.5)
  })
})
