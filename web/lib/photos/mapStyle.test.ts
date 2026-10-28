import { describe, it, expect } from 'vitest'
import { buildMapStyle, mapLang, mapRequest, regionURL } from './mapStyle'

describe('map style', () => {
  it('points every resource at the server', () => {
    const calls: unknown[][] = []
    const style = buildMapStyle({
      origin: 'https://dd.example',
      flavor: 'dark',
      lang: 'ru',
      layers: (...a) => { calls.push(a); return [{ id: 'x' }] },
    }) as any
    expect(style.glyphs).toBe('https://dd.example/me/photos/v1/maps/assets/fonts/{fontstack}/{range}.pbf')
    expect(style.sprite).toBe('https://dd.example/me/photos/v1/maps/assets/sprites/v4/dark')
    expect(style.sources.protomaps.url).toBe('pmtiles://https://dd.example/me/photos/v1/maps/region.pmtiles')
    expect(style.layers).toEqual([{ id: 'x' }])
    expect(calls).toEqual([['protomaps', 'dark', 'ru']])
    expect(regionURL('https://dd.example')).toBe('https://dd.example/me/photos/v1/maps/region.pmtiles')
  })

  it('picks the label language', () => {
    expect(mapLang('ru')).toBe('ru')
    expect(mapLang('ru-RU')).toBe('ru')
    expect(mapLang('en')).toBe('en')
    expect(mapLang('de')).toBe('en')
  })

  it('adds the token to map assets only', () => {
    const tr = mapRequest('https://dd.example', 'T')
    expect(tr('https://dd.example/me/photos/v1/maps/assets/sprites/v4/light.json')).toEqual({
      url: 'https://dd.example/me/photos/v1/maps/assets/sprites/v4/light.json', headers: { Authorization: 'Bearer T' },
    })
    expect(tr('https://elsewhere.example/me/photos/v1/maps/x')).toEqual({ url: 'https://elsewhere.example/me/photos/v1/maps/x' })
    expect(tr('https://dd.example/files')).toEqual({ url: 'https://dd.example/files' })
  })
})
