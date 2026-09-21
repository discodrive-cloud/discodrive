/** Ordered mapping with a bounded number of in-flight operations. Drain on error. */
export async function mapConcurrent<T, R>(items: T[], limit: number, map: (item: T) => Promise<R>): Promise<R[]> {
  if (!Number.isInteger(limit) || limit < 1) throw new RangeError('Invalid concurrency limit')
  const result = new Array<R>(items.length)
  let next = 0
  let failed = false
  let failure: unknown
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (!failed && next < items.length) {
      const index = next++
      try {
        result[index] = await map(items[index])
      } catch (error) {
        if (!failed) failure = error
        failed = true
      }
    }
  }))
  if (failed) throw failure
  return result
}
