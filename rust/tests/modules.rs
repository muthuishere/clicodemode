use clicodemode::{CodeMode, Operation};
use serde_json::{json, Value};

fn engine() -> CodeMode {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.register(Operation::new("math.add", "add"), |a: Value| {
        Ok(json!({"sum": a["a"].as_f64().unwrap() + a["b"].as_f64().unwrap()}))
    })
    .unwrap();
    cm
}

#[test]
fn embedded_modules_import_by_name() {
    let mut cm = engine();
    let names = cm
        .register_modules(
            "my",
            clicodemode::embed_modules!(
                "tests/fixtures/mods",
                ["layout.js", "util/math.js", "cfg.json"]
            ),
        )
        .unwrap();
    assert_eq!(names, ["my:layout", "my:util/math", "my:cfg.json"]);
    let r = cm
        .run(
            r#"import { calc } from "my:layout"; await calc(5)"#,
            Default::default(),
        )
        .unwrap();
    assert!(r.ok, "{r:?}");
    assert_eq!(r.value, json!(11));
    let caps = cm.capabilities().unwrap();
    let layout = caps.modules.iter().find(|m| m.name == "my:layout").unwrap();
    assert_eq!(layout.description, "Layout helpers.");
    assert_eq!(
        layout.exports[0].doc,
        "Doubles then adds the configured offset."
    );
}

#[test]
fn modules_from_dir_file_and_content() {
    let mut cm = engine();
    let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/tests/fixtures/mods");
    cm.register_modules_dir("d", dir).unwrap();
    cm.register_module_file("f", format!("{dir}/util/math.js"))
        .unwrap();
    cm.register_module("c:one", "export const one = 1").unwrap();
    let r = cm
        .run(
            r#"import { calc } from "d:layout"; import { twice } from "f:math"; import { one } from "c:one"; (await calc(1)) + twice(1) + one"#,
            Default::default(),
        )
        .unwrap();
    assert_eq!(r.value, json!(6), "{r:?}");
    assert!(cm.register_module("nocolon", "").is_err());
    assert!(cm.register_module("c:one", "").is_err());
}

#[test]
fn build_helper_writes_include_str_entries() {
    let out = std::env::temp_dir().join(format!("ccm-build-{}", std::process::id()));
    std::fs::create_dir_all(&out).unwrap();
    std::env::set_var("OUT_DIR", &out);
    clicodemode::build::embed_dir("tests/fixtures/mods", "modules.rs").unwrap();
    let gen = std::fs::read_to_string(out.join("modules.rs")).unwrap();
    assert!(
        gen.contains("(\"layout.js\", include_str!(")
            && gen.contains("(\"util/math.js\", include_str!("),
        "{gen}"
    );
}
