def _go(action, arguments):
    # A temporary modfile keeps go.mod/go.sum untouched. Disable workspace
    # mode to avoid go.work.sum updates; these checks operate on one module.
    environment = {"GOWORK": "off"}
    module = privileged.shell("go", "env", "GOMOD", env=environment).run()
    if not module.ok:
        return module
    mod = module.stdout.strip()
    tmp = privileged.shell("mktemp", "-d").output().strip()
    privileged.shell("cp", mod, tmp + "/check.mod").output()
    checksum = mod.removesuffix(".mod") + ".sum"
    if privileged.shell("test", "-f", checksum).run().ok:
        privileged.shell("cp", checksum, tmp + "/check.sum").output()
    result = privileged.shell("go", action, "-mod=readonly", "-modfile=" + tmp + "/check.mod", env=environment, *arguments).run()
    privileged.shell("rm", "-rf", "--", tmp).output()
    return result

def build_check(pattern):
    """build_check(pattern: str) -> struct(ok, exit_code, stdout, stderr): Check a Go module build without writing workspace files. Failed builds return ok=False; missing tools raise."""
    return _go("build", ["-o", "/dev/null", "--", pattern])

def format_check(pattern):
    """format_check(pattern: str) -> struct(ok, exit_code, stdout, stderr): Check Go module package sources, including tests and build-tagged files. stdout lists unformatted files; ok=False on check failure. No files are changed; missing tools raise."""
    template = "".join([
        '{{range .' + field + '}}{{$.Dir}}/{{.}}{{"\\n"}}{{end}}'
        for field in ["GoFiles", "CgoFiles", "TestGoFiles", "XTestGoFiles", "IgnoredGoFiles"]
    ])
    files = _go("list", ["-f", template, "--", pattern])
    if not files.ok:
        return files
    paths = [line for line in files.stdout.splitlines() if line]
    if not paths:
        return struct(ok=True, exit_code=0, stdout="", stderr=files.stderr)
    checked = privileged.shell("gofmt", "-l", "--", *paths).run()
    if not checked.ok:
        return checked
    return struct(ok=not checked.stdout, exit_code=1 if checked.stdout else 0, stdout=checked.stdout, stderr=checked.stderr)
