import contextlib
import json
import pickle
from datetime import datetime
from functools import wraps
from os import environ, utime
from os.path import getmtime
from pathlib import Path
from time import sleep, time
from typing import Any, Callable, Optional
from urllib.parse import parse_qs, urlparse

from requests import get
from requests.exceptions import ConnectionError, HTTPError, RequestException

from wyzecam.api_models import WyzeAccount, WyzeCamera, WyzeCredential
from wyzecam.api import AccessTokenError, RateLimitError, WyzeAPIError, get_cam_webrtc, get_camera_list, get_user_info, login, post_device, refresh_token, run_action
from wyzebridge.auth import get_secret
from wyzebridge.bridge_utils import env_bool, env_list
from wyzebridge.config import IMG_PATH, MOTION, TOKEN_PATH
from wyzebridge.logging import logger

def cached(func: Callable[..., Any]) -> Callable[..., Any]:
    def wrapper(self, *args: Any, **kwargs: Any):
        name = "auth" if func.__name__ == "login" else func.__name__.split("_", 1)[-1]
        if not self.auth and not self.creds.is_set and name != "auth":
            return
        if not kwargs.get("fresh_data"):
            if getattr(self, name, None):
                return func(self, *args, **kwargs)
            try:
                with open(TOKEN_PATH + name + ".pickle", "rb") as pkl_f:
                    if not (data := pickle.load(pkl_f)):
                        raise OSError
                if name == "user" and not self.creds.same_email(data.email):
                    raise ValueError("🕵️ Cached email doesn't match 'WYZE_EMAIL'")
                logger.info(f"📚 Using '{name}' from local cache...")
                setattr(self, name, data)
                return data
            except OSError:
                logger.info(f"🔍 Could not find local cache for '{name}'")
            except Exception as ex:
                logger.warning(f"Error restoring data for '{name}': [{type(ex).__name__}] {ex}")
                self.clear_cache()
        logger.info(f"☁️ Fetching '{name}' from the Wyze API...")
        result = func(self, *args, **kwargs)
        if result and (data := getattr(self, name, None)):
            pickle_dump(name, data)
        return result

    return wrapper

def authenticated(func: Callable[..., Any]) -> Callable[..., Any]:
    @wraps(func)
    def wrapper(self, *args: Any, **kwargs: Any):
        if not self.auth and not self.login():
            return

        try:
            return func(self, *args, **kwargs)
        except AccessTokenError:
            logger.warning("[API] ⚠️ Expired token?")
            self.refresh_token()
            return func(self, *args, **kwargs)
        except (RateLimitError, WyzeAPIError) as ex:
            logger.error(f"[API] [{type(ex).__name__}] {ex}")
        except ConnectionError as ex:
            logger.error(f"[API] [{type(ex).__name__}] {ex}")

    return wrapper

class WyzeCredentials:
    __slots__ = "email", "password", "key_id", "api_key"

    def __init__(self) -> None:
        self.email: str = get_secret("WYZE_EMAIL")
        self.password: str = get_secret("WYZE_PASSWORD")
        self.key_id: str = get_secret("API_ID")
        self.api_key: str = get_secret("API_KEY")

        if not self.is_set:
            logger.warning("[API] Credentials are NOT set")

    @property
    def is_set(self) -> bool:
        return bool(self.email and self.password and self.key_id and self.api_key)

    def update(self, email: str, password: str, key_id: str, api_key: str) -> None:
        self.email = email.strip()
        self.password = password.strip()
        self.key_id = key_id.strip()
        self.api_key = api_key.strip()

    def reset_creds(self):
        self.email = self.password = self.key_id = self.api_key = ""

    def same_email(self, email: str) -> bool:
        return self.email.lower() == email.lower() if self.is_set else True

