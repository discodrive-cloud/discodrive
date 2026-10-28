// Loads MapLibre, pmtiles and the protomaps style on first use (import(): the gallery
// bundle does not carry them) and registers one pmtiles protocol for the page, whose
// archive is the server's region.pmtiles.
import { regionURL } from './mapStyle'

export interface MapLib {
  maplibregl: any
  source: any // pmtiles FetchSource: setHeaders() before use (the token renews)
  archive: any // pmtiles PMTiles of the region
  basemaps: any
}

let shared: Promise<MapLib> | null = null

export function loadMapLib(): Promise<MapLib> {
  if (!shared) {
    shared = (async () => {
      // The CSP build: the app's Content-Security-Policy allows scripts and workers from
      // 'self' only, so the worker is a file of our own (not a blob:) and nothing evals.
      const [ml, worker, pm, basemaps] = await Promise.all([
        import('maplibre-gl/dist/maplibre-gl-csp.js' as string),
        import('maplibre-gl/dist/maplibre-gl-csp-worker.js?url' as string),
        import('pmtiles'),
        import('@protomaps/basemaps'),
        import('maplibre-gl/dist/maplibre-gl.css'),
      ])
      const maplibregl = (ml as any).default ?? ml
      maplibregl.setWorkerUrl((worker as any).default)
      const protocol = new pm.Protocol()
      maplibregl.addProtocol('pmtiles', protocol.tile)
      const source = new pm.FetchSource(regionURL(location.origin), new Headers())
      const archive = new pm.PMTiles(source)
      protocol.add(archive)
      return { maplibregl, source, archive, basemaps }
    })()
    shared.catch(() => { shared = null })
  }
  return shared
}
