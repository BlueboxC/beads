"""Parse supplied text only. Never import, evaluate or open project code."""
import ast
import json
import sys


def dotted(node):
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute):
        base = dotted(node.value)
        return (base + "." + node.attr) if base else ""
    return ""


class Parser(ast.NodeVisitor):
    def __init__(self, path):
        self.path = path
        self.scope = ""
        self.symbols = []
        self.imports = []
        self.calls = []
        self.blocked = {}
        self.declarations = {}

    def owner(self):
        return self.path + "::" + self.scope

    def block(self, name):
        self.blocked.setdefault(self.owner(), set()).add(name)

    def definition(self, node, kind):
        parent = self.owner()
        qualified = (self.scope + "." if self.scope else "") + node.name
        identity = self.path + "::" + qualified
        self.declarations.setdefault(parent, {}).setdefault(node.name, 0)
        self.declarations[parent][node.name] += 1
        self.symbols.append({"id": identity, "name": qualified, "kind": kind,
                             "line": node.lineno, "end_line": node.end_lineno,
                             "parent": parent})
        # Defaults, decorators and bases are evaluated in the enclosing scope.
        for value in node.decorator_list:
            self.visit(value)
        if isinstance(node, ast.ClassDef):
            for value in node.bases:
                self.visit(value)
            for value in node.keywords:
                self.visit(value)
        else:
            for value in node.args.defaults + node.args.kw_defaults:
                if value is not None:
                    self.visit(value)
        previous = self.scope
        self.scope = qualified
        if not isinstance(node, ast.ClassDef):
            arguments = node.args.posonlyargs + node.args.args + node.args.kwonlyargs
            arguments += [v for v in (node.args.vararg, node.args.kwarg) if v]
            for argument in arguments:
                self.block(argument.arg)
        for value in node.body:
            self.visit(value)
        self.scope = previous

    def visit_FunctionDef(self, node):
        self.definition(node, "function")

    def visit_AsyncFunctionDef(self, node):
        self.definition(node, "async_function")

    def visit_ClassDef(self, node):
        self.definition(node, "class")

    def visit_Import(self, node):
        for name in node.names:
            self.imports.append({"owner": self.owner(), "name": name.name,
                                 "alias": name.asname or name.name.split(".")[0],
                                 "level": 0, "line": node.lineno, "from": False,
                                 "explicit_alias": name.asname is not None})

    def visit_ImportFrom(self, node):
        for name in node.names:
            self.imports.append({"owner": self.owner(), "name": node.module or "",
                                 "member": name.name, "alias": name.asname or name.name,
                                 "level": node.level, "line": node.lineno, "from": True})

    def visit_Call(self, node):
        self.calls.append({"owner": self.owner(), "name": dotted(node.func) or "<dynamic>",
                           "line": node.lineno})
        self.generic_visit(node)

    def visit_Name(self, node):
        if isinstance(node.ctx, (ast.Store, ast.Del)):
            self.block(node.id)

    def visit_Global(self, node):
        # Do not guess mutation-sensitive global/nonlocal bindings.
        for name in node.names:
            self.block(name)

    visit_Nonlocal = visit_Global

    def visit_ExceptHandler(self, node):
        if node.name:
            self.block(node.name)
        self.generic_visit(node)

    def visit_Lambda(self, node):
        # Anonymous scopes have no stable named symbol; omit their references.
        pass

    def visit_ListComp(self, node):
        pass

    visit_SetComp = visit_ListComp
    visit_DictComp = visit_ListComp
    visit_GeneratorExp = visit_ListComp

    def result(self):
        for owner, names in self.declarations.items():
            for name, count in names.items():
                if count > 1:
                    self.blocked.setdefault(owner, set()).add(name)
        return {"symbols": self.symbols, "imports": self.imports, "calls": self.calls,
                "blocked": {key: sorted(value) for key, value in self.blocked.items()}}


results = []
for source in json.load(sys.stdin):
    parser = Parser(source["path"])
    error = ""
    try:
        parser.visit(ast.parse(source["content"], filename=source["path"]))
    except (SyntaxError, ValueError, RecursionError) as exc:
        # No source excerpt: a malformed file may contain private literals.
        error = type(exc).__name__ + " at line " + str(getattr(exc, "lineno", 0))
        parser = Parser(source["path"])
    result = parser.result()
    result.update({"path": source["path"], "parse_error": error})
    results.append(result)
json.dump({"python": sys.version.split()[0], "files": results}, sys.stdout, separators=(",", ":"))
