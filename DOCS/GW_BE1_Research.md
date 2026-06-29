Net interpretation
Your GW_BE1 supports two parallel paths, not just WebRTC:

TUTK on port 8355 — what the Wyze mobile app actually uses (standard IoTC rendezvous, same MTP framing as every other Wyze camera). This is what wyze:// in go2rtc was built for.
Mars-asrv on port 28800 (Gwell auth) — likely the credential-mint step that gates the TUTK session for GW_-class devices. Our existing MarsRegisterGWUser in internal/webui/shim.go is close but targets a different endpoint.
WebRTC via mars-webcsrv (what we currently use) — was NOT used by the phone in this session. Still confirmed-working from your bridge's perspective via go2rtc #format=wyze, but it may not be the only option.
Three things you could try (in order of risk)
Cheap experiment, no code yet: temporarily edit your config to add FILTER_MODELS=GW_BE1 + FILTER_BLOCKS=true to exclude the doorbell, then remove "GW_BE1": true from webRTCStreamerModels in internal/wyzeapi/models.go, unfilter, and see whether go2rtc's plain wyze:// source accepts it. If yes, we drop the KVS shim dependency for this lineage.
If (1) 401s or hangs: add a MarsRegisterAuthUser mirror of the existing function, hitting wyze-mars-asrv.wyzecam.com instead of the gw_user endpoint, and supply the result to go2rtc.
Otherwise: leave the WebRTC path alone — it works. The pcap data is now in memory for whoever picks this up later.