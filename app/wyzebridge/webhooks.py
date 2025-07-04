from typing import Optional

import requests

from wyzebridge.build_config import VERSION
from wyzebridge.bridge_utils import env_cam
from wyzebridge.logging import logger

def send_webhook(event: str, camera: str, msg: str, img: Optional[str] = None) -> None:
    if not (url := env_cam(f"{event}_webhooks", camera, style="original")):
        return

    url = url.format(cam_name=camera, img=str(img))

    header = {
        "user-agent": f"wyzebridge/{VERSION}",
        "X-Title": f"{event} event".title(),
        "X-Attach": img,
        "X-Tags": f"{camera},{event}",
        "X-Camera": camera,
        "X-Event": event,
    }

    logger.debug(f"[WEBHOOKS] 📲 Triggering {event.upper()} event for {camera}")
    try:
        resp = requests.post(url, headers=header, data=msg, verify=False)
        resp.raise_for_status()
    except Exception as ex:
        print(f"[WEBHOOKS] [{type(ex).__name__}] {ex}")
