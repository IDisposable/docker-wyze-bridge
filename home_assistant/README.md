[![Docker](https://github.com/idisposable/docker-wyze-bridge/actions/workflows/docker-image.yml/badge.svg)](https://github.com/idisposable/docker-wyze-bridge/actions/workflows/docker-image.yml)
[![GitHub release (latest by date)](https://img.shields.io/github/v/release/idisposable/docker-wyze-bridge?logo=github)](https://github.com/idisposable/docker-wyze-bridge/releases/latest)
[![Docker Image Size (latest semver)](https://img.shields.io/docker/image-size/idisposablegithub365/wyze-bridge?sort=semver&logo=docker&logoColor=white)](https://hub.docker.com/r/idisposablegithub365/wyze-bridge)
[![Docker Pulls](https://img.shields.io/docker/pulls/idisposablegithub365/wyze-bridge?logo=docker&logoColor=white)](https://hub.docker.com/r/idisposablegithub365/wyze-bridge)
![GitHub Repo stars](https://img.shields.io/github/stars/idisposable/docker-wyze-bridge?style=social)

# WebRTC/RTMP/RTSP/HLS Bridge for Wyze Cam

![479shots_so](https://user-images.githubusercontent.com/67088095/224595527-05242f98-c4ab-4295-b9f5-07051ced1008.png)

Create a local WebRTC, RTSP, RTMP, or HLS/Low-Latency HLS stream for most of your Wyze cameras including the outdoor, doorbell, and 2K cams. 

No third-party or special firmware required.

It just works!

Streams direct from camera without additional bandwidth or subscriptions.


Based on [@noelhibbard's script](https://gist.github.com/noelhibbard/03703f551298c6460f2fd0bfdbc328bd#file-readme-md) with [kroo/wyzecam](https://github.com/kroo/wyzecam) and [aler9/rtsp-simple-server](https://github.com/aler9/rtsp-simple-server).

Please consider [supporting](https://ko-fi.com/mrlt8) this project if you found it useful, or use our [affiliate link](https://amzn.to/3NLnbvt) if shopping on amazon!

## System Compatibility

![Supports arm32v7 Architecture](https://img.shields.io/badge/arm32v7-yes-success.svg)
![Supports arm64 Architecture](https://img.shields.io/badge/arm64-yes-success.svg)
![Supports amd64 Architecture](https://img.shields.io/badge/amd64-yes-success.svg)
![Supports Apple Silicon Architecture](https://img.shields.io/badge/apple_silicon-yes-success.svg)
[![Home Assistant Add-on](https://img.shields.io/badge/home_assistant-add--on-blue.svg?logo=homeassistant&logoColor=white)](https://github.com/idisposable/docker-wyze-bridge/wiki/Home-Assistant)

Should work on most x64 systems as well as on most modern arm-based systems like the Raspberry Pi 3/4/5 or Apple Silicon M1/M2/M3.

## Supported Cameras

![Wyze Cam V1](https://img.shields.io/badge/wyze_v1-yes-success.svg)
![Wyze Cam V2](https://img.shields.io/badge/wyze_v2-yes-success.svg)
![Wyze Cam V3](https://img.shields.io/badge/wyze_v3-yes-success.svg)
![Wyze Cam V3 Pro](https://img.shields.io/badge/wyze_v3_pro-yes-success.svg)
![Wyze Cam V4](https://img.shields.io/badge/wyze_v4-yes-success.svg)
![Wyze Cam Floodlight](https://img.shields.io/badge/wyze_floodlight-yes-success.svg)
![Wyze Cam Floodlight V2](https://img.shields.io/badge/wyze_floodlight_v2-yes-success.svg)
![Wyze Cam Pan](https://img.shields.io/badge/wyze_pan-yes-success.svg)
![Wyze Cam Pan V2](https://img.shields.io/badge/wyze_pan_v2-yes-success.svg)
![Wyze Cam Pan V3](https://img.shields.io/badge/wyze_pan_v3-yes-success.svg)
![Wyze Cam Pan Pro](https://img.shields.io/badge/wyze_pan_pro-yes-success.svg)
![Wyze Cam Outdoor](https://img.shields.io/badge/wyze_outdoor-yes-success.svg)
![Wyze Cam Outdoor V2](https://img.shields.io/badge/wyze_outdoor_v2-yes-success.svg)
![Wyze Cam Doorbell](https://img.shields.io/badge/wyze_doorbell-yes-success.svg)
![Wyze Cam Doorbell V2](https://img.shields.io/badge/wyze_doorbell_v2-yes-success.svg)

---
[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/J3J85TD3K)