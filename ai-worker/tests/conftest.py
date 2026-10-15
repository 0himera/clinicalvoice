import io
import wave

import pytest

from src.services import Services


def wav_fixture(pcm: bytes = b"\x00\x00" * 800, rate: int = 8000) -> bytes:
    buffer = io.BytesIO()
    with wave.open(buffer, "wb") as output:
        output.setnchannels(1)
        output.setsampwidth(2)
        output.setframerate(rate)
        output.writeframes(pcm)
    return buffer.getvalue()


class FakeASR:
    def transcribe_wav(self, audio: bytes) -> str:
        return "Запишите к терапевту"


class FakeTTS:
    def __init__(self):
        self.requests = []

    def synthesize(self, text: str, sample_rate: int = 24000, speaker=None) -> bytes:
        self.requests.append((text, sample_rate, speaker))
        return wav_fixture(rate=sample_rate)


class FakeVAD:
    def new_stream(self):
        return lambda pcm: pcm != b"\x00" * len(pcm)


@pytest.fixture
def services():
    return Services(FakeASR(), FakeTTS(), FakeVAD())
