## What's Changed in v3.0.6

- Revert MediaMTX to 1.11.3 because 1.12 doesn't work here.

## What's Changed in v3.0.5 ~DELETED~

- Fix MediaMTX to pass a user name [since 1.12.0 now requires one](https://github.com/bluenviron/mediamtx/compare/v1.11.3...v1.12.0#diff-b5c575fc54691bae05c5cc598fac91c97876b3d15687c359f970a8b832ab3ab6R23-R41)

## What's Changed in v3.0.4  ~DELETED~

- Chore: Bump [MediaMTX to 1.12.0](https://github.com/bluenviron/mediamtx/releases/tag/v1.12.0)

## What's Changed in v3.0.3

Rehoming this to ensure it lives on since PR merges have stalled in the original (and most excellent) @mrlt8 repo, I am surfacing a new 
release with the PRs I know work. **Note** The badges on the GitHub repo may be broken and the donation links _still_ go to @mrlt8 (as they should!)

- Chore: Bump Flask to 3.1.*
- Chore: Bump Pydantic to 2.11.*
- Chore: Bump Python-dotenv to 1.1.*
- Chore: Bump MediaMTX to 1.11.3
- FIX: Add host_network: true for use in home assistant by @jdeath to allow communications in Docker
- FIX: Hardware accelerated rotation by @giorgi1324
- Enhancement: Add more details to the cams.m3u8 endpoint by @IDisposable
- FIX: Fix mixed case when URI_MAC=true by @unlifelike
- Update: Update Homebridge-Camera-FFMpeg documentation link by @donavanbecker
- FIX: Add formatting of {cam_name} and {img} to webhooks.py by @traviswparker which was lost
- Chore: Adjust everything for move to my GitHub repo and Docker Hub account

## What's Changed in v2.10.3

- FIX: Increased `MTX_WRITEQUEUESIZE` to prevent issues with higher bitrates.
- FIX: Restart RTMP livestream on fail (#1333)
- FIX: Restore user data on bridge restart (#1334)
- NEW: `SNAPSHOT_KEEP` Option to delete old snapshots when saving snapshots with a timelapse-like custom format with `SNAPSHOT_FORMAT`. (#1330)
  - Example for 3 min: `SNAPSHOT_KEEP=180`, `SNAPSHOT_KEEP=180s`, `SNAPSHOT_KEEP=3m`
  - Example for 3 days: `SNAPSHOT_KEEP=72h`, `SNAPSHOT_KEEP=3d`
  - Example for 3 weeks: `SNAPSHOT_KEEP=21d`, `SNAPSHOT_KEEP=3w`
- NEW: `RESTREAMIO` option for livestreaming via [restream.io](https://restream.io). (#1333)
  - Example `RESTREAMIO_FRONT_DOOR=re_My_Custom_Key123`

## What's Changed in v2.10.2

- FIX: day/night FPS slowdown for V4 cameras (#1287) Thanks @cdoolin and @Answer-1!
- NEW: Update battery level in WebUI

## What's Changed in v2.10.0/v2.10.1

FIXED: Could not disable `WB_AUTH` if `WB_API` is set. Thanks @bengthu! (#1304)

### WebUI Authentication

Simplify default credentials for the WebUI:

  - This will not affect users who are setting their own `WB_PASSWORD` and `WB_API`.
  - Default `WB_PASSWORD` will now be derived from the username part of the Wyze email address instead of using a randomly generated password.
    - Example: For the email address `john123@doe.com`, the `WB_PASSWORD` will be `john123`.
  - Default `WB_API` will be based on the wyze account for persistance.

### Stream Authentication

NEW: `STREAM_AUTH` option to specify multiple users and paths:

  - Username and password should be separated by a `:` 
  - An additional `:` can be used to specify the allowed IP address for the user. 
    - **This does NOT work with docker desktop**
    - Specify multiple IPs using a comma
  - Use the `@` to specify paths accessible to the user. 
    - Paths are optional for each user.  
    - Multiple paths can be specified by using a comma. If none are provided, the user will have access to all paths/streams 
  - Multiple users can be specified by using  `|` as a separator 

  **EXAMPLE**:

  ```
  STREAM_AUTH=user:pass@cam-1,other-cam|second-user:password@just-one-cam|user3:pass
  ```

  - `user:pass`  has access to `cam-1` and `other-cam`
  - `second-user:password` has access to `just-one-cam`
  - `user3:pass` has access to **all** paths/cameras

  See [Wiki](https://github.com/mrlt8/docker-wyze-bridge/wiki/Authentication#custom-stream-auth) for more information and examples.

### Recording via MediaMTX

Recoding streams has been updated to use MediaMTX with the option to delete older clips. 

Use `RECORD_ALL` or `RECORD_CAM_NAME` to enable recording.

- `RECORD_PATH` Available variables are `%path` or `{cam_name}`, `%Y` `%m` `%d` `%H` `%M` `%S` `%f` `%s` (time in strftime format).
- `RECORD_LENGTH` Length of each clip. Use `s` for seconds , `h` for hours. Defaults to `60s`
- `RECORD_KEEP` Delete older clips. Use `s` for seconds , `h` for hours. Set to 0s to disable automatic deletion. Defaults to `0s`

## What's Changed in v2.9.11/12

- FIX: Fix regression introduced in v2.9.11 which caused connection issues for WYZEDB3, WVOD1, HL_WCO2, and WYZEC1 (#1294) 
- FIX: Update stream state on startup to prevent multiple connections.
- FIX: No audio on HW and QSV builds. (#1281)
- Use k10056 if supported and not setting fps when updating resolution and bitrate (#1194)
- Temporary fix: Don't check bitrate on newer firmware which do not seem to report the actual bitrate. (#1194)

## What's Changed in v2.9.10

- FIX: `-20021` error when sending multiple ioctl commands to the camera.
- FIX: Regression introduced in v2.9.9 where the WebRTC/HLS icon in WebUI was missing.
- Reduced memory usage slightly.
- NEW: Option to use pre-hashed passwords (#1275):
  - You must md5 hash your password three times and prefix it with `hashed:`
  - Example: `WYZE_PASSWORD=hashed:<your-tripple-hashed-password>`
- NEW: REST/MQTT commands (#1274):
  - `notifications` GET/SET wyze app push notifications on/off (CLOUD).
  - `motion_detection` GET/SET motion detection on/off (LOCAL).


## What's Changed in v2.9.9

- FIX: Regression introduced in v2.9.8 where a pipe blocking issue would cause CPU to spike (#1268) (#1270)
- Tweak HLS latency and buffer.

## What's Changed in v2.9.8

KNOWN BUG: stream path may become unresponsive after stopping when ON_DEMAND is enabled until the onDemand timeout clears (60s).

- FIX: restart options in the WebUI
- FIX: Resume HLS/WebRTC on recover and play on first click in WebUI.
- NEW: Add 'reload cameras' option to refresh camera data without clearing all data (#1255)
- CHANGED: Use hls-js for HLS in WebUI.

## What's Changed in v2.9.7

- FIX: Pan and tilt cruise points 3 and 4 were broken. Thanks @Deach01! (#1228)
- FIX: Remove whitespaces from credentials (#1252)
- CHANGED: Removed `blank` option when setting `cruise_points` as it would be ignored anyways.


## What's Changed in v2.9.6

- FIX: Connection to camera would get stuck and not come back on it's own until the webui was opened. Thanks @vipergts450 and @g13092! (#1234) (#1240)
- FIX: Regression introduced in v2.9.5 where AAC audio sources would not work (#1241) Thanks @rspierenburg!
- Home Assistant FIX: Regression introduced in v2.9.5 where MQTT was not setting up automatically. (#1247)
- Home Assistant FIX: check if path exists when migrating HA config (#1242)
- Home Assistant NEW: Disable MQTT by setting MQTT to `false` (#1232)
- NEW: Ability to read credentials from Docker Secrets. Thanks @cliaz! (#1244)
  - Supported variables: `WYZE_EMAIL`, `WYZE_PASSWORD`, `API_ID`,`API_KEY`, `WB_USERNAME`, `WB_PASSWORD`, and `WB_API`


## What's Changed in v2.9.5

- **POTENTIALLY BREAKING**: The bridge will now use **PCMU/8000** as the default audio codec when the camera does not provide an RTSP/WebRTC-compatible audio format. This change should enhance compatibility with various NVR systems like **Surveillance Station** which do not support opus. Thanks @Dot50Cal!
  - To use a different audio codec, set the desired codec in the `AUDIO_CODEC` environment variable.
- Always re-encode `aac_eld` (Wyze Cam v4) even when WebRTC is not enabled (#1236) Thanks @Dot50Cal!
- HOME ASSISTANT: Disable MQTT from automatically setting up by setting `MQTT_DTOPIC` to something other than `homeassistant` (#1232)

## What's Changed in v2.9.4

- Adjust AV sync issue/delay when audio is enabled. (#1231) Thanks @delmlund!

## What's Changed in v2.9.3

- FIX: Clear the retain flag from MQTT Discovery which was causing commands to be resent to the bridge on startup for some users. (#1182)
- Ignore commands when connection is stopping.

## What's Changed in v2.9.2

- Improved video connection stability and audio sync.  #1175 #1196 #1194 #1193 #1186 Thanks @vipergts450!
- FIX: Remove quotes from credentials #1158
- NEW: `FORCE_FPS` option for all cameras #1161
- Home Assistant: Add `FORCE_FPS` option #1161
- Home Assistant: Ignore whitespaces in api key/id #1188 Thanks @richh1! 

## What's Changed in v2.9.1

- FIX: Setting bitrate higher than 255 would not report correctly (#1185) Thanks @Anc0dia!
- FIX: Wrong bitrate for HL_CFL2 (#1112) Thanks @dreondre!
- FIX: Could not set values with the REST API when `WB_AUTH` is enabled.(#1189) Thanks @kiwi-cam!
- NEW: `api` header authentication option for the RES API when `WB_AUTH` is enabled:
  - `-H "api: MyWbApiKey"`
 
## What's Changed in v2.9.0

> [!IMPORTANT] 
> WebUI and stream authentication will be enabled by default to prevent unintentional access.

**Default Authentication**

  - To disable default authentication, set `WB_AUTH=False` explicitly.
  - Note that all streams and the REST API will necessitate authentication when `WB_AUTH` is enabled.

**WebUI Authentication**

- If `WB_USERNAME` and `WB_PASSWORD` are not set, the system will try to use `WYZE_EMAIL` and `WYZE_PASSWORD`.
- In case neither sets of credentials are provided, the username will default to `wbadmin` with a randomly generated `WB_PASSWORD`, which will be logged and stored in a `wb_password` file within the tokens directory.
- Credentials are case sensitive.

**Stream and REST API Authentication**
- A unique API key will be accessible at the bottom of your WebUI and saved to a `wb_api` file in your tokens directory.
  - For persistence, ensure to set the `WB_API` environment variable or volume mount the `/tokens` directory.
- REST API will require an `api` query parameter. 
  - Example:  `http://localhost:5000/api/<camera-name>/state?api=<your-wb-api-key>`
- Streams will also require authentication.
  - username: `wb`
  - password: your unique wb api key

**FIXES**
- Wrong file permission caused errors for non-root. (#1174) Thanks @GiZZoR!
- Fix `MOTION_API` when substreams were enabled. (#1125) Thanks @kiwi-cam!
- Changing FPS and `FORCE_FPS` were broken (#1161) Thanks @jarrah31!
- Dropped frame issue when camera is falling behind. (#1167) Thanks @34t614t1254y!

**NEW**
- Token based wyze authentication from WebUI. See [wiki](https://github.com/mrlt8/docker-wyze-bridge/wiki/Authentication#token-based-authentication).
- Remove 255 limit from `QUALITY`. Can now go as high as your network can handle. e.g. `- QUALITY=HD8000` 
- Update snapshot with `MOTION_API` and push to mqtt (#709) (#970)
- Additional headers for `MOTION_WEBHOOKS`.
- `OFFLINE_WEBHOOKS` will send a POST request when the bridge cannot connect to a camera because it is offline. Replaces `ifttt_webhook`.

**POTENTIALLY BREAKING**
- CHANGES: `MOTION_WEBHOOKS` now makes a POST request instead of a GET request.
- CHANGES: `MOTION_WEBHOOKS` includes the event timestamp in the message body which may require you to adjust the timezone for your container with the `TZ` environment.
- REMOVED: `ifttt_webhook` as webhooks are no longer free with IFTTT.
- CHANGED: Renamed WebUI authentication related ENV options:
  - `WEB_AUTH` -> `WB_AUTH`
  - `WEB_USERNAME` -> `WB_USERNAME`
  - `WEB_PASSWORD` -> `WB_PASSWORD`

**HOME ASSISTANT**
- Login with API Key/ID or existing token via Ingress/WebUI.
- Config now uses yaml instead of json.
- Credentials are now optional to allow for WebUI based login, but it is still recommended to set them under advanced options.


## What's Changed in v2.8.2/3

* Add support for developer API Key/ID for WebUI based logins.
* Update Home Assistant and unraid config to support API Key/ID
* Refactor to catch additional WyzeAPIErrors.


## What's Changed in v2.8.1

* Fix video lag introduced in v2.7.0
* Add aac_eld audio support for V4 cams (HL_CAM4).
* Add 2k resolution support for Floodlight V2 cams (HL_CFL2).
* Fix version number

Home Assistant:

* Add dev and previous builds (v2.6.0) to the repo.
* Note: you may need to re-add the repo if you cannot see the latest updates.

## What's Changed in v2.7.0

* Audio sync - bridge will now try to make minor adjustments to try to keep the video and audio in sync Thanks @carlosnasillo and everyone who helped with testing! (#388).
* Refactor for compatibility with Scrypted. Thanks @koush (#1066)
* Use K10050GetVideoParam for FW 4.50.4.x (#1070)
* Fix jittery video in Firefox (#1025)
* Retain MQTT Discovery Message Thanks @jhansche! (#920) 

Home Assistant:

* Now uses `addon_config` instead of `config` [Additional info](https://developers.home-assistant.io/blog/2023/11/06/public-addon-config/) 
  * May need to cleanup old config manually.
* Reset alarm/siren state (#953) (#1051)


## What's Changed in v2.6.0

* **NEW**: ARM 64-bit native library (#529 #604 #664 #871 #998 #1004)
  
  The arm64 container now runs in 64-bit mode, addressing compatibility issues, particularly on Apple Silicon M1/M2/M3, when using the Home Assistant Add-on.

  Resolves issues on the Raspberry Pi 4/5 running the 64-bit version of Raspbian.

* **Update**: Python 3.11 -> Python 3.12


## What's Changed in v2.5.3

* FIXED: use static bulma for Pi-Hole compatibility Thanks @MetalliMyers! #1054
* NEW: MQTT/API - Format SD Card using the topic/endpoint `format_sd` Thanks @iferlive! #1053
* NEW: `MQTT_RETRIES` to adjust the number of retires on exception. Defaults to 3 before disabling MQTT. Thanks @rmaes4! #1047

## What's Changed in v2.5.2

* FIX: MQTT Naming Warning in Home Assistant #1046 Thanks @ejpenney!
* NEW: `{img}` variable for `motion_webhooks` #1044
  * e.g., `MOTION_WEBHOOKS: http://0.0.0.0:123/webhooks/endpoint?camera={cam_name}&snapshot={img}`

## What's Changed in v2.5.1

* FIX `ON_DEMAND=False` option was broken in v2.5.0 #1036 #1037
* NEW API/MQTT commands Thanks @ralacher! #921:
  * GET: `/api/<cam-name>/accessories` | MQTT: `wyzebridge/<cam-name>/accessories/get`
  * SET: `/api/<cam-name>/spotlight` | MQTT: `wyzebridge/<cam-name>/spotlight/set`

## What's Changed in v2.5.0

* NEW camera support:
  * HL_DB2: Wyze Cam Doorbell v2 - thanks @hoveeman!
  * HL_CAM4: Wyze Cam V4
* NEW API Endpoint:
  * `/api/all/update_snapshot` - trigger interval snapshots via web API #1030


## What's Changed in v2.4.0

* Motion Events! 
  * Pulls events from the wyze API, so it doesn't require an active connection to the camera to detect motion - great for battery cams.
  * Motion status and timestamp available via MQTT and REST API:
    * MQTT topics: `wyzebridge/{cam-name}/motion` or `wyzebridge/{cam-name}/motion_ts`
    * REST endpoint: `/api/{cam-name}/motion` or `/api/{cam-name}/motion_ts`
  * Webhooks ready and works with ntfy.sh `triggers`. 
  * See [Camera Motion wiki](https://github.com/mrlt8/docker-wyze-bridge/wiki/Camera-Motion) for more information.
* Other fixes and changes:
  * Potential improvements for audio sync. Audio will still lag on frame drops. (#388)
    * Using **wallclock** seems to help in some situations: 
    `- FFMPEG_FLAGS=-use_wallclock_as_timestamps 1`
  * UPDATE FFmpeg to [v6.0](https://github.com/homebridge/ffmpeg-for-homebridge/releases/tag/v2.1.0) 
  * UPDATE MediaMTX version from v1.0.3 to v1.1.0 (#1002)
  * Store and reuse s3 thumbnail from events to reduce calls to the wyze api (#970)
  * Increase default `MTX_WRITEQUEUESIZE` (#984)
  * keep stream alive if livestream enabled (#985)
  * Catch RuntimeError if libseccomp2 is missing (#994)
  * Refactored API client to better handle exceptions and limit connections.
  * Check bitrate from videoParams for all 11.x or newer firmware (#975)
  * buffer mtx event data (#990)
  * Exclude battery cams from scheduled RTSP snapshots (#970)
* New ENV/Options:
  * `MOTION_API=True`  to enable motion events. (Default: False)
  * `MOTION_INT=<float>` number of seconds between motion checks. (Default: 1.5) 
  * `MOTION_START=True` to have the bridge initiate a connection to the camera on a motion event. (Default: False)
  * `MOTION_WEBHOOK=<str>` webhooks url. Can use `{cam_name}` in the url to make a request to a url with the camera name. Image url and title are available in the request header.
  * `MOTION_WEBHOOK_<CAM-NAME>=<str>` Same as `MOTION_WEBHOOK` but for a specific camera. 

## What's Changed in v2.3.17

* NEW REST/MQTT Commands:
  * `battery` GET current battery level on outdoor cams. (#864)
  * `battery_usage` GET current battery usage times. (#864)
  * `hor_flip` GET/SET horizontal video flip. (#976)
  * `ver_flip` GET/SET vertical video flip. (#976)
* FIXES:
  * Wrong bitrate error on newer `4.36.11.x` firmware which were not returning the correct bitrate info. (#975)
  * Typo in `quick_response` REST/MQTT topic.
  * invalid escape sequence warning.
* UPDATES:
  * MediaMTX version from v1.0.0 to v1.0.3 (#979)


## What's Changed in v2.3.16

* FIX: Catch exception in thread errors 
* FIX: Other minor typos and typing errors.
* UPDATE: Wyze App version to v2.44.1.1 (#946)

## What's Changed in v2.3.15

* FIX: `caminfo` not found error.
* Update MediaMTX version from v0.23.8 to v1.0.0 (#956)

## What's Changed in v2.3.14

NEW: 
* PTZ controls in MQTT discovery as "cover"
* Add ffmpeg `filter_complex` config (#919)


CHANGED:
* Adjust default bitrate for re-encoding to 3000k.
* Case sensitive FFMPEG_CMD (#736) Thanks @392media!
* `DEBUG_FFMPEG` is now `FFMPEG_LOGLEVEL` with customizable levels:
  * `quiet`, `panic`, `fatal`, `error`, `warning`, `info`, `verbose`, `debug`.
  * Defaults to `fatal`.
* Bump Wyze App version to v2.44.1.1 (#946)

## What's Changed in v2.3.13

FIXES:
  * Errors when SET/GET `bitrate`. Thanks @plat2on1! (#929)
  * Prevent exception on empty GET/SET payload.

## What's Changed in v2.3.12

* NEW:
  * `update_snapshot` MQTT/REST API GET topic.
  * Additional MQTT entities (#921)
* FIXES:
  * Monitor and set preferred bitrate if/when the wyze app changes it. Thanks @plat2on1! (#929)
  * `cruise_point` index starts at 1 when setting via MQTT/REST API. (#835)
  * Camera status was always online. (#907) (#920)
  * Power status was incorrect when using MQTT discovery. (#921)
  
## What's Changed in v2.3.11

* NEW:
  * Add more MQTT entities when using MQTT discovery. Thanks @jhansche! #921 #922
  * custom video filter - Use `FFMPEG_FILTER` or `FFMPEG_FILTER_CAM-NAME` to set custom ffmpeg video filters. #919
* NEW MQTT/REST commands:
  * **SET** topic: `cruise_point` | payload: (int) 1-4 - Pan to predefined cruise_point/waypoint. Thanks @jhansche! (#835).
  * **SET** topic: `time_zone` | payload: (str) `Area/Location`, e.g. `America/New_York` - Change camera timezone. Thanks @DennisGarvey! (#916)
  * **GET/SET** topic: `osd_timestamp` | payload: (bool/int) `on/off` - toggle timestamp on video.
  * **GET/SET** topic: `osd_logo` | payload: (bool/int) `on/off` - toggle wyze logo on video.
  * **SET** topic: `quick_reponse` | payload: (int) 1-3 -  Doorbell quick response.
* FIXES:
  * Resend discovery message on HA online. Thanks @jhansche! #907 #920
  * Return json response/value for commands. Thanks @jhansche! #835
  * Fix threading issue on restart. Thanks @ZacTyAdams! #902
  * Catch and disable MQTT on name resolution error.
  * Fix SET cruise_points over MQTT.
* Updates:
  * Wyze iOS App version from v2.43.0.12 to v2.43.5.3 (#914)
  * MediaMTX version from v0.23.7 to v0.23.8 (#925)

## What's Changed in v2.3.10

* FIX: KeyError when upgrading with old cache data in v2.3.9 (#905) Thanks @itsamenathan!
  * You should be able to remove or set `FRESH_DATA` back to false.
* MQTT: Update bridge status (#907) Thanks @giorgi1324!

## What's Changed in v2.3.9

* NEW: ENV Options - token-based authentication (#876)
  * `REFRESH_TOKEN` - Use a valid refresh token to request a *new* access token and refresh token.
  * `ACCESS_TOKEN` - Use an existing valid access token too access the API. Will *not* be able to refresh the token once it expires.
* NEW: Docker "QSV" Images with basic support for QSV hardware accelerated encoding. (#736) Thanks @mitchross, @392media, @chris001, and everyone who helped!
  * Use the `latest-qsv` tag (e.g., `idisposablegithub365/wyze-bridge:latest-qsv`) along with the `H264_ENC=h264_qsv` ENV variable. 
* FIXES:
  * Home Assistant: set max bitrate quality to 255 (#893) Thanks @gtxaspec!
  * WebUI: email 2FA support.
* UPDATES:
  * Docker base image: bullseye -> bookworm
  * MediaMTX: v0.23.6 -> v0.23.7
  * Wyze App: v2.42.6.1 -> v2.43.0.12

## What's Changed in v2.3.8

* FIX: Home Assistant - `API_KEY` and `API_ID` config for wyze API was broken. (#837)
* FIX: Prioritize sms > totp > email for MFA if no MFA_TYPE or primary option is set. (#885)
* Potential fix: Add libx11 to qsv image.

## What's Changed in v2.3.7

* FIX: Regression introduced in v2.3.6 if primary_option for MFA is "Unknown". Will now default to sms or totp if MFA_TYPE is not set. Thanks @Dot50Cal! (#885)
* FIX: Reduce excess logging if rtsp snapshot times out.

## What's Changed in v2.3.6

* NEW: Add support for email 2FA (#880) Thanks @foobarmeow!
* NEW: ENV Option `MFA_TYPE` - allows you to specify a verification type to use when an account has multiple options enabled. Will default to the primary option from the app if not set. Valid options are:
  * `TotpVerificationCode`
  * `PrimaryPhone`
  * `Email`

## What's Changed in v2.3.5

* FIXED: response code and error handling for Wyze Web API.
* FIXED: catch exceptions when taking a snapshot (#873)
* CHANGED: MediaMTX versions are now pinned to avoid breaking changes.
* UPDATED: MediaMTX to 0.23.6 and fixed `MTX_PATH`.

## What's Changed in v2.3.4

* ENV Options:
  * FIX: `FILTER_NAMES` would not work if camera had spaces at the end of the name. Thanks @djak250! (#868)
* Camera Commands:
  * FIX: Regression introduced in v2.3.3 preventing negative values for the `rotary_degree` topic. Thanks @gtxaspec! (#870) (#866)
  * FIX: String cmd lookup for `rotary_degree` and json error response breaking web api. #870 #866
* Other Fixes:
  * Catch exceptions when saving thumbnail from api. (#869)
  * Clear cache on UnpicklingError. (#867)

## What's Changed in v2.3.3

* ENV Option:
  * NEW: Add `SUB_RECORD` config. Thanks @gtxaspec! (#861)
  * FIX: Home Assistant `SUB_QUALITY`
* MQTT:
  * NEW: Update more camera parameters on connect.
* Camera Commands:
  * NEW: Add GET topics for camera params.
  * FIX: Persist bitrate changes on-reconnect (#852)
  * FIX: Limited vertical angle for `ptz_position`. Thanks @Rijswijker! (#862)

## What's Changed in v2.3.2

* Camera commands:
  * SET Topic: `state`; payload: `start|stop|enable|disable` - control the camera stream.
  * GET Topic: `state` - get the state of the stream in the bridge.
  * GET Topic: `power` - get the power switch state (Wyze Cloud API).
* REST/MQTT Control:
  * FIXED: Refresh token if needed when sending `power` commands.
  * FIXED: Remove quotations from payload. (#857)
  * CHANGED: Better error handling for commands.
* MQTT:
  * Updated discovery availability and additional entities.
  * Publish additional topics with current settings.
  * Disable on TimeoutError.
  
## What's Changed in v2.3.1

* NEW: WebUI - Power on/off/restart controls.
  * As these commands are sent over Wyze's Cloud API, the cameras will need access to the wyze servers.
  * These commands also suffer from the same "offline" issue as the app, and will give an error if the camera is reporting offline in the app.
* NEW: Camera commands:
  * Topic: `power`; payload: `on|off|restart` Sent over Wyze Cloud API. (#845) (#841)
  * Topic: `bitrate`; payload: `1-255` Change the video bitrate/quality (#852)
* NEW: Camera specific sub_quality option (#851)
  * Docker: use `SUB_QUALITY_NAME=SD60`
  * Home Assistant: use `SUB_QUALITY: SD60` in [Camera Specific Options](https://github.com/mrlt8/docker-wyze-bridge/wiki/Home-Assistant#camera-specific-options).
* NEW: Home Assistant - add config for 8554/udp (#855)

## What's Changed in v2.3.0

* NEW: Optional `API_KEY` and `API_ID` config for wyze API (#837)
  * Key/ID are optional and the bridge will continue to function without them.
  * `WYZE_EMAIL` and `WYZE_PASSWORD` are still required, but using API key/ID will allow you to skip 2FA without disabling it.
  * Key/ID are tied to a single account, so you will need to generate a new set for each account.
  * See: https://support.wyze.com/hc/en-us/articles/16129834216731
* NEW: Camera commands (#835)
  * GET/SET `cruise_points` for Pan cams. See [cruise_points](https://github.com/mrlt8/docker-wyze-bridge/wiki/Camera-Commands#cruise_points)
  * GET/SET `ptz_position` for Pan cams. See [ptz_position](https://github.com/mrlt8/docker-wyze-bridge/wiki/Camera-Commands#ptz_position)

## What's Changed in v2.2.4

* NEW: Add Wyze credentials via WebUI.
  * This does not replace the old method, but is just an alternate way to pass your wyze credentials to the container.
  * This should hopefully resolve the issue some users were facing when they had special characters in their docker-compose.
  * `WYZE_EMAIL` and `WYZE_PASSWORD` are no longer required to start the bridge. #807
* FIXED: Log wording when filtering is enabled. Thanks @cturra
* UPDATED: MediaMTX to v0.23.5

## What's Changed in v2.2.3

* NEW: `LOG_TIME` config to add timestamps to the logs. #830
* CHANGED: `DEBUG_LEVEL` is now `LOG_LEVEL`
* FIXED: `DEBUG_LEVEL`/`LOG_LEVEL` and `LOG_FILE` were broken in Home Assistant. #830
  * `LOG_FILE` now logs to `/config/wyze-bridge/logs/`
  
## What's Changed in v2.2.2

* FIXED: `autoplay` URL parameter was being ignored - Thanks @stere0123! #826
* NEW: Fullscreen mode on web-ui enables autoplay.
* Disabled `LD_CFP` "Floodlight Pro" because it doesn't use tutk - Thanks @cryptosmasher! #822
  * This seems to be slightly different than the Gwell cameras (OG/Doorbell Pro). Needs further investigation. 
* UPDATED: MediaMTX to [v0.23.4](https://github.com/bluenviron/mediamtx/releases/tag/v0.23.4).

## What's Changed in v2.2.1

* FIXED: topic to set `motion_tracking` Thanks @crslen! #823
* FIXED: additional cam info was missing from web-ui.
* NEW: outdoor cam related tutk commands and `battery` topic for API.

## What's Changed in v2.2.0

⚠️ Breaking changes for MQTT/REST API 

See [wiki](https://github.com/mrlt8/docker-wyze-bridge/wiki/Camera-Commands) for details.

* CHANGED: API commands are now split into topics and payload values for more flexibility.
* NEW: API commands will initiate connection if not connected.
* NEW: json payload for API commands.
* NEW: `PUT`/`POST` methods for REST API.
* NEW: MQTT topics for each get and set command.
* NEW: MQTT value gets updated on set command.
* NEW: start/stop/enable/disable over MQTT.
* FIXED: camera busy on re-connect.

## What's Changed in v2.1.8

* NEW: Camera Commands
  * `set_pan_cruise_on`/ `set_pan_cruise_off`  - Enables or disables the Pan Scan ("Cruise") behavior, where the camera cycles through configured waypoints every 10 seconds. Thanks @jhansche
  * `set_motion_tracking_on`/`set_motion_tracking_off`/`get_motion_tracking` - Follow detected motion events on Pan Cams. Thanks @jhansche
* NEW: ENV Option
  * `ROTATE_IMG_CAM_NAME=<true|0|1|2|3>` - Rotate snapshots for a single camera. #804
* UPDATE: MediaMTX to v0.23.3
* UPDATE: WebRTC offer to use SDP for compatibility with MTX v0.23.3

## What's Changed in v2.1.7

* FIX: WebRTC not loading in the WebUI.
* UPDATE: MediaMTX to v0.23.2

## What's Changed in v2.1.6

* UPDATE: MediaMTX to v0.23.0
* FIXED: Error reading some events.
* FIXED: Restart MediaMTX on exit and kill flask on cleanup which could prevent the bridge from restarting.

## What's Changed in v2.1.5

* FIX: set_alarm_on/set_alarm_off was inverted #795. Thanks @iferlive!
* NEW: `URI_MAC=true` to append last 4 characters of the MAC address to the URI to avoid conflicting URIs when multiple cameras share the same name. #760
* Home Assistant: Add RECORD_FILE_NAME option #791
* UPDATE: base image to bullseye.

## What's Changed in v2.1.4

* FIX: Record option would not auto-connect. #784 Thanks @JA16122000!

## What's Changed in v2.1.2/3

* Increase close on-demand time to 60s to prevent reconnect messages. #643 #750 #764
* Disable default LL-HLS for compatibility with apple. LL-HLS can still be enabled with `LLHLS=true` which will generate the necessary SSL certificates to work on Apple devices.
* Disable MQTT if connection refused.
* UPDATED: MediaMTX to [v0.22.2](https://github.com/aler9/mediamtx/releases/tag/v0.22.2)

## What's Changed in v2.1.1

* FIXED: WebRTC on UDP Port #772
* UPDATED: MediaMTX to [v0.22.1](https://github.com/aler9/mediamtx/releases/tag/v0.22.1)
* ENV Options: Re-enable `ON_DEMAND` to toggle connection mode. #643 #750 #764

## What's Changed in v2.1.0

⚠️ This version updates the backend rtsp-simple-server to MediaMTX which may cause some issues if you're using custom rtsp-simple-server related configs.

* CHANGED: rtsp-simple-server to MediaMTX.
* ENV Options:
  * New: `SUB_QUALITY` - Specify the quality to be used for the substream. #755
  * New: `SNAPSHOT_FORMAT` - Specify the output file format when using `SNAPSHOT` which can be used to create a timelapse/save multiple snapshots. e.g., `SNAPSHOT_FORMAT={cam_name}/%Y-%m-%d/%H-%M.jpg` #757:
* Home Assistant/MQTT:
  * Fixed: MQTT auto-discovery error #751
  * New: Additional entities for each of the cameras.
  * Changed: Default IMG_DIR to `media/wyze/img/` #660

## What's Changed in v2.0.2

* Camera Control: Don't wait for a response when sending `set_rotary_` commands. #746
* Camera Control: Add commands for motion tagging (potentially useful if using waitmotion in mini hacks):
  * `get_motion_tagging` current status: `1`=ON, `2`=OFF.
  * `set_motion_tagging_on` turn on motion tagging.
  * `set_motion_tagging_off` turn off motion tagging
* WebUI: Refresh image previews even if camera is not connected but enabled. (will still ignore battery cameras) #750
* WebUI: Add battery icon to cameras with a battery.
* WebUI: Use Last-Modified date to calculate the age of the thumbnails from the wyze API. 
* Update documentation for K10010ControlChannel media controls for potential on-demand control of video/audio.

## What's Changed in v2.0.1

* Fixed a bug where the WebUI would not start if 2FA was required. #741

## What's Changed in v2.0.0

⚠️ All streams will be on-demand unless local recording is enabled.

* NEW: Substreams - Add a secondary lower resolution stream:
  * `SUBSTREAM=True` to enable a lower resolution sub-stream on all cameras with a compatible firmware.
  * `SUBSTREAM_CAM_NAME=True` to enable sub-stream for a single camera without a firmware version check.
  * Secondary 360p stream will be available using the `cam-name-sub` uri.
  * See the [substream](https://github.com/mrlt8/docker-wyze-bridge/wiki/Camera-Substreams) page for more info.
* NEW: WebUI endpoints:
  * `/img/camera-name.jpg?exp=90` Take a new snapshot if the existing one is older than the `exp` value in seconds.
  * `/thumb/cam-name.jpg` Pull the latest thumbnail from the wyze API.
  * `/api/cam-name/enable` Enable the stream for recording and streaming. #717
  * `/api/cam-name/disable` Disable the stream for recording and streaming. #717
* NEW: ENV Options:
  * `LOG_FILE=true` Log to file (`/logs/debug.log`).
  * `SUBJECT_ALT_NAME=str` Specify the subjectAltName for SSL. #725
* NEW: WebUI controls: `start/stop/enable/disable` as well as some basic controls for the night vision.
* NEW: JS notifications when the status of a stream changes.
* NEW: Browser notifications when the page is in the background. Requires a secure context.
* Performance improvements and memory optimization!
* Updated boa to work alongside other camera controls on supported firmware.
* Bump python to 3.11
* Bump rtsp-simple-server to [v0.21.6](https://github.com/aler9/rtsp-simple-server/releases/tag/v0.21.6)
* Bump Wyze app version.

Some ENV options have been deprecated:
* `ON_DEMAND` - No longer used as all streams are now on-demand.
* `TAKE_PHOTO` -> `BOA_TAKE_PHOTO`
* `PULL_PHOTO` -> `BOA_PHOTO`
* `PULL_ALARM` -> `BOA_ALARM`
* `MOTION_HTTP` -> `BOA_MOTION`
* `MOTION_COOLDOWN` -> `BOA_COOLDOWN`


[View previous changes](https://github.com/idisposable/docker-wyze-bridge/releases)
