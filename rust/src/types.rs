//! Typed mirrors of the Go engine's JSON.

use serde::{Deserialize, Deserializer, Serialize};
use serde_json::Value;

fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

/// What an operation may change.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum SideEffects {
    /// Pure read.
    None,
    /// Changes this machine (files, processes). The default.
    Local,
    /// Changes something remote (network, accounts).
    External,
}

/// A worked call shown by `describe` and `docs`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Example {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub args: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub result: Option<Value>,
}

/// One callable unit. Names are dotted segments, e.g. `window.focus` (case-sensitive).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Operation {
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub input_schema: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub output_schema: Option<Value>,
    #[serde(default = "default_side_effects")]
    pub side_effects: SideEffects,
    #[serde(default)]
    pub idempotent: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub authorization: String,
    #[serde(
        default,
        deserialize_with = "null_default",
        skip_serializing_if = "Vec::is_empty"
    )]
    pub examples: Vec<Example>,
}

fn default_side_effects() -> SideEffects {
    SideEffects::Local
}

impl Operation {
    pub fn new(name: impl Into<String>, description: impl Into<String>) -> Self {
        Operation {
            name: name.into(),
            description: description.into(),
            input_schema: None,
            output_schema: None,
            side_effects: SideEffects::Local,
            idempotent: false,
            authorization: String::new(),
            examples: Vec::new(),
        }
    }
    pub fn side_effects(mut self, s: SideEffects) -> Self {
        self.side_effects = s;
        self
    }
    pub fn idempotent(mut self, v: bool) -> Self {
        self.idempotent = v;
        self
    }
    /// JSON Schema for the arguments (type, properties, required, enum, items, additionalProperties).
    pub fn input_schema(mut self, s: Value) -> Self {
        self.input_schema = Some(s);
        self
    }
    pub fn output_schema(mut self, s: Value) -> Self {
        self.output_schema = Some(s);
        self
    }
    /// Scope the host must grant to call this operation.
    pub fn authorization(mut self, a: impl Into<String>) -> Self {
        self.authorization = a.into();
        self
    }
    pub fn example(mut self, e: Example) -> Self {
        self.examples.push(e);
        self
    }
}

/// A problem found by check, compile or run. Line and column are 1-based.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Diagnostic {
    pub severity: String,
    #[serde(default)]
    pub file: String,
    #[serde(default)]
    pub line: u32,
    #[serde(default)]
    pub col: u32,
    pub message: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub hint: String,
}

impl std::fmt::Display for Diagnostic {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "{}:{}:{}: {}: {}",
            self.file, self.line, self.col, self.severity, self.message
        )?;
        if !self.hint.is_empty() {
            write!(f, " ({})", self.hint)?;
        }
        Ok(())
    }
}

/// Outcome of `run` or `eval`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct RunResult {
    pub ok: bool,
    #[serde(default)]
    pub value: Value,
    #[serde(default)]
    pub error: String,
    #[serde(default, deserialize_with = "null_default")]
    pub diagnostics: Vec<Diagnostic>,
    /// Milliseconds spent inside the engine.
    #[serde(default)]
    pub ms: i64,
    /// What the program printed (`print`, `console.log`).
    #[serde(default, skip_deserializing)]
    pub stdout: String,
}

/// A checked program.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Program {
    pub file: String,
    pub hash: String,
    #[serde(default, deserialize_with = "null_default")]
    pub imports: Vec<String>,
    /// Operations the program references statically.
    #[serde(default, deserialize_with = "null_default")]
    pub operations: Vec<String>,
    pub cache_key: String,
}

/// Result of `compile`: the plan (absent if the source does not parse) and diagnostics.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct CompileResult {
    pub program: Option<Program>,
    #[serde(default, deserialize_with = "null_default")]
    pub diagnostics: Vec<Diagnostic>,
}

/// What the program language supports.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct LanguageInfo {
    pub name: String,
    pub features: Vec<String>,
    pub unsupported: Vec<String>,
    pub globals: Vec<String>,
}

/// The machine-readable summary agents cache.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Capabilities {
    pub spec: String,
    pub version: String,
    /// `sha256:` + 32 hex; changes whenever any operation's contract changes.
    pub fingerprint: String,
    pub language: LanguageInfo,
    #[serde(default, deserialize_with = "null_default")]
    pub operations: Vec<Operation>,
}
