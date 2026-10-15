"""AudioSocket framing and per-call endpoint detection, independent of models."""

import io
import struct
import wave
from collections import deque
from collections.abc import Callable


def frame(kind: int, payload: bytes = b"") -> bytes:
    if len(payload) > 65535:
        raise ValueError("AudioSocket payload is too large")
    return struct.pack("!BH", kind, len(payload)) + payload


def pcm_to_wav(pcm: bytes) -> bytes:
    buffer = io.BytesIO()
    with wave.open(buffer, "wb") as output:
        output.setnchannels(1)
        output.setsampwidth(2)
        output.setframerate(8000)
        output.writeframes(pcm)
    return buffer.getvalue()


def wav_to_pcm(audio: bytes) -> bytes:
    with wave.open(io.BytesIO(audio), "rb") as source:
        if (source.getnchannels(), source.getsampwidth(), source.getframerate()) != (1, 2, 8000):
            raise ValueError("telephone output must be PCM16 mono at 8 kHz")
        return source.readframes(source.getnframes())


class ResettingVAD:
    """Forget recurrent history after 640 ms without speech (20 × 32 ms).

    Long quiet periods otherwise bias the model against short replies such as
    "да". Keep the speech threshold and each call's model instance unchanged.
    """

    def __init__(self, detect: Callable[[bytes], bool], reset: Callable[[], None]):
        self.detect = detect
        self.reset = reset
        self.quiet_frames = 0

    def __call__(self, pcm: bytes) -> bool:
        voiced = self.detect(pcm)
        self.quiet_frames = 0 if voiced else self.quiet_frames + 1
        if self.quiet_frames >= 20:
            self.reset()
            self.quiet_frames = 0
        return voiced


class EndpointDetector:
    """32 ms VAD frames, 192 ms pre-roll, 640 ms hangover and a 20 s ceiling."""

    def __init__(self, is_speech: Callable[[bytes], bool]):
        self.is_speech = is_speech
        self.pending = bytearray()
        self.pre_roll: deque[bytes] = deque(maxlen=6)
        self.speech = bytearray()
        self.voice_frames = 0
        self.silence_frames = 0
        self.barge_frames = 0

    def feed(self, pcm: bytes) -> tuple[list[bytes], bool]:
        if len(pcm) % 2:
            raise ValueError("PCM16 payload must have even length")
        self.pending.extend(pcm)
        complete = []
        barge_in = False
        while len(self.pending) >= 512:
            chunk = bytes(self.pending[:512])
            del self.pending[:512]
            voiced = self.is_speech(chunk)
            self.barge_frames = self.barge_frames + 1 if voiced else 0
            barge_in |= self.barge_frames == 4
            if voiced:
                if not self.speech:
                    self.speech.extend(b"".join(self.pre_roll))
                    self.pre_roll.clear()
                self.speech.extend(chunk)
                self.voice_frames += 1
                self.silence_frames = 0
            elif self.speech:
                self.speech.extend(chunk)
                self.silence_frames += 1
            else:
                self.pre_roll.append(chunk)
            if self.speech and (self.silence_frames >= 20 or len(self.speech) >= 320000):
                if self.voice_frames >= 5:
                    complete.append(bytes(self.speech))
                self.speech.clear()
                self.voice_frames = self.silence_frames = self.barge_frames = 0
        return complete, barge_in