class WyzeApi:
    __slots__ = "auth", "user", "creds", "cameras", "_last_pull"

    def __init__(self) -> None:
        self.auth: Optional[WyzeCredential] = None
        self.user: Optional[WyzeAccount] = None
        self.creds: WyzeCredentials = WyzeCredentials()
        self.cameras: Optional[list[WyzeCamera]] = None
        self._last_pull: float = 0

        if env_bool("FRESH_DATA"):
            self.clear_cache()

    @property
    def total_cams(self) -> int:
        return len(self.get_cameras() or [])

    @cached
    def login(self, fresh_data: bool = False, web: bool = False) -> WyzeCredential:
        if fresh_data:
            self.clear_cache()

        self.token_auth()
        while not self.auth:
            if not self.creds.is_set:
                logger.error("Credentials required to complete login!")
                logger.info("Please visit the WebUI to enter your credentials.")
                web = True

            while not (self.creds.is_set or self.auth):
                sleep(0.5)

            if not self.auth:
                self.attempt_login(web)

        return self.auth

    def attempt_login(self, web: bool = False) -> None:
        while self.auth_locked:
            sleep(1)

        try:
            self.auth = login(
                email=self.creds.email,
                password=self.creds.password,
                api_key=self.creds.api_key,
                key_id=self.creds.key_id,
            )
        except WyzeAPIError as ex:
            logger.error(f"[API] [{type(ex).__name__}] {ex}")
            if ex.code == "1000":
                logger.error("[API] Clearing credentials. Please try again.")
                self.creds.reset_creds()
        except HTTPError as ex:
            if hasattr(ex, "response") and ex.response.status_code == 403:
                logger.error(f"[API] Your IP may be blocked from {ex.request.url}")
            if hasattr(ex, "response") and ex.response.text:
                logger.error(f"[API] Response: {ex.response.text}")
        except (ValueError, RateLimitError, RequestException) as ex:
            logger.error(f"[API] [{type(ex).__name__}] {ex}")
        finally:
            if not web and not self.auth:
                logger.info("[API] Cool down for 20s before trying again.")
                sleep(20)

    def token_auth(
        self, tokens: Optional[str] = None, refresh: Optional[str] = None
    ) -> None:
        if len(token := tokens or env_bool("access_token", style="original")) > 150:
            token, refresh = parse_token(token)
            logger.info("⚠️ Using 'ACCESS_TOKEN' for authentication")
            try:
                self.auth = WyzeCredential(access_token=token)
            except Exception:
                self.auth = None

        if len(token := refresh or env_bool("refresh_token", style="original")) > 150:
            logger.info("⚠️ Using 'REFRESH_TOKEN' for authentication")
            try:
                creds = WyzeCredential(refresh_token=token)
                self.auth = refresh_token(creds)
            except Exception:
                self.auth = None

    @cached
    @authenticated
    def get_user(self) -> Optional[WyzeAccount]:
        if self.user:
            return self.user

        if self.auth:
            self.user = get_user_info(self.auth)

        return self.user

    @cached
    @authenticated
    def get_cameras(self, fresh_data: bool = False) -> list[WyzeCamera]:
        if self.cameras and not fresh_data:
            return self.cameras
        
        if not self.auth:
            logger.error("[API] User not authorized in get_camera()")
            return []

        self.cameras = get_camera_list(self.auth)
        self._last_pull = time()
        logger.info(f"[API] Fetched [{len(self.cameras)}] cameras")
        logger.debug(f"[API] cameras={[c.nickname for c in self.cameras]}")

        return self.cameras

    def filtered_cams(self) -> list[WyzeCamera]:
        return filter_cams(self.get_cameras() or [])

    def get_camera(self, uri: str, existing: bool = False) -> Optional[WyzeCamera]:
        if existing and self.cameras:
            with contextlib.suppress(StopIteration):
                return next((c for c in self.cameras if c.name_uri == uri))

        too_old = time() - self._last_pull > 120
        with contextlib.suppress(TypeError, AccessTokenError):
            for cam in self.get_cameras(fresh_data=too_old):
                if cam.name_uri == uri:
                    return cam

    def get_thumbnail(self, uri: str) -> str:
        if (cam := self.get_camera(uri, MOTION)) and valid_s3_url(cam.thumbnail):
            return cam.thumbnail or ""

        if cam := self.get_camera(uri):
            return cam.thumbnail or ""

        return ""

    def save_thumbnail(self, uri: str, thumb: str) -> bool:
        if not thumb:
            thumb = self.get_thumbnail(uri)

        if not thumb:
            return False

        save_to = IMG_PATH + uri + ".jpg"
        s3_timestamp = url_timestamp(thumb)

        with contextlib.suppress(FileNotFoundError):
            if s3_timestamp <= int(getmtime(save_to)):
                logger.debug(f"[API] Using existing thumbnail for {uri}")
                return True

        logger.info(f'☁️ Pulling "{uri}" thumbnail to {save_to}')

        try:
            img = get(thumb)
            img.raise_for_status()

            with open(save_to, "wb") as f:
                f.write(img.content)

            if modified := s3_timestamp or img.headers.get("Last-Modified"):
                ts_format = "%a, %d %b %Y %H:%M:%S %Z"

                if isinstance(modified, int):
                    utime(save_to, (modified, modified))
                elif ts := int(datetime.strptime(modified, ts_format).timestamp()):
                    utime(save_to, (ts, ts))

            return True
        except Exception as ex:
            logger.error(f"[API] Error pulling thumbnail: [{type(ex).__name__}] {ex}")
            return False

    @authenticated
    def get_kvs_signal(self, cam_name: str) -> Optional[dict]:
        if not (cam := self.get_camera(cam_name, True)):
            return {"result": "cam not found", "cam": cam_name}

        if not self.auth:
            logger.error("[API] User not authorized in get_kvs_signal()")
            return  {"result": "User not authorized"}

        try:
            logger.info("☁️ Fetching signaling data from the Wyze API...")
            wss = get_cam_webrtc(self.auth, cam.mac)
            return wss | {"result": "ok", "cam": cam_name}
        except (HTTPError, WyzeAPIError) as ex:
            logger.warning(f"[API] Error fetching signaling data [{type(ex).__name__}] {ex}")
            if isinstance(ex, HTTPError) and ex.response.status_code == 404:
                ex = "Camera does not support WebRTC"
            return {"result": str(ex), "cam": cam_name}

    @authenticated
    def refresh_token(self):
        logger.info("♻️ Refreshing tokens")

        if self.auth_locked:
            return

        if not self.auth:
            logger.error("[API] no auth information in refresh_token")
            return

        try:
            self.auth = refresh_token(self.auth)
            pickle_dump("auth", self.auth)
            return self.auth
        except Exception as ex:
            logger.error(f"[API] Exception refreshing token [{type(ex).__name__}] {ex}")
            logger.warning("⏰ Expired refresh token?")
            return self.login(fresh_data=True)

    @property
    def auth_locked(self) -> bool:
        if time() - self._last_pull < 15:
            return True
        self._last_pull = time()
        return False

    @authenticated
    def run_action(self, cam: WyzeCamera, action: str):
        logger.info(f"[CONTROL] ☁️ Sending {action} to {cam.name_uri} via Wyze API")

        if not self.auth:
            logger.error("[API] User not authorized in run_action()")
            return  {"status": "error", "response": "User not authorized"}

        try:
            resp = run_action(self.auth, cam, action.lower())
            return {"status": "success", "response": resp["result"]}
        except (ValueError, WyzeAPIError) as ex:
            logger.error(f"[CONTROL] Error: [{type(ex).__name__}] {ex}")
            return {"status": "error", "response": str(ex)}

    @authenticated
    def get_device_info(self, cam: WyzeCamera, pid: str = "", cmd: str = ""):
        logger.info(f"[CONTROL] ☁️ get_device_Info for {cam.name_uri} via Wyze API")

        if not self.auth:
            logger.error("[API] User not authorized in get_device_info()")
            return  {"status": "error", "response": "User not authorized"}

        params = {"device_mac": cam.mac, "device_model": cam.product_model}
        try:
            resp = post_device(self.auth, "get_device_Info", params, api_version=2)
            property_list = resp["property_list"]
        except (ValueError, WyzeAPIError) as ex:
            logger.error(f"[CONTROL] Error: [{type(ex).__name__}] {ex}")
            return {"status": "error", "response": str(ex)}

        if cmd in resp:
            return {"status": "success", "response": resp[cmd]}

        if not pid:
            return {"status": "success", "response": property_list}

        if not (item := next((i for i in property_list if i["pid"] == pid), None)):
            logger.error(f"[CONTROL] Error: {pid} not found")
            return {"status": "error", "response": f"{pid} not found"}

        return {"status": "success", "value": item.get("value"), "response": item}

    @authenticated
    def set_property(self, cam: WyzeCamera, pid: str, pvalue: str):
        params = {"pid": pid.upper(), "pvalue": pvalue}

        logger.info(f"[CONTROL] ☁️ set_property: {params} for {cam.name_uri} via Wyze API")

        if not self.auth:
            logger.error("[API] User not authorized in set_property()")
            return  {"status": "error", "response": "User not authorized"}

        params |= {"device_mac": cam.mac, "device_model": cam.product_model}
        try:
            res = post_device(self.auth, "set_property", params, api_version=2)
        except (ValueError, WyzeAPIError) as ex:
            logger.error(f"[CONTROL] Error: [{type(ex).__name__}] {ex}")
            return {"status": "error", "response": str(ex)}

        return {"status": "success", "response": res.get("result")}

    @authenticated
    def get_events(self, macs: Optional[list] = None, last_ts: int = 0):
        if not self.auth:
            logger.error("[API] User not authorized in get_events()")
            return time() + 60, []

        current_ms = int(time() + 60) * 1000
        params = {
            "count": 20,
            "order_by": 1,
            "begin_time": max((last_ts + 1) * 1_000, (current_ms - 1_000_000)),
            "end_time": current_ms,
            "nonce": str(int(time() * 1000)),
            "device_id_list": list(set(macs or [])),
            "event_value_list": [],
            "event_tag_list": [],
        }

        try:
            resp = post_device(self.auth, "get_event_list", params, api_version=4)
            return time(), resp["event_list"]
        except RateLimitError as ex:
            logger.error(f"[API] Events RateLimitError: [{type(ex).__name__}] {ex}, cooling down.")
            return ex.reset_by, []
        except (RequestException, WyzeAPIError) as ex:
            logger.error(f"[API] Events error: {type(ex).__name__}: {ex}, cooling down.")
            return time() + 60, []

    @authenticated
    def set_device_info(self, cam: WyzeCamera, params: dict):
        if not isinstance(params, dict):
            return {"status": "error", "response": f"Invalid params [{params=}]"}

        if not self.auth:
            logger.error("[API] User not authorized in set_device_info()")
            return  {"status": "error", "response": "User not authorized"}

        logger.info(f"[CONTROL] ☁ set_device_Info {params} for {cam.name_uri} via Wyze API")

        params |= {"device_mac": cam.mac}
        try:
            post_device(self.auth, "set_device_Info", params, api_version=1)
            return {"status": "success", "response": "success"}
        except ValueError as ex:
            error = f'{ex.args[0].get("code")}: {ex.args[0].get("msg")}'
            logger.error(f"[CONTROL] Error: {error}")
            return {"status": "error", "response": f"{error}"}

    def clear_cache(self, name: Optional[str] = None):
        data = {"auth", "user", "cameras"}

        if name in data:
            logger.info(f"♻️ Clearing {name} from local cache...")
            setattr(self, name, None)
            pickled_data = Path(TOKEN_PATH, f"{name}.pickle")
            if pickled_data.exists():
                pickled_data.unlink()
        else:
            logger.info("♻️ Clearing local cache...")
            for data_attr in data:
                setattr(self, data_attr, None)
            for token_file in Path(TOKEN_PATH).glob("*.pickle"):
                token_file.unlink()

