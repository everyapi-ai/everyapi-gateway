# EveryAPI Web Gateway

A separately deployable, single-user gateway distribution based on Magpie. It connects to EveryAPI through its public API; it does not require changes to the EveryAPI backend, a desktop session, Redis, or an external database. The console configures one EveryAPI connection, its region and exposed models, plus gateway keys, budgets, sessions and request usage. It has no provider picker, Add provider flow, routing-group page or external-plugin marketplace.

## Start with Docker Compose

```sh
cp .env.everyapi.example .env.everyapi
# Optional: set EVERYAPI_API_KEY in .env.everyapi before starting.
docker compose --env-file .env.everyapi -f compose.everyapi.yaml up -d --build
docker compose --env-file .env.everyapi -f compose.everyapi.yaml exec gateway cat /var/lib/everyapi-web/admin-key
```

Open `http://127.0.0.1:8080` and enter the separate management password. It is supplied by `EVERYAPI_WEB_KEY` / `EVERYAPI_WEB_KEY_FILE`, or generated in `admin-key` when neither is supplied. After signing in, the home page opens `/settings?view=sessions`: authorize GitHub, explicitly select a project, choose the model in the input composer, describe a code change, review its diff and create a Pull Request. Open `/settings` to enter the EveryAPI API key, choose the global or China endpoint, refresh models, select exposed models, or manage gateway client keys and budgets. There is no supplier-selection step. The upstream EveryAPI key stays on the server and is never sent to the browser.

The generated `admin-key` is the management password; it is separate from both the upstream EveryAPI key and the client gateway keys.

The page and client API share port 8080. Only that port is published. The default host binding is loopback. Set `EVERYAPI_BIND_IP=0.0.0.0` for access from other machines and use an HTTPS reverse proxy for an Internet deployment. Forward the original host, scheme and client address; proxy streaming without buffering. Do not mount the operator's home or agent credentials into this container. This is one administrator's gateway, not a multi-tenant billing or account-registration service.

Read the generated initial client key when needed:

```sh
docker compose --env-file .env.everyapi -f compose.everyapi.yaml exec gateway cat /var/lib/everyapi-web/client-key
```

If you supplied `EVERYAPI_GATEWAY_KEY`, use that value instead. The file is only a bootstrap copy; after rotating or deleting the key, the Gateway page is authoritative. Restarting never recreates a deleted key.

## Native binary

The local build is `everyapi-web-0.1.0`, based on Magpie `ee715c2e`; this is not a published upstream release. If using a prepared archive, unpack it and run `./everyapi-web --data-dir /absolute/path/to/server-data --addr 127.0.0.1:8080`. The archive includes the license, these instructions and the explicit live acceptance script.

Requires Go 1.26.8 or later. No new dependencies have been added to Magpie's module.

```sh
go build -tags nogui -trimpath -ldflags '-s -w -X main.version=everyapi-web-0.1.0' -o dist/everyapi-web ./cmd/everyapi-web
./dist/everyapi-web --data-dir "$PWD/.everyapi-data" --addr 127.0.0.1:8080
```

Keep the data directory outside your source checkout in production. With no `--data-dir`, it is `everyapi-web` under the operating system's user configuration directory. The process gives itself an isolated home under that directory and clears inherited agent configuration paths. It disables subscription discovery, upstream automatic binary updates and usage telemetry. It does not alter the operator's live Claude Code, Codex or other agent configurations. Update this distribution by rebuilding or replacing its image/binary.

## iPhone and PWA

The console and Sessions include a Web App Manifest, EveryAPI home-screen icons, standalone display mode and a service worker. Open the deployed HTTPS origin in Safari, sign in, then use Share → Add to Home Screen. Open the saved icon afterward; its first launch may ask you to sign in again because the installed app can have separate browser storage. The console header's Install app button explains the Safari steps; browsers offering an installation prompt use that prompt. Pinch zoom remains available, inputs use phone-friendly text sizes, and the layout handles narrow screens, landscape and safe-area insets.

