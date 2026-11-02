# Optional camera microSD download

The bridge adapts the one-file Linux helper from
[AlexxIT/go2rtc#2637](https://github.com/AlexxIT/go2rtc/pull/2637).
RDT and camera authentication stay in go2rtc; this bridge change adds request
validation, cached-pairing selection, process/file lifecycle, and HTTP delivery.
The helper is experimental and not part of an upstream go2rtc release yet.
It does not require replacing the bridge's streaming go2rtc binary.

## Setup

Build the helper on Linux from the go2rtc PR branch:

```sh
go build -o wyze-sd-download ./examples/wyze-sd-download
```

Install it at an operator-controlled absolute path accessible to the bridge,
then set `SD_DOWNLOAD_HELPER=/usr/local/bin/wyze-sd-download`. For bare Docker,
mount the architecture-matching executable read-only at that path and pass the
environment variable. The bridge needs a writable private temporary directory
with room for one clip (up to 16 MiB). The helper is not bundled in the current
image or HA add-ons; no add-on schema/UI option is added by this draft.
Other native platforms keep running with this optional backend unavailable.

The target must already be in the bridge's camera cache, with a private IPv4
LAN address, DTLS enabled, and valid UID/ENR/MAC/model pairing metadata. The
endpoint does not refresh that cache or contact Wyze account APIs. A stale IP
or pairing record must be corrected through the bridge's existing discovery
workflow. No Wyze account password/token is passed to the helper.

The route uses the existing bridge auth middleware. Set `BRIDGE_AUTH=true`
and configure Basic credentials or `BRIDGE_API_TOKEN` before exposing this
endpoint. When bridge authentication is disabled, the route follows the same
access policy as the rest of its camera API.

```sh
curl --fail-with-body \
  -H "Authorization: Bearer $BRIDGE_API_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"date":"20260101","hour":12,"minute":34}' \
  http://127.0.0.1:5080/api/cameras/front_door/sd/recording \
  --output clip.mp4
```

Dates/times use the **camera's local recording clock**. `date` is `YYYYMMDD`;
`hour` and `minute` are integers or one/two-digit strings. Optional `new: true`
selects the camera's `_new.mp4` filename variant. Arbitrary paths, duplicate or
unknown fields, invalid calendar dates, and bodies over 1024 bytes are rejected.
Successful responses are MP4 attachments with `Cache-Control: no-store`.

## Bounded helper contract

The bridge invokes the helper directly without a shell or inherited environment:

```text
--live --credentials <private>/camera.json
--download /media/mmc/record/YYYYMMDD/HH/MM.mp4
--media-output <private>/clip.mp4 --output <private>/result.json
--max-bytes 16777216
```

Credentials contain only `ip`, `uid`, `enr`, `mac`, and `model`. The temporary
directory is private (0700) and credentials are 0600. Outputs must be private,
current-user-owned regular files; symlinks and special files are refused.
Success requires exit code zero, matching operation/path, `download_complete`,
no reported error, and a positive size matching the media file, at most 16 MiB.
Helper diagnostics and pairing metadata are never included in API errors.

Each process opens and closes a fresh authenticated camera session. The helper
has 65 seconds; the HTTP write deadline is 90 seconds for transfer plus delivery.
One bridge export slot remains held through delivery, preventing overlapping
temporary clips. Disconnect/cancellation terminates an active helper; delivery
failure also closes the file and removes scratch data. This is not an index,
archive scheduler, playback UI, or promise of sustained whole-day transfers.

| Status | Meaning |
| --- | --- |
| 400 / 413 / 415 | Invalid request / oversized request / JSON required |
| 404 | Unknown camera or recording missing |
| 405 | POST required |
| 409 | Another SD export is in progress |
| 422 | Cached camera pairing unavailable or unsupported |
| 501 | Helper disabled |
| 503 | Helper/platform unavailable |
| 504 | Helper timed out or request cancelled during transfer |
| 502 | Invalid helper result, incomplete media, or local backend failure |

## Validation scope

The Go adapter is validated offline with request/response, subprocess, private
file, locking, and failure-path tests. Exact commands/results are recorded in
the draft PR. No camera calls, cloud logins, device restarts, or new recordings
were used while adapting this fork.

Earlier bounded hardware results apply to the **same go2rtc helper/protocol**
through the original Python DWB adapter, not to this Go adapter end to end:
12/12 single-minute transfers on a Wyze Cam v3, firmware 4.36.16.6114, after an
operator restart; 6.429–8.084 seconds per roughly 6 MB clip (median 7.308).
Downloaded hashes matched earlier FFmpeg-decoded reference files, and temporary
files/locks were released. That harness used in-process HTTP dispatch and
fixture authentication, not a deployed Docker bridge. Before that restart,
the SD channel had stalled; its cause was not established. These results do
not establish other-model compatibility or indefinite transfer reliability.

Related contributions remain open:
[go2rtc#2637](https://github.com/AlexxIT/go2rtc/pull/2637) and
[original DWB#1547](https://github.com/mrlt8/docker-wyze-bridge/pull/1547).
