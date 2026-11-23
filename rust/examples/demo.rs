use clicodemode::{CodeMode, Operation, RunOptions, SideEffects};
use serde_json::json;
use std::time::Instant;

fn main() -> Result<(), clicodemode::Error> {
    let mut cm = CodeMode::new("1.0.0")?;
    cm.register(
        Operation::new("health.check", "Report whether this machine is healthy.")
            .side_effects(SideEffects::None)
            .idempotent(true)
            .input_schema(json!({"type":"object","properties":{"verbose":{"type":"boolean"}}})),
        |args| Ok(json!({"ok": true, "verbose": args["verbose"] == json!(true)})),
    )?;
    cm.register(
        Operation::new("disk.free", "Free space in GB for a mount.")
            .side_effects(SideEffects::None)
            .input_schema(json!({"type":"object","required":["mount"],"properties":{"mount":{"type":"string"}}})),
        |args| Ok(json!({"mount": args["mount"], "free_gb": 42})),
    )?;

    let t = Instant::now();
    let r = cm.run(
        r#"export default async () => {
            const [h, d] = await Promise.all([ops.health.check({verbose: true}), ops.disk.free({mount: input.mount})]);
            console.log("checked", d.mount);
            return {healthy: h.ok, free: d.free_gb};
        }"#,
        RunOptions::default().input(json!({"mount": "/"})),
    )?;
    println!("result: {}  (first run incl. startup: {:?})", r.value, t.elapsed());
    println!("program printed: {:?}", r.stdout);

    for d in cm.check("class A {}", "prog.js")? {
        println!("diagnostic: {d}");
    }
    println!("fingerprint: {}", cm.capabilities()?.fingerprint);
    Ok(())
}
