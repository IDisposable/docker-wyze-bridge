import contextlib
import json
import time
from subprocess import Popen, TimeoutExpired
from threading import Thread
from typing import  Callable, Optional

from wyzebridge.stream import Stream
from wyzebridge.config import MOTION, MQTT_DISCOVERY, SNAPSHOT_INT, SNAPSHOT_TYPE
from wyzebridge.ffmpeg import rtsp_snap_cmd
from wyzebridge.logging import logger
from wyzebridge.mqtt import bridge_status, cam_control, publish_topic, update_preview
from wyzebridge.mtx_event import RtspEvent
from wyzebridge.wyze_events import WyzeEvents

class StreamManager:
    __slots__ = "stop_flag", "streams", "rtsp_snapshots", "last_snap", "thread"

    def __init__(self):
        self.stop_flag: bool = False
        self.streams: dict[str, Stream] = {}
        self.rtsp_snapshots: dict[str, Popen] = {}
        self.last_snap: float = 0
        self.thread: Optional[Thread] = None

    @property
    def total(self):
        return len(self.streams)

    @property
    def active(self):
        return len([s for s in self.streams.values() if s.enabled])

    def add(self, stream: Stream) -> str:
        uri = stream.uri
        self.streams[uri] = stream
        return uri

    def get(self, uri: str) -> Optional[Stream]:
        return self.streams.get(uri)

    def get_info(self, uri: str) -> dict:
        return stream.get_info() if (stream := self.get(uri)) else {}

    def get_all_cam_info(self) -> dict:
        return {uri: s.get_info() for uri, s in self.streams.items()}

    def stop_all(self) -> None:
        logger.info(f"[STREAM] Stopping {self.total} stream{'s'[:self.total^1]}")
        self.stop_flag = True

        for stream in self.streams.values():
            stream.stop()

        if self.thread and self.thread.is_alive():
            with contextlib.suppress(AttributeError, KeyError):
                self.thread.join()

    def monitor_streams(self, mtx_health: Callable) -> None:
        self.stop_flag = False

        if MQTT_DISCOVERY:
            self.thread = Thread(target=self.monitor_snapshots)
            self.thread.daemon = True # allow this thread to be aban
            self.thread.start()

        mqtt = cam_control(self.streams, self.send_cmd)
        logger.info(f"🎬 {self.total} stream{'s'[:self.total^1]} enabled")
        event = RtspEvent(self.streams)
        events = WyzeEvents(self.streams) if MOTION else None

        while not self.stop_flag:
            event.read(timeout=1)
            self.snap_all(self.active_streams())

            if events:
                events.check_motion()

            if int(time.time()) % 15 == 0:
                mtx_health()
                bridge_status(mqtt)

        if mqtt:
            mqtt.loop_stop()
        logger.info("[STREAM] Stream monitoring stopped")

    def monitor_snapshots(self) -> None:
        for cam in self.streams:
            update_preview(cam)
        while not self.stop_flag:
            for cam, ffmpeg in list(self.rtsp_snapshots.items()):
                if ffmpeg is not None and (returncode := ffmpeg.returncode) is not None:
                    if returncode == 0:
                        update_preview(cam)
                    # we have some response, remove from queue
                    self.remove_from_rtsp_snapshots(cam)
            time.sleep(1)

    def remove_from_rtsp_snapshots(self, cam):
        try:
            del self.rtsp_snapshots[cam]
        except Exception as ex:
            logger.error(f"[STREAM] [{type(ex).__name__}] removing {cam=} {ex}.")

    def active_streams(self) -> list[str]:
        """
        Health check on all streams and return a list of enabled
        streams that are NOT battery powered.

        Returns:
        - list(str): uri-friendly name of streams that are enabled.
        """
        if self.stop_flag:
            return []
        return [cam for cam, s in self.streams.items() if s.health_check() > 0]

    def snap_all(self, cams: Optional[list[str]] = None, force: bool = False):
        """
        Take an rtsp snapshot of the streams in the list.

        Args:
        - cams (list[str], optional): names of the streams to take a snapshot of.
        - force (bool, optional): Ignore interval and force snapshot. Defaults to False.
        """
        if force or self._should_snap():
            self.last_snap = time.time()
            for cam in cams or self.active_streams():
                stop_subprocess(self.rtsp_snapshots.get(cam))
                self.rtsp_snap_popen(cam, True)

    def _should_snap(self):
        return SNAPSHOT_TYPE == "rtsp" and time.time() - self.last_snap >= SNAPSHOT_INT

    def get_sse_status(self) -> dict:
        return {
            uri: {"status": cam.status(), "motion": cam.motion}
            for uri, cam in self.streams.items()
        }

    def send_cmd(
        self, cam_name: str, cmd: str, payload: str | list | dict = ""
    ) -> dict:
        """
        Send a command directly to the camera and wait for a response.

        Parameters:
        - cam_name (str): uri-friendly name of the camera.
        - cmd (str): The camera/tutk command to send.
        - payload (str): value for the tutk command.

        Returns:
        - dictionary: Results that can be converted to JSON.
        """
        resp = {"status": "error", "command": cmd, "payload": payload}

        if cam_name == "all" and cmd == "update_snapshot":
            self.snap_all(force=True)
            return resp | {"status": "success"}

        if not (stream := self.get(cam_name)):
            return resp | {"response": "Camera not found"}

        if cam_resp := stream.send_cmd(cmd, payload):
            status = cam_resp.get("value") if cam_resp.get("status") == "success" else 0
            if isinstance(status, dict):
                status = json.dumps(status)

            if "update_snapshot" in cam_resp:
                on_demand = not stream.connected
                snap = self.get_rtsp_snap(cam_name)
                if on_demand:
                    stream.stop()
                publish_topic(f"{cam_name}/{cmd}", int(time.time()) if snap else 0)
                return dict(resp, status="success", value=snap, response=snap)

            publish_topic(f"{cam_name}/{cmd}", status)

        return cam_resp if "status" in cam_resp else resp | cam_resp

    def rtsp_snap_popen(self, cam_name: str, interval: bool = False) -> Optional[Popen]:
        if not (stream := self.get(cam_name)):
            return
        stream.start()
        ffmpeg = self.rtsp_snapshots.get(cam_name)
        if not ffmpeg or ffmpeg.poll() is not None:
            # None means inherit from parent process
            ffmpeg = Popen(rtsp_snap_cmd(cam_name, interval), stderr=None)
            self.rtsp_snapshots[cam_name] = ffmpeg
        return ffmpeg

    def get_rtsp_snap(self, cam_name: str) -> bool:
        if not (stream := self.get(cam_name)) or stream.health_check() < 1:
            return False
        if not (ffmpeg := self.rtsp_snap_popen(cam_name)):
            return False
        try:
            if ffmpeg.wait(timeout=15) == 0:
                return True
        except TimeoutExpired:
            logger.info(f"❗ [{cam_name}] Snapshot timed out")
        except Exception as ex:
            logger.error(f"❗ [{cam_name}] [{type(ex).__name__}] {ex}")
        stop_subprocess(ffmpeg)

        return False

def stop_subprocess(ffmpeg: Optional[Popen]):
    if ffmpeg and ffmpeg.poll() is None:
        ffmpeg.kill()
        ffmpeg.communicate()
