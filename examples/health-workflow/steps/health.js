export async function inspect(ctx) {
  const h = await call("health.check", { verbose: true })
  return { ok: h.ok, os: h.os }
}

export function report(ctx) {
  return { line: `machine ${ctx.steps.inspect?.os ?? "?"} is healthy` }
}

export async function poll(ctx) {
  await call("time.sleep", { ms: 10 })
  return { outcome: "ready" }   // or "waiting"; declared in main.js outcomes
}
