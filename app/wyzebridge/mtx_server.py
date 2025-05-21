from os import getenv
from pathlib import Path
from signal import SIGTERM
from subprocess import Popen
from typing import Optional

import yaml
from wyzebridge.bridge_utils import env_bool
from wyzebridge.logging import logger

MTX_CONFIG = "/app/mediamtx.yml"

RECORD_LENGTH = env_bool("RECORD_LENGTH", "60s")
RECORD_KEEP = env_bool("RECORD_KEEP", "0s")
REC_FILE = env_bool("RECORD_FILE_NAME", r"%Y-%m-%d-%H-%M-%S", style="original").strip("/")
REC_PATH = env_bool("RECORD_PATH", r"%path/{cam_name}/%Y/%m/%d", style="original").strip("/")
RECORD_PATH = f"{Path(REC_PATH) / Path(REC_FILE)}".removesuffix(".mp4").removesuffix(".fmp4").removesuffix(".ts")

class MtxInterface:
    __slots__ = "data", "_modified"

    def __init__(self):
        self.data = {}
        self._modified = False

    def __enter__(self):
        self._load_config()
        return self

    def __exit__(self, exc_type, exc_value, traceback):
        if self._modified:
            self._save_config()

    def _load_config(self):
        with open(MTX_CONFIG, "r") as f:
            self.data = yaml.safe_load(f) or {}

    def _save_config(self):
        with open(MTX_CONFIG, "w") as f:
            yaml.safe_dump(self.data, f, sort_keys=False)

    def get(self, path: str):
        keys = path.split(".")
        current = self.data
        for key in keys:
            if current is None:
                return None
            current = current.get(key)
        return current

    def set(self, path: str, value):
        keys = path.split(".")
        current = self.data
        for key in keys[:-1]:
            current = current.setdefault(key, {})
        current[keys[-1]] = value
        self._modified = True

    def add(self, path: str, value):
        if not isinstance(value, list):
            value = [value]
        current = self.data.get(path)
        if isinstance(current, list):
            current.extend([item for item in value if item not in current])
        else:
            self.data[path] = value
        self._modified = True


class MtxServer:
    """Setup and interact with the backend mediamtx."""

    __slots__ = "sub_process"

    def __init__(self) -> None:
        self.sub_process: Optional[Popen] = None
        self._setup_path_defaults()

    def _setup_path_defaults(self):
        logger.info(f"[MTX] setting up default RECORD_PATH: {RECORD_PATH}")
        record_path = ensure_record_path()

        with MtxInterface() as mtx:
            mtx.set("paths", {})
            for event in {"Read", "Unread", "Ready", "NotReady"}:
                bash_cmd = f"echo $MTX_PATH,{event}! > /tmp/mtx_event;"
                mtx.set(f"pathDefaults.runOn{event}", f"bash -c '{bash_cmd}'")
            mtx.set("pathDefaults.runOnDemandStartTimeout", "30s")
            mtx.set("pathDefaults.runOnDemandCloseAfter", "60s")
            mtx.set("pathDefaults.recordPath", record_path)
            mtx.set("pathDefaults.recordSegmentDuration", RECORD_LENGTH)
            mtx.set("pathDefaults.recordDeleteAfter", RECORD_KEEP)

    def setup_auth(self, api: Optional[str], stream: Optional[str]):
        administrator: dict = {
                "user": "any",
                "ips": ["127.0.0.1", "::1"],
                "permissions": [{"action": "api"}, {"action": "metrics"}, {"action": "pprof"}]
            }
        publisher: dict = {
                "user": "any",
                "permissions": [{"action": "read"}, {"action": "playback"}, {"action": "publish"}]
            }

        with MtxInterface() as mtx:
            mtx.set("authInternalUsers", [])
            mtx.add("authInternalUsers", administrator)
            mtx.add("authInternalUsers", publisher)
            if (api or not stream):
                client: dict = { }
                if api:
                    client.update({"user": "wb", "pass": api})
                else:
                    client.update({"user": "any"})
                client.update({"permissions": [{"action": "read"}, {"action": "playback"}]})
                mtx.add("authInternalUsers", client)
            if stream:
                logger.info("[MTX] Custom stream auth enabled")
                for client in parse_auth(stream):
                    mtx.add("authInternalUsers", client)


    def add_path(self, uri: str, on_demand: bool = True):
        with MtxInterface() as mtx:
            if on_demand:
                cmd = "bash -c 'echo $MTX_PATH,{}! > /tmp/mtx_event'"
                mtx.set(f"paths.{uri}.runOnDemand", cmd.format("start"))
                mtx.set(f"paths.{uri}.runOnUnDemand", cmd.format("stop"))
            else:
                mtx.set(f"paths.{uri}", {})

    def add_source(self, uri: str, value: str):
        with MtxInterface() as mtx:
            mtx.set(f"paths.{uri}.source", value)

    def record(self, uri: str):
        logger.info(f"[MTX] Starting record for {uri}")
        record_path = ensure_record_path().format(cam_name=uri.lower(), CAM_NAME=uri.upper())

        logger.info(f"[MTX] 📹 Will record {RECORD_LENGTH} clips for {uri} to {record_path}")
        with MtxInterface() as mtx:
            mtx.set(f"paths.{uri}.record", True)
            mtx.set(f"paths.{uri}.recordPath", record_path)

    def start(self):
        if self.sub_process:
            return
        logger.info(f"[MTX] starting MediaMTX {getenv('MTX_TAG')}")
        self.sub_process = Popen(["./mediamtx", "./mediamtx.yml"],
                                 stdout=None,  # None means inherit from parent process
                                 stderr=None   # None means inherit from parent process
        )

    def stop(self):
        if not self.sub_process:
            return
        if self.sub_process.poll() is None:
            logger.info("[MTX] Stopping MediaMTX...")
            self.sub_process.send_signal(SIGTERM)
            self.sub_process.communicate()
        self.sub_process = None

    def restart(self):
        self.stop()
        self.start()

    def health_check(self):
        if self.sub_process and self.sub_process.poll() is not None:
            logger.error(f"[MediaMTX] Process exited with {self.sub_process.poll()}")
            self.restart()

    def setup_webrtc(self, bridge_ip: Optional[str]):
        if not bridge_ip:
            logger.warning("SET WB_IP to allow WEBRTC connections.")
            return
        ips = bridge_ip.split(",")
        logger.debug(f"Using {' and '.join(ips)} for webrtc")
        with MtxInterface() as mtx:
            mtx.add("webrtcAdditionalHosts", ips)

    def setup_llhls(self, token_path: str = "/tokens/", hass: bool = False):
        logger.info("[MTX] Configuring LL-HLS")
        with MtxInterface() as mtx:
            mtx.set("hlsVariant", "lowLatency")
            mtx.set("hlsEncryption", True)
            if env_bool("mtx_hlsServerKey"):
                return

            key = "/ssl/privkey.pem"
            cert = "/ssl/fullchain.pem"
            if hass and Path(key).is_file() and Path(cert).is_file():
                logger.info(
                    "[MTX] 🔐 Using existing SSL certificate from Home Assistant"
                )
                mtx.set("hlsServerKey", key)
                mtx.set("hlsServerCert", cert)
                return

            cert_path = f"{token_path}hls_server"
            generate_certificates(cert_path)
            mtx.set("hlsServerKey", f"{cert_path}.key")
            mtx.set("hlsServerCert", f"{cert_path}.crt")


