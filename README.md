# Servelets

Servelets are the backends of applets: small Go programs with a SQLite database of their own, built with [Let](../let/) into WebAssembly modules that Genesis runs on the phone.

This directory holds one servelet per directory, named after its id (the `id` in its `servelet.json`).

| Servelet | Used by | What it stores |
| --- | --- | --- |
| [`hn-bookmarks`](hn-bookmarks/) | the `hn-bookmarks` applet's `bookmarks` service | Hacker News bookmarks, categories and their order |
| [`x-bookmarks`](x-bookmarks/) | the `x-bookmarks` applet's `bookmarks` service | X categories and the posts saved in them |

## How an applet uses a servelet

An applet declares a service in `applet.json` and points its `config` at a service file that names the servelet:

```json
{ "serviceId": "bookmarks", "runtime": "local", "servelet": "hn-bookmarks" }
```

`let-applet build` finds `servelets/<id>` beside the applets directory, builds it, and puts the module in the applet bundle. The applet calls the service as it would call a remote one.

## Commands

```sh
npx let-servelet check servelets/<id>      # build, then answer its checks.json
npx let-servelet run servelets/<id> --port 8790
```
