import { describe, it, expect } from 'vitest'
import { parseCoords, formatCoord } from './coords'

function near(got: { lat: number; lon: number } | null, lat: number, lon: number) {
  expect(got).not.toBeNull()
  expect(got!.lat).toBeCloseTo(lat, 4)
  expect(got!.lon).toBeCloseTo(lon, 4)
}

describe('parseCoords', () => {
  it('reads a decimal pair with a comma', () => {
    near(parseCoords('42.6475, 77.0820'), 42.6475, 77.082)
    near(parseCoords('42.6475,77.0820'), 42.6475, 77.082)
    near(parseCoords('  -33.8688 , 151.2093 '), -33.8688, 151.2093)
  })

  it('reads decimal commas', () => {
    // one number with a decimal comma is not a pair
    expect(parseCoords('42,6475')).toBeNull()
    expect(parseCoords('42,6')).toBeNull()
    near(parseCoords('42,6475, 77,0820'), 42.6475, 77.082)
    near(parseCoords('42,6475 77,0820'), 42.6475, 77.082)
    near(parseCoords('42,6475; 77,0820'), 42.6475, 77.082)
    near(parseCoords('42, 77'), 42, 77)
    // a bare comma between integers is a decimal comma, not a separator
    expect(parseCoords('42,77')).toBeNull()
  })

  it('reads a decimal pair with a space', () => {
    near(parseCoords('42.6475 77.0820'), 42.6475, 77.082)
    near(parseCoords('42 77'), 42, 77)
  })

  it('reads degrees, minutes and seconds', () => {
    near(parseCoords(`42°38'51"N 77°4'55"E`), 42.6475, 77.08194)
    near(parseCoords('42°38′51″N, 77°4′55″E'), 42.6475, 77.08194)
    near(parseCoords(`33°52'7.7"S 151°12'33.5"E`), -33.86881, 151.20931)
    near(parseCoords(`40°26'46"N 79°58'56"W`), 40.44611, -79.98222)
    near(parseCoords(`N42°38'51" E77°4'55"`), 42.6475, 77.08194)
    // longitude first, told apart by the letters
    near(parseCoords(`77°4'55"E 42°38'51"N`), 42.6475, 77.08194)
    // degrees and decimal minutes
    near(parseCoords(`42°38.85'N 77°4.9167'E`), 42.6475, 77.08194)
  })

  it('reads Google Maps links', () => {
    near(parseCoords('https://www.google.com/maps/@42.6475,77.082,15z'), 42.6475, 77.082)
    near(parseCoords('https://www.google.com/maps/place/Cholpon-Ata/@42.6475,77.0820,13z/data=!3m1!4b1'), 42.6475, 77.082)
    near(parseCoords('https://maps.google.com/?q=42.6475,77.0820'), 42.6475, 77.082)
  })

  it('reads Apple Maps links', () => {
    near(parseCoords('https://maps.apple.com/?ll=42.6475,77.0820&q=Pin'), 42.6475, 77.082)
    near(parseCoords('https://maps.apple.com/?q=Pin&ll=42.6475,77.0820'), 42.6475, 77.082)
  })

  it('reads OpenStreetMap links', () => {
    near(parseCoords('https://www.openstreetmap.org/#map=15/42.6475/77.0820'), 42.6475, 77.082)
    near(parseCoords('https://www.openstreetmap.org/?mlat=42.6475&mlon=77.0820#map=15/42.6/77.0'), 42.6475, 77.082)
  })

  it('checks the ranges', () => {
    expect(parseCoords('91, 10')).toBeNull()
    expect(parseCoords('-90.0001, 10')).toBeNull()
    expect(parseCoords('10, 180.5')).toBeNull()
    expect(parseCoords('10, -181')).toBeNull()
    expect(parseCoords(`95°0'0"N 10°0'0"E`)).toBeNull()
    expect(parseCoords('https://www.google.com/maps/@95.1,77.0,15z')).toBeNull()
    near(parseCoords('90, -180'), 90, -180)
  })

  it('rejects rubbish', () => {
    for (const s of [
      '', '   ', 'hello', '42.6475', '42.6475, ', ', 77.08', '42.6475, 77.0820, 5',
      'abc, def', '42.6.4, 77', '1e3, 5', 'https://example.com/', 'https://maps.apple.com/?q=Cafe',
      `42°38'51"N 43°4'55"N`, `42°75'0"N 77°0'0"E`, `42°38'51"NS 77°4'55"E`, 'Чолпон-Ата',
    ]) {
      expect(parseCoords(s), s).toBeNull()
    }
  })
})

describe('formatCoord', () => {
  it('keeps six decimals without trailing zeros', () => {
    expect(formatCoord(42.647512345)).toBe('42.647512')
    expect(formatCoord(77.08)).toBe('77.08')
    expect(formatCoord(-0.5)).toBe('-0.5')
  })
})
