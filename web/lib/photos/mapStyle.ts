// The basemap style of the gallery's maps: protomaps layers over the server's own
// region.pmtiles, fonts and sprites (Task 10's /me/photos/v1/maps endpoints). Every one of
// those needs the Bearer token: tiles get it through the pmtiles FetchSource's headers,
// fonts and sprites through MapLibre's transformRequest (mapRequest below).

export const MAPS_API = '/me/photos/v1/maps'
export const MAP_SOURCE = 'protomaps'

export type MapFlavor = 'light' | 'dark'

export function regionURL(origin: string): string {
  return `${origin}${MAPS_API}/region.pmtiles`
}

// mapLang: the label language; protomaps knows both ru and en.
export function mapLang(locale: string): 'ru' | 'en' {
  return locale.toLowerCase().startsWith('ru') ? 'ru' : 'en'
}

export function buildMapStyle(opts: {
  origin: string
  flavor: MapFlavor
  lang: string
  // @protomaps/basemaps' layers(source, namedFlavor(flavor), {lang})
  layers: (source: string, flavor: MapFlavor, lang: string) => unknown[]
}): Record<string, unknown> {
  const { origin, flavor, lang } = opts
  return {
    version: 8,
    glyphs: `${origin}${MAPS_API}/assets/fonts/{fontstack}/{range}.pbf`,
    sprite: `${origin}${MAPS_API}/assets/sprites/v4/${flavor}`,
    sources: {
      [MAP_SOURCE]: {
        type: 'vector',
        url: `pmtiles://${regionURL(origin)}`,
        attribution: '<a href="https://openstreetmap.org/copyright" target="_blank" rel="noopener">© OpenStreetMap</a>',
      },
    },
    layers: opts.layers(MAP_SOURCE, flavor, lang),
  }
}

// mapRequest is MapLibre's transformRequest: the token goes to our map assets only.
export function mapRequest(origin: string, token: string) {
  const prefix = `${origin}${MAPS_API}/`
  return (url: string) => (token && url.startsWith(prefix) ? { url, headers: { Authorization: `Bearer ${token}` } } : { url })
}
