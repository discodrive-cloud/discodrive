// backoffDelay — wait before retry number `attempt` (1-based): exponential growth from
// `baseMs`, capped at `capMs`, with "equal jitter" (half fixed, half random) so parallel
// uploads that failed together do not retry in lockstep against a struggling server.
export function backoffDelay(attempt: number, baseMs = 500, capMs = 8000, random: () => number = Math.random): number {
  const exp = Math.min(capMs, baseMs * 2 ** Math.max(0, attempt - 1))
  return Math.round(exp / 2 + random() * (exp / 2))
}

export const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))
