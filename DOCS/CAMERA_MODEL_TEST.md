# Camera model go2rtc Compatibility Test

## What You Need Before Starting

- Docker installed on a machine on the **same LAN as your doorbell**
- Your Wyze account email and password
- Your Wyze API ID and API Key (from https://developer-api.wyze.com)
- Your camera's local IP address (check your router's DHCP table — look for the device named something like "WyzeCam" with MAC starting in the Wyze OUI range)
- About 10 minutes

---

## Step 1: Get Your Camera's Current Firmware Version

Before testing, note the firmware version. You'll need it for the bug report if things don't work.

Open the Wyze app → tap your camera → Settings (gear icon) → Device Info → Firmware Version.

Write it down. It will look like `4.25.x.xxxx`.docke

---

## Step 2: Run go2rtc in Docker

Run this **exactly as-is**, substituting your values for the four ALL_CAPS items:

```bash
docker run --rm \
  --network host \
  -e GO2RTC_YAML="
log:
  level: debug
api:
  listen: :1984
wyze:
  YOU@EXAMPLE.COM:
    api_id: <YOUR API ID> aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
    api_key: <YOUR API KEY> xyxyxyxyxyyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyxyx
    password: <YOUR PASSWORD> zzzzzzzzzz
" \
  alexxit/go2rtc:latest
```

**Important: `--network host` is required.** go2rtc uses UDP broadcast to reach cameras on your LAN. Without host networking, it cannot see the cameras.

Leave this terminal running.

---

## Step 3: Add Your Camera via the local go2rtc WebUI

Open a browser and go to: `http://localhost:1984`

1. Click **"Add"** in the top navigation.
2. In the **"Wyze"** section that appears, you should see a list of your Wyze cameras fetched from the cloud.
3. Find your Camera (it will show as its name from the Wyze app).
4. Click the **"+"** button next to it.
5. It will generate a stream URL like: `wyze://192.168.x.x?uid=...&enr=...&mac=...&model=MODELNUMBER&dtls=true`
6. You'll be prompted to give it a name — use anything, like `testy`.
7. Click **Save**.

---

## Step 4: Test the Stream

Back on the main go2rtc page at `http://localhost:1984`:

1. You should see your `testy` stream listed.
2. Click **"Links"** next to it.
3. You'll see RTSP, WebRTC, HLS links. Click the **"play"** icon next to the WebRTC link.

**If it works:** You'll see live video in your browser. Note whether audio plays (look for the audio icon in the player).

**If it fails:** The stream will show a spinner and eventually error out. Check the terminal where Docker is running — you'll see error lines. Copy the last 20-30 lines of output.

---

## Step 5: Test Audio Specifically

If video works, check audio:

In the terminal output (from Step 2), look for lines containing:
- `codec` — tells you what audio codec the camera is advertising
- `pcm`, `pcmu`, `aac` — the specific codec name
- Any `audio` lines in the stream probe output

You can also click **"Probe"** in the go2rtc WebUI next to your stream. This shows a JSON blob — look for the `medias` array. It will list something like:
```
"audio, recvonly, PCML/8000/1"
```

That tells us the codec the camera is using.

---

## What to Report Back

Regardless of pass/fail, report:

1. **Firmware version** (from Step 1)
2. **Does video connect?** Yes / No / Timeout
3. **If no:** The last ~20 lines from the Docker terminal output
4. **If yes:** What does the Probe output show for audio codec?
5. **Error message if any:** The exact text, e.g., `connect failed: disco stage 1: timeout after 5s` or `expected K10003, got K10001`

---

## The Error Messages and What They Mean

| Error | Meaning |
|-------|---------|
| `disco stage 1: timeout` | Camera not responding to UDP broadcast — likely not on same subnet, or firewall blocking UDP |
| `expected K10003, got K10001` | DTLS handshake mismatch — camera firmware may not support DTLS (old firmware) |
| `av login failed: context deadline exceeded` | Connected but authentication timed out — usually a transient retry issue |
| `connect failed: read udp ... i/o timeout` | UDP connection established but no data flowing — can indicate old encryption scheme |
| Stream connects and shows video | **PASS** — go2rtc works with your doorbell |

---

## Cleanup

When done testing, Ctrl+C the Docker container. The `--rm` flag means it cleans up automatically.

The stream URL that go2rtc generated (with uid, enr, mac) is what the new bridge will auto-generate and pass to go2rtc on every startup — you won't need to do this manually in production.
