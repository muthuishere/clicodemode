import { inspect, report, poll } from "./steps/health.js"

const wf = workflow("health-check", [
  step("inspect", inspect, { retry: { attempts: 3, backoff: "2s" }, timeout: "30s" }),
  step("wait-ready", poll, {
    needs: ["inspect"],
    outcomes: ["ready", "waiting"],   // exported as WFNexus exit-codes 10/11
    until: { path: "Steps.wait-ready.outcome", equals: "ready" },
    maxIterations: 5,
  }),
  step("report", report, { needs: ["inspect", "wait-ready"] }),
])

export default async function main() {
  const run = await wf.run()
  return { ok: run.ok, statuses: Object.fromEntries(Object.entries(run.steps).map(([k, v]) => [k, v.status])), yaml: wf.toYAML() }
}
