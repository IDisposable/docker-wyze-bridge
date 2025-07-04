from dataclasses import dataclass

@dataclass(slots=True)
class WyzeStreamOptions:
    quality: str = "hd180"
    audio: bool = False
    record: bool = False
    reconnect: bool = False
    substream: bool = False
    frame_size: int = 0
    bitrate: int = 120

    def __post_init__(self):
        if self.record:
            self.reconnect = True

    def update_quality(self, hq_frame_size: int = 0) -> None:
        quality = (self.quality or "hd").lower().ljust(3, "0")
        bit = int(quality[2:] or "0")

        self.quality = quality
        self.bitrate = bit or 180
        self.frame_size = 1 if "sd" in quality else hq_frame_size
