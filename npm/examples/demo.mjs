import { createCodeMode } from "../dist/index.js";

const cm = createCodeMode({ version: "1.0.0" });
cm.register({
  name: "demo.add",
  description: "Add two numbers",
  sideEffects: "none",
  idempotent: true,
  handler: async ({ a, b }) => ({ sum: a + b }),
});

console.log((await cm.capabilities()).fingerprint);
const r = await cm.run(`
  const rs = await pmap([1, 2, 3], (n) => ops.demo.add({ a: n, b: input.offset }), { concurrency: 2 });
  rs.map((x) => x.sum)
`, { input: { offset: 10 } });
console.log(r.ok, r.value);
