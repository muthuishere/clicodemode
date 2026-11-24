from pathlib import Path

import pytest

from clicodemode import CodeMode, UsageError

FIX = Path(__file__).parent / "fixtures" / "mods"


def _add(cm: CodeMode) -> None:
    @cm.operation("math.add")
    def add(args):  # noqa: ANN001, ANN202
        return {"sum": args["a"] + args["b"]}


def test_register_modules_from_folder(cm: CodeMode) -> None:
    _add(cm)
    names = cm.register_modules("my", FIX)
    assert names == ["my:cfg.json", "my:layout", "my:util/math"]
    r = cm.run('import { calc } from "my:layout"; await calc(5)')
    assert r.ok and r.value == 11, r
    caps = cm.capabilities()
    layout = next(m for m in caps.modules if m["name"] == "my:layout")
    assert layout["description"] == "Layout helpers."
    assert layout["exports"] == [{"name": "calc", "doc": "Doubles then adds the configured offset."}]
    assert 'import { calc } from "my:layout"' in cm.skill()


def test_register_modules_from_zip_package_data(cm: CodeMode, tmp_path: Path) -> None:
    # importlib.resources hands out Traversables like zipfile.Path when a
    # package (and its .js data) is installed as a zip or wheel
    import zipfile

    z = tmp_path / "pkg.zip"
    with zipfile.ZipFile(z, "w") as zf:
        for f in FIX.rglob("*"):
            if f.is_file():
                zf.write(f, "mypkg/js/" + f.relative_to(FIX).as_posix())
    _add(cm)
    names = cm.register_modules("pkg", zipfile.Path(z, "mypkg/js/"))
    assert "pkg:util/math" in names
    assert cm.run('import { calc } from "pkg:layout"; await calc(1)').value == 3


def test_register_module_content_dict_and_file(cm: CodeMode) -> None:
    cm.register_module("b:one", "export const one = 1")
    cm.register_modules("c", {"two.js": "export const two = 2", "notes.txt": "x"})
    cm.register_module_file("f", FIX / "util" / "math.js")
    r = cm.run('import { one } from "b:one"; import { two } from "c:two"; import { twice } from "f:math"; one + two + twice(1)')
    assert r.value == 5, r
    with pytest.raises(UsageError):
        cm.register_module("nocolon", "")
    with pytest.raises(UsageError):
        cm.register_module("b:one", "")


def test_module_with_unknown_op_fails_check(cm: CodeMode) -> None:
    cm.register_module("x:bad", "export const f = () => ops.nope({})")
    ds = cm.check('import { f } from "x:bad"')
    assert any("unknown operation" in d.message for d in ds), ds
