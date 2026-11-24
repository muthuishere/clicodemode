// Window layouts built from aos primitives.
import { clamp } from "./util/math.js"
import presets from "./presets.json"

/** Put two apps side by side on a monitor. */
export async function sideBySide(left, right, monitor = 1) {
  const m = clamp(monitor, 1, presets.maxMonitors)
  return Promise.all([
    ops.window.move({ app: left, zone: "left", monitor: m }),
    ops.window.move({ app: right, zone: "right", monitor: m }),
  ])
}

// The named presets.
export const names = Object.keys(presets.layouts)
