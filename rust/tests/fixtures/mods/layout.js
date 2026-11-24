// Layout helpers.
import { twice } from "./util/math.js"
import cfg from "./cfg.json"
/** Doubles then adds the configured offset. */
export async function calc(n) { return (await ops.math.add({ a: twice(n), b: cfg.offset })).sum }
