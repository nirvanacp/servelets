# Servelets

Example servelets for [Genesis](https://github.com/nirvanacp/genesis) applets.

A servelet is an applet's own backend: a small Go HTTP service with a SQLite database of its own. [Let](https://github.com/nirvanacp/let) compiles it to a WebAssembly module, the applet carries the module in its bundle, and Genesis runs it on the phone. The app's records stay on the device. There is no server to host, no address, no token and no account.

An applet needs a servelet when it owns data that needs queries, search, sorting, validation or computed results, such as bookmarks filed into categories. Data the website already keeps stays on the website, and a few small documents fit in the applet's own storage without a servelet.

| Servelet | Used by | What it stores |
| --- | --- | --- |
| [`hn-bookmarks`](hn-bookmarks/README.md) | The [`hn-bookmarks`](https://github.com/nirvanacp/applets/tree/develop/hn-bookmarks) applet's `bookmarks` service | Hacker News bookmarks, categories and their order |
| [`x-bookmarks`](x-bookmarks/README.md) | The [`x-bookmarks`](https://github.com/nirvanacp/applets/tree/develop/x-bookmarks) applet's `bookmarks` service | X categories and the posts filed in them, bound to one X account |

Both replaced Node backends that ran on a computer and reached the phone through `adb` tunnels. They kept the same routes and rules, so the applets' code did not change.

## What's in a servelet

| File | What it is |
| --- | --- |
| `servelet.json` | The declaration: id, name, language, framework, runtime (`local`), engine (`wasm`) and health path |
| `main.go` | Registers the HTTP handler in `init` with `servelet.Serve`, and runs the schema migrations |
| Other `.go` files, such as `store.go` | Routes, validation and SQL |
| `checks.json` | Requests and the statuses they must answer; `let-servelet check` runs them over a throwaway database |
| `README.md` | Its routes, errors and a **Schema** table listing every migration |

## How an applet uses one

The applet declares a service in `applet.json` and points its `config` at a service file that names the servelet:

```json
{ "serviceId": "bookmarks", "runtime": "local", "servelet": "hn-bookmarks" }
```

`let-applet build` finds `servelets/<id>` next to the applets directory, builds it and puts the module in the bundle. The applet calls the service as it would call a remote one. On the phone, each servelet's database lives in Genesis's private storage and is deleted when the applet is removed or its data is cleared. Each request runs within fuel, time and memory limits.

## Commands

From a Genesis workspace, with Go 1.24 or later on `PATH`:

```sh
npx let-servelet check servelets/<id>               # build, then run checks.json
npx let-servelet run servelets/<id> --port 8790     # serve it on 127.0.0.1 for testing
npx let-servelet create servelets/<id> --id <id> --name "<name>"
```

`build` writes `dist/`, which is never committed.

## Contributing

Follow the Genesis root `AGENTS.md`, Let's servelet builder instructions in `let/factory/servelets/AGENTS.md`, and this repository's [AGENTS.md](AGENTS.md). Record every schema change in the servelet's **Schema** table with its new `servelet.Migrate` step, and never change a step that has shipped. Keep `checks.json` covering every route, and run `let-servelet check` before committing.
