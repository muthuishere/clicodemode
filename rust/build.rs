//! Produces `$OUT_DIR/codemode.wasm`, the Go engine compiled to WASI, which
//! `src/guest.rs` embeds with `include_bytes!`.
//!
//! Where the wasm comes from, in order:
//!   1. `CLICODEMODE_WASM=/path/to/codemode.wasm` (for example a GitHub
//!      release asset): copied as is. No Go needed.
//!   2. `assets/codemode.wasm`, if present (written by `scripts/build-wasi.sh`
//!      in a repository checkout; it is not committed).
//!   3. Build it: `GOOS=wasip1 GOARCH=wasm go build ../cmd/codemode-wasi`.
//!      Needs the repository checkout (a git dependency has it; a crate from
//!      a registry would not) and Go 1.26 or newer on PATH.
//!
//! The source tree is never written to; everything lands in `OUT_DIR`.

use std::env;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

fn main() {
    let out = PathBuf::from(env::var_os("OUT_DIR").expect("OUT_DIR")).join("codemode.wasm");
    let manifest = PathBuf::from(env::var_os("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"));
    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-env-changed=CLICODEMODE_WASM");

    if let Some(p) = env::var_os("CLICODEMODE_WASM").filter(|p| !p.is_empty()) {
        let p = PathBuf::from(p);
        println!("cargo:rerun-if-changed={}", p.display());
        copy(&p, &out, "CLICODEMODE_WASM");
        return;
    }

    let asset = manifest.join("assets").join("codemode.wasm");
    println!("cargo:rerun-if-changed={}", asset.display());
    if asset.is_file() {
        copy(&asset, &out, "assets/codemode.wasm");
        return;
    }

    let root = manifest.join("..");
    let src = root.join("cmd").join("codemode-wasi");
    if !src.join("main.go").is_file() {
        fail(&format!(
            "the Go engine source ({}) is not available, so the wasm cannot be built here",
            src.display()
        ));
    }
    // Rebuild when the engine's Go sources change.
    for d in ["cmd/codemode-wasi", "codemode", "js"] {
        println!("cargo:rerun-if-changed={}", root.join(d).display());
    }
    println!("cargo:rerun-if-changed={}", root.join("go.mod").display());

    let status = Command::new("go")
        .current_dir(&root)
        .env("GOOS", "wasip1")
        .env("GOARCH", "wasm")
        .env("CGO_ENABLED", "0")
        .args(["build", "-trimpath", "-ldflags=-s -w", "-o"])
        .arg(&out)
        .arg("./cmd/codemode-wasi")
        .status();
    match status {
        Ok(s) if s.success() => {}
        Ok(s) => fail(&format!("`go build` of cmd/codemode-wasi failed ({s})")),
        Err(e) => fail(&format!("could not run `go` ({e})")),
    }
}

fn copy(from: &Path, to: &Path, what: &str) {
    if let Err(e) = fs::copy(from, to) {
        fail(&format!("cannot read {what} ({}): {e}", from.display()));
    }
}

fn fail(why: &str) -> ! {
    eprintln!(
        "\nerror: clicodemode needs the engine compiled to WebAssembly (codemode.wasm), but {why}.\n\
         \n\
         Pick one:\n\
         \x20 1. Install Go 1.26 or newer (https://go.dev/dl/) and depend on the crate through git, so the\n\
         \x20    build can compile the engine itself:\n\
         \x20      cargo add clicodemode --git https://github.com/muthuishere/clicodemode\n\
         \x20 2. Download codemode.wasm from the GitHub release assets and point the build at it:\n\
         \x20      CLICODEMODE_WASM=/path/to/codemode.wasm cargo build\n"
    );
    std::process::exit(1);
}
