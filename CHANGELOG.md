# Changelog

## [Unreleased]

### Security

- `/convert` staging moved from the shared `/tmp/gleann-plugin-sound` to a per-user
  `<tmp>/gleann-plugin-sound-<uid>/port-<n>` (user name on Windows). The root and the port dir are
  Lstat-checked before use: a real directory, owned by the current user, no group/other write. If a check
  fails the startup sweep is skipped (logged) and uploads go to a fresh private temp dir. The sweep no longer
  follows symlinks: it deletes only regular files named `upload-*` and never recurses, so another local user
  can no longer plant a symlink that makes the plugin delete files elsewhere.

### Fixed

- Shutdown now finishes inside the host's stop grace: drain is grace minus 1 s (half of it for graces under 2 s),
  the hard exit fires at the grace instead of 2 s after the host's SIGKILL, and the refiner/engine close is
  bounded to 800 ms; a second release after an abandoned one no longer waits again.
- `GLEANN_PLUGIN_STOP_GRACE` is parsed like the host: a positive Go duration (a bare number or a value under
  1 s is no longer treated differently), default 10 s.
- A wedged refiner no longer blocks `plugin-serve` from returning: its deferred close is bounded like `release()`.
- `/health` reports `refiner: "down"` for a sidecar that died while idle (a reaper goroutine on the process
  clears the liveness flag).
- Lint: removed the unused `blockingEngine.err` test field (staticcheck U1000).

## v1.2.0 (unreleased)

### Security

- Every route except `GET /health` now requires the access token when one is configured: the operator's
  `--token` / `GLEANN_SOUND_TOKEN`, or the token gleann hands over in `GLEANN_PLUGIN_TOKEN` (gleann v1.20.0 and
  later). Either is accepted; comparison is constant-time. A non-loopback `--host` is allowed with a gleann token.
- On a loopback listener a request whose `Host` header is not a loopback name is refused with `403` (DNS rebinding).
- `/convert` rejects media longer than `--max-duration` (default `2h`, env `GLEANN_SOUND_MAX_DURATION`, `0`
  disables) with `413`, checked from the container's declared duration before decoding.
- Decoded audio is capped as well, as a backstop for files that lie about their duration: all decode sites
  (shared `core.DecodeFile`, used by the whisper, ONNX, nemotron, tc engines and the refiner path) stop ffmpeg
  once the output crosses the limit and `/convert` answers `413`. Nothing past the cap is buffered.
- `?token=` is accepted on `/live` only; every other route needs the `Authorization` header.
- The duration probe has its own 30 s timeout (a hung probe answers `422`).

### Fixed

- The ONNX Runtime auto-download uses the official NuGet package (GitHub releases of microsoft/onnxruntime were removed), sha256-verified. Intel macOS has no package and now reports a clear error.
- `gleann-plugin-sound install` refuses to touch a `plugins.json` that is not valid JSON (clear error, non-zero
  exit, file untouched) and writes the registry atomically (temp file + rename, mode kept, symlink followed).
- Without a token, `/live` refuses a WebSocket `Origin` that is not a loopback name (cross-site pages); a
  missing Origin (native clients) is still allowed.
- A hung duration probe (ffprobe or the ffmpeg fallback) now reliably answers 422; a request that ended during
  the probe answers 408.

- A request that presents a valid token is no longer subject to the loopback Host check, so a TLS reverse
  proxy (Caddy, nginx, `tailscale serve`) that forwards its public Host works. The check stays when no token
  is presented. `localhost` is matched case-insensitively and with a trailing dot.
- `Authorization: bearer <token>` (any case of the scheme) is accepted.
- `gleann-plugin-sound install` no longer deletes the `token` field (or any other field it does not know) from
  `~/.gleann/plugins.json`, neither on its own entry nor on other plugins' entries.
- An invalid `GLEANN_SOUND_MAX_DURATION` is reported under that name instead of `--max-duration`.
- The fallback plugin version for builds without a stamped version is `dev` instead of the stale `1.0.0`.

### CI

- ONNX Runtime is fetched from the official NuGet package (GitHub releases of microsoft/onnxruntime are gone), sha256-pinned.

### Docs

- README: gleann keeps one token per plugin and reuses it; how the duration limit, the decode cap and the
  30 minute request deadline interact.

### Behaviour changes

- `/info`, `/settings` and `/models` need the token when one is configured (they were open before).
- A foreign `Host` on a loopback listener gets `403` when no valid token is presented.
- Without a token, `/live` refuses a non-loopback browser `Origin` (403).
- `?token=` works only on `/live`; on any other route it is ignored and the request gets `401`.
- Decoded audio is capped at `--max-duration` (plus one second): over-long input answers `413` even when its
  container declares a shorter duration.
- With a valid token the loopback Host check no longer applies.
- The default duration cap is 2 h (4 h -> 2 h), to fit the 30 minute request deadline.
- `gleann-plugin-sound update` now treats an unstamped build (`dev`) as unknown version rather than `1.0.0`.