def ensure_record_path() -> str:
    record_path = RECORD_PATH

    if "%path" in record_path:
        logger.info("[MTX] The computed record_path includes %path")
    else:
        logger.warning("[MTX] The computed record_path did not include %path, prepending it to the pattern")
        record_path = "%path/" + record_path

    if "%s" in record_path or all(x in record_path for x in ["%Y", "%m", "%d", "%H", "%M", "%S"]):
        logger.info(f"[MTX] The computed record_path: '{record_path}' IS VALID")
    else:
        logger.warning(f"[MTX] The computed record_path: '{record_path}' IS NOT VALID, appending the %%s to the pattern")
        record_path += "_%s"

    return record_path


def mtx_version() -> str:
    try:
        with open("/MTX_TAG", "r") as tag:
            return tag.read().strip()
    except FileNotFoundError:
        return ""


def generate_certificates(cert_path):
    if not Path(f"{cert_path}.key").is_file():
        logger.info("[MTX] 🔐 Generating key for LL-HLS")
        Popen(
            ["openssl", "genrsa", "-out", f"{cert_path}.key", "2048"],
            stdout=None,  # None means inherit from parent process
            stderr=None   # None means inherit from parent process
        ).wait()
    if not Path(f"{cert_path}.crt").is_file():
        logger.info("[MTX] 🔏 Generating certificate for LL-HLS")
        dns = getenv("SUBJECT_ALT_NAME")
        Popen(
            ["openssl", "req", "-new", "-x509", "-sha256"]
            + ["-key", f"{cert_path}.key"]
            + ["-subj", "/C=US/ST=WA/L=Kirkland/O=WYZE BRIDGE/CN=wyze-bridge"]
            + (["-addext", f"subjectAltName = DNS:{dns}"] if dns else [])
            + ["-out", f"{cert_path}.crt"]
            + ["-days", "3650"],
            stdout=None,  # None means inherit from parent process
            stderr=None   # None means inherit from parent process
        ).wait()


def parse_auth(auth: str) -> list[dict[str, str]]:
    entries = []
    for entry in auth.split("|"):
        creds, *endpoints = entry.split("@")
        if ":" not in creds:
            continue
        user, password, *ips = creds.split(":", 2)
        if ips:
            ips = ips[0].split(",")
        data: dict = {"user": user or "any", "pass": password, "ips": ips, "permissions": []}
        if endpoints:
            paths = []
            for endpoint in endpoints[0].split(","):
                paths.append(endpoint)
                data["permissions"].append({"action": "read", "path": endpoint})
                data["permissions"].append({"action": "playback", "path": endpoint})
        else:
            paths = "all"
            data["permissions"].append({"action": "read"})
            data["permissions"].append({"action": "playback"})
        logger.info(f"[MTX] Auth [{data['user']}:{data['pass']}] {paths=}")
        entries.append(data)
    return entries
