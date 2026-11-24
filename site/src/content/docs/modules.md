---
title: Ship your own JS modules
description: "Bake JavaScript modules into your CLI's build so agents import higher-level building blocks instead of composing raw operations."
---

Operations are your CLI's primitives: each one does a single thing, with a schema, side effects and authorization. **Modules** are the building blocks on top of them, such as a window layout, a release checklist or a deploy recipe, written in the same JavaScript as agent programs and shipped inside your binary or package. An agent imports one instead of rebuilding it every time:

```js
import { sideBySide } from "aos:layout"
await sideBySide("Code", "Safari", 2)
```

A module can only call the operations you registered, under the same limits, so it adds no privilege. It is checked like a program: an unknown operation or a bad literal argument in a module is a check error, and nothing runs.

## Names

`scheme:path`. A `.js` file drops its extension (`js/layout.js` becomes `aos:layout`, `js/util/math.js` becomes `aos:util/math`); a `.json` file keeps it (`aos:presets.json`, imported as its default export). Inside a module, `./` and `../` imports stay within the same scheme. A file's leading comment is its description, and the comment above each `export` is that export's doc.

```js
// Window layouts built from aos primitives.
import presets from "./presets.json"

/** Put two apps side by side on a monitor. */
export async function sideBySide(left, right, monitor = 1) {
  return Promise.all([
    ops.window.move({ app: left, zone: "left", monitor }),
    ops.window.move({ app: right, zone: "right", monitor }),
  ])
}
```

## Register them: a folder, one file, or plain content

Every language takes all three, and the files are baked in at build time.

### Go (`go:embed`)

```go
//go:embed js
var jsFiles embed.FS

reg.MustRegisterModules("aos", jsFiles, "js")                 // whole folder
reg.MustRegisterModule(codemode.Module{Name: "aos:quick", Source: src}) // content
_ = reg.RegisterModuleFile("aos", jsFiles, "js/layout.js")    // one file
```

### Rust (compiled in)

```rust
// listed files, with include_str!
cm.register_modules("mycli", clicodemode::embed_modules!("js", ["layout.js", "util/math.js"]))?;

// a whole folder: build.rs
fn main() { clicodemode::build::embed_dir("js", "modules.rs").unwrap(); }
// then in your code
cm.register_modules("mycli", include!(concat!(env!("OUT_DIR"), "/modules.rs")))?;

cm.register_module("mycli:quick", "export const x = 1")?;   // content
```

`register_modules_dir` and `register_module_file` also exist for reading at startup.

### TypeScript / Node

```sh
npx clicodemode-embed js src/modules.generated.js   # build step: folder -> one module
```

```ts
import modules from "./modules.generated.js";
cm.registerModules("mycli", modules);                       // {path: source} map
cm.registerModule({ name: "mycli:quick", source: "export const x = 1" });
cm.registerModuleFile("mycli", new URL("./js/layout.js", import.meta.url)); // one file
```

A bundler's raw imports (`import src from "./layout.js?raw"`) work the same way. `registerModules(scheme, dirOrURL)` also reads a folder at startup.

### Python (package data in the wheel)

```python
from importlib.resources import files

cm.register_modules("mycli", files("mycli") / "js")           # folder, also from a zip/wheel
cm.register_module_file("mycli", files("mycli") / "js" / "layout.js")
cm.register_module("mycli:quick", "export const x = 1")
cm.register_modules("mycli", {"layout.js": SRC})              # a dict of contents
```

## How agents find them

- `mycli code modules` lists modules with descriptions.
- `mycli code describe aos:layout` shows the exports, their docs and the source.
- `mycli code skill` puts a **Modules** section before Operations, so the agent reaches for your building blocks first.
- `capabilities --json` carries `modules` with exports and docs, and the fingerprint changes when a module changes.

In your own tests, `engine.CheckModules()` (Go) checks every module against the registered operations.

Requirements MOD-1 to MOD-4 in the [specification](/clicodemode/reference/spec/) define this.
