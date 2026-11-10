# Installing nex-cua on Linux

> DEPRECATED. The Python MCP server that lived in this repo is **removed**. The
> canonical CUA MCP tool is now the TypeScript package **@nex/nex-cua**
> (npm). This document described the old `uvx --from nex-cua[linux]@<wheel>
> nex-cua` install path plus the `scripts/nex-cua-linux` distro-`pyatspi`
> launcher — all of which targeted the removed Python server and no longer apply.

Install the canonical tool from npm:

```bash
npm install @nex/nex-cua
# or, without installing:
npx @nex/nex-cua
```

Source and the Linux/AT-SPI backend live in the z-code repo:
[git.example.invalid/codegeex/z-code](https://git.example.invalid/codegeex/z-code)
→ package [@nex/nex-cua](https://www.npmjs.com/package/@nex/nex-cua).

What this repo still hosts (the real-model act-verify harness, the canonical
skill, screenshot-pipeline parity fixtures) is described in the top-level
`README.md`. The earlier Python-server Linux install guide is preserved in the
git history of this repo (see the commit prior to `refactor/deprecate-python`).

For background on the TypeScript rewrite and this deprecation, see the Feishu
design doc:
[BE9UdI4zMo4HjRx4659cTHOnn9f](https://internal-docs.example.invalid/redacted).