Set `EVERYAPI_PUBLIC_URL` to the reachable HTTPS origin, for example `https://gateway.everyapi.ai`. A phone cannot reach the server through `127.0.0.1`; a plain HTTP LAN address is suitable for an online layout preview but does not enable secure-context features such as the service worker. Use a certificate trusted by the phone and proxy the origin at `/`, including `/pwa/*`, `/api/*` and the client API routes. See [MDN's installation requirements](https://developer.mozilla.org/en-US/docs/Web/Progressive_web_apps/Guides/Making_PWAs_installable) and [WebKit's home-screen app behavior](https://webkit.org/blog/17333/webkit-features-in-safari-26-0/).

Offline mode stores only a public, credential-free reconnect page. Management pages, settings, request history, API responses and keys are not put into Cache Storage. Calls and console operations still need the gateway online. The launch URL is `/`, never a key-bearing sign-in link. Only the manifest, icons, worker and reconnect page are readable without a sign-in cookie; management APIs retain authentication.

## GitHub project assistant

Open `https://gateway.everyapi.ai/settings?view=sessions` after signing in to the management console. The EveryAPI project assistant can authorize GitHub, list repositories in the configured organization, clone a selected repository into the gateway's private data volume, ask EveryAPI to inspect and edit files, show the resulting diff and create a Pull Request from a new branch. The model selector is in the composer on phone and desktop; there is no separate `/chat` page.

Create a dedicated GitHub OAuth App with homepage `https://gateway.everyapi.ai` and callback `https://gateway.everyapi.ai/_github/callback`. GitHub's OAuth flow requires the callback sent by the authorization request to match a callback registered on the app; GitHub documents the same requirement for the code exchange ([GitHub OAuth documentation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)). In Sessions, an administrator can enter the Client ID, Client Secret and organization once the app exists. The gateway writes these credentials to `github-oauth.json` in the private data directory with mode `0600`, never returns the secret from its API and keeps users' GitHub access tokens in memory only. The OAuth request asks for `repo`, `read:org` and `user:email`; the account must belong to the configured organization.

The assistant refuses paths that escape the selected workspace and refuses common secret files such as `.env`, `.pem`, `.key`, `secret` and `credential` files. The Pull Request operation commits only the selected workspace's changes, pushes a timestamped `everyapi/*` branch and uses the selected repository's default branch as the PR base.

`MOBILE_PWA=1 node scripts/everyapi-web-smoke.cjs dist/everyapi-web` adds real-worker mobile acceptance to the normal disposable-server smoke. It checks Chinese and English at 320, 375, 390, 430 and 768 pixels wide plus landscape, touch navigation, EveryAPI save/discovery, login expiry with a controlling worker, Cache Storage contents and a real interrupted local connection followed by reconnect. Run it with Chromium and with `BROWSER=webkit`; browser emulation does not establish a successful installation on a physical iPhone.

## Connect a client

List available model IDs with the client gateway key. Use the exact returned ID; an EveryAPI model normally appears as `everyapi/<upstream-model-id>` and a routing group as `group/<group-id>`.

Magpie's model selection applies: with no explicit picks and more than 24 available models, the first 24 are exposed by default. Select additional models in Providers as needed.

```sh
export OPENAI_BASE_URL=http://127.0.0.1:8080/v1
export OPENAI_API_KEY='<your client gateway key>'
curl "$OPENAI_BASE_URL/models" -H "Authorization: Bearer $OPENAI_API_KEY"
```

```sh
curl "$OPENAI_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"everyapi/<upstream-model-id>","stream":true,"messages":[{"role":"user","content":"Hello"}]}'
```

The gateway also accepts `/v1/responses`, `/v1/messages` and `/v1beta/models/{model}:generateContent`. For Anthropic clients, set `ANTHROPIC_BASE_URL=http://127.0.0.1:8080` and `ANTHROPIC_API_KEY` to a client gateway key. For Gemini clients, use the gateway origin and the same client key in `x-goog-api-key`. The upstream EveryAPI key stays on the server. Administrator and upstream keys are not accepted as client keys, including on localhost.

Gateway → Connect selects an enabled client key for its copyable examples, including on localhost. Set `EVERYAPI_PUBLIC_URL` to the address clients can reach when deploying behind a reverse proxy.

## Configuration and persistence

| Setting | Default | Meaning |
| --- | --- | --- |
| `EVERYAPI_REGION` | `global` | Initial upstream: `global` → `https://api.everyapi.ai`; `cn` → `https://api-cn.everyapi.ai` |
| `EVERYAPI_API_BASE` | Region default | Initial self-hosted upstream origin or deployment prefix; a trailing `/v1` is removed before native endpoints are composed |
| `EVERYAPI_API_KEY` | Empty | Initial upstream API key; without it, enter the key directly on the EveryAPI page |
| `EVERYAPI_WEB_KEY` | Generated and persisted | Administrator sign-in key, at least 16 letters, digits or `-._~` |
| `EVERYAPI_GATEWAY_KEY` | Generated and persisted | Initial client key; 16–256 printable ASCII characters, different from the administrator/upstream keys |
| `EVERYAPI_DATA_DIR` | OS config directory / `everyapi-web` | Absolute persistent directory; `--data-dir` takes precedence |
| `EVERYAPI_WEB_ADDR` | `127.0.0.1:8080` | Web and client API address; `--addr` takes precedence; the container uses `0.0.0.0:8080` |
| `EVERYAPI_INTERNAL_ADDR` | `127.0.0.1:13425` | Private Magpie listener; use a different loopback port for multiple native instances |
| `EVERYAPI_PUBLIC_URL` | Web listener's origin; Compose uses `http://localhost:<published-port>` | Public origin shown in client connection examples; set it to your reverse proxy's HTTPS origin |
| `EVERYAPI_BIND_IP`, `EVERYAPI_PORT` | `127.0.0.1`, `8080` | Compose host port mapping |

All three key settings support a `_FILE` variant for mounted secret files. Supplying a value and a file together fails. The supplied Compose file forwards environment values; it does not mount secrets automatically. To use a secret file, add a Compose override with a `secrets` mount and set the corresponding `_FILE` variable inside the container. For example, an upstream-only override is:

```yaml
services:
  gateway:
    environment:
      EVERYAPI_API_KEY: ""
      EVERYAPI_API_KEY_FILE: /run/secrets/everyapi_upstream
    secrets:
      - everyapi_upstream
secrets:
  everyapi_upstream:
    file: /absolute/path/to/upstream-key
```

Pass both files with `-f compose.everyapi.yaml -f <your-override.yaml>`. The mounted file must be readable by container UID 65532. Generated key files have mode 0600 and keys are not printed in startup logs. The Compose sample intentionally contains no credentials. Exclude `.env.everyapi`, secret files and the data directory from version control.

Upstream and client credentials are bootstrapped only when their respective Magpie stores do not exist. An upstream provider is created only when an upstream key is supplied; region/base environment values apply to that bootstrap. Web setup selects the global or China region; an explicitly configured custom bootstrap base is retained until the region is changed. Subsequent Web edits, model selection, key rotation and key revocation survive restarts; changing bootstrap environment values is not a configuration migration. Use EveryAPI and Gateway for later changes. `EVERYAPI_WEB_KEY` remains a runtime override of the saved administrator key. An unreadable store fails startup; a provider store with a valid Magpie backup uses that backup according to Magpie's existing recovery rules.

The Docker volume contains the isolated home, provider settings, generated keys, caches and request history. Back it up as sensitive server data. `docker compose down` preserves it; `down -v` deletes it. `everyapi-web --healthcheck` checks the Web sign-in listener without bootstrapping or changing files. An occupied internal gateway port fails startup instead of silently attaching to another local gateway.

## Research and implementation decisions

Research baseline: the supplied local Magpie checkout, commit `ee715c2e` (2026-10-25). Magpie is MIT licensed; its original license and attribution are retained.

| Finding in Magpie | Decision for this distribution |
| --- | --- |
| [`internal/gui/web.go`](internal/gui/web.go) already serves the embedded management UI and client routes on one port | Reuse that Web server and its existing sign-in, same-origin guards and streaming behavior |
| [`internal/gateway`](internal/gateway) already translates Chat, Responses, Anthropic and Gemini, routes models and enforces caller budgets | Reuse its engine; enable issued-key authentication on loopback client routes for the server distribution |
| [`internal/provider/presets.go`](internal/provider/presets.go) describes keyed providers and regional native endpoints | Add an EveryAPI relay preset; bootstrap native Chat, Responses, Anthropic and Gemini URLs from the selected origin |
| [`internal/gui/gatewaymode.go`](internal/gui/gatewaymode.go) already has a server-oriented navigation mode | Open in gateway mode, with EveryAPI browser branding, without rewriting the management UI |
| Magpie can discover local subscription accounts and update its own upstream binary | Isolate the server's home, disable discovery and automatic binary updates, and deploy through a separately built image/binary |
| EveryAPI supports additional asynchronous media, realtime and account APIs beyond the four conversational protocols | This distribution does not claim complete EveryAPI API parity; billing, account management and unsupported media routes remain on EveryAPI itself |

The distribution is a Magpie-derived fork with its own entry point, not a new EveryAPI backend component. Most management screens still use Magpie's terminology, assets and translations. No Magpie subscription plugin is needed to use an EveryAPI API key.

## Verification

```sh
go test -tags nogui ./cmd/everyapi-web -count=1
go test -tags nogui ./internal/provider -run TestEveryAPIPreset -count=1
go test -tags nogui ./internal/gui -run TestWebDistributionName -count=1
go test -race -tags nogui ./cmd/everyapi-web -count=1
docker compose --env-file .env.everyapi -f compose.everyapi.yaml config --quiet
```

Tests use Magpie's isolated temporary HOME and offline transport. The relay test exercises the real gateway guards with a local upstream server: model discovery, Chat JSON/SSE, Responses, Anthropic, Gemini, upstream/client credential separation and rejection of unissued client keys from local and remote peers. Bootstrap tests cover preserved Web edits, revoked keys, secret files, invalid configuration and unreadable state. They do not make paid EveryAPI model calls.

`node scripts/everyapi-web-smoke.cjs dist/everyapi-web dist/everyapi-web-preview.png` launches the built binary under temporary data and checks blank-key startup, its real sign-in, provider configuration through the management API, branding, model discovery, connection example and authenticated Chat relay against a local upstream fixture. It requires Playwright; `PLAYWRIGHT_EXECUTABLE_PATH` can select an installed Chromium and `BROWSER=webkit` selects WebKit. The browser, gateway and temporary data are removed on exit. `internal/gui/tests/gateway-connect-keys.test.cjs` covers issued-key selection and revoked-key empty states in all five languages on Chromium and WebKit.

After building an image, `python3 scripts/everyapi-web-docker-smoke.py everyapi-web:local` checks the nonroot container with a read-only root filesystem, its healthcheck, login, model refresh and relay, then recreates it on the same volume and confirms model persistence and key revocation. It uses Docker Desktop/OrbStack's `host.docker.internal` to reach its local upstream fixture and removes its own containers and volume on exit. It makes no real EveryAPI calls.

Real EveryAPI acceptance is a separate, explicit operation:

```sh
chmod 600 /absolute/path/to/upstream-key
timeout 420 python3 scripts/everyapi-web-live.py \
  --key-file /absolute/path/to/upstream-key \
  --model '<exact upstream text-model ID>'
```

For a broader matrix, repeat `--additional-model <id>` for models checked on every selected protocol and `--chat-only-model <id>` for models checked only on Chat. `--workers 1` (the default) through `--workers 4` controls concurrency; `--report-file <path.json>` records sanitized per-model results. Each selected model/protocol sends a real request. The runner continues after individual generation failures, verifies persistence and revocation, then exits nonzero if any probe failed. It never silently retries failed probes or counts them as passes.

This uses the built native binary and a disposable data directory. It signs in, saves the upstream key through the management Web API, fetches actual upstream models, then sends five short generation requests: Chat JSON, Chat SSE, Responses, Anthropic and Gemini. The selected model must support the protocols being checked; `--protocols chat chat-stream` narrows the run explicitly, and `--region cn` selects China. There is no automatic model choice or retry. Each request asks for at most 128 output tokens; upstream pricing and model behavior determine actual charges. The script then checks client-key rotation, persistence of the saved provider/key/models, revocation across restarts and credential-safe process logs. Raw responses, keys and process logs are withheld; its child process and disposable data are removed on exit. This exercises the Web API; the separate Playwright smoke checks actual browser interaction.

When running from an unpacked binary archive, add `--binary ./everyapi-web` to the acceptance command. Docker Compose builds require the source checkout, not just the binary archive.

An exit-zero run against a loopback fixture validates the acceptance script only. A real integration result must name the upstream region, selected model and protocols actually run. Until that real run succeeds, the offline and container results establish local behavior only.