def url_timestamp(url: str) -> int:
    try:
        url_path = urlparse(url).path.split("/")[3]
        return int(url_path.split("_")[1]) // 1000
    except Exception:
        return 0

def valid_s3_url(url: Optional[str]) -> bool:
    if not url:
        return False

    try:
        query_parameters = parse_qs(urlparse(url).query)
        x_amz_date = query_parameters["X-Amz-Date"][0]
        x_amz_expires = query_parameters["X-Amz-Expires"][0]
        amz_date = datetime.strptime(x_amz_date, "%Y%m%dT%H%M%SZ")
        return amz_date.timestamp() + int(x_amz_expires) > time()
    except (ValueError, TypeError, KeyError):
        return False

def env_filter(cam: WyzeCamera) -> bool:
    """Check if cam is being filtered in any env."""
    if not cam.nickname:
        return False
    return (
        cam.nickname.upper().strip() in env_list("FILTER_NAMES")
        or cam.mac in env_list("FILTER_MACS")
        or cam.product_model in env_list("FILTER_MODELS")
        or cam.model_name.upper() in env_list("FILTER_MODELS")
    )
    
def filter_cams(cams: list[WyzeCamera]) -> list[WyzeCamera]:
    total = len(cams)
    if env_bool("FILTER_BLOCK"):
        if filtered := list(filter(lambda cam: not env_filter(cam), cams)):
            logger.info(f"🪄 FILTER BLOCKING: {total - len(filtered)} of {total} cams")
            return filtered
    elif any(key.startswith("FILTER_") for key in environ):
        if filtered := list(filter(env_filter, cams)):
            logger.info(f"🪄 FILTER ALLOWING: {len(filtered)} of {total} cams")
            return filtered
    return cams

def pickle_dump(name: str, data: object):
    with open(TOKEN_PATH + name + ".pickle", "wb") as f:
        logger.info(f"💾 Saving '{name}' to local cache...")
        pickle.dump(data, f)

def parse_token(access_token: Optional[str]) -> tuple[Optional[str], Optional[str]]:
    if not access_token:
        return None, None

    access_token = access_token.strip(" '\"")

    try:
        json_token = json.loads(access_token)
        json_token = json_token.get("data", json_token)

        return json_token.get("access_token"), json_token.get("refresh_token")
    except ValueError:
        return access_token, None
