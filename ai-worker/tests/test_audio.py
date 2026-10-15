import struct

import pytest

from src.audio import EndpointDetector, ResettingVAD, frame, pcm_to_wav, wav_to_pcm


def test_protocol_and_wav_round_trip():
    pcm = b"\x00\x01" * 256
    assert frame(0x10, pcm) == struct.pack("!BH", 0x10, len(pcm)) + pcm
    assert wav_to_pcm(pcm_to_wav(pcm)) == pcm
    assert frame(0) == b"\x00\x00\x00"


def test_vad_endpoint_preroll_hangover_and_barge_in():
    detector = EndpointDetector(lambda pcm: pcm[0] != 0)
    quiet = b"\x00" * 512
    speech = b"\x01\x00" * 256
    for _ in range(6):
        assert detector.feed(quiet) == ([], False)
    interruptions = []
    for _ in range(8):
        complete, interrupted = detector.feed(speech)
        assert not complete
        interruptions.append(interrupted)
    assert sum(interruptions) == 1
    for _ in range(19):
        assert not detector.feed(quiet)[0]
    complete, _ = detector.feed(quiet)
    assert len(complete) == 1
    assert complete[0].startswith(quiet * 6)
    assert not detector.speech


def test_unbounded_speech_is_split_and_malformed_pcm_rejected():
    detector = EndpointDetector(lambda pcm: True)
    complete, _ = detector.feed(b"\x01\x00" * 170000)
    assert complete and len(complete[0]) <= 320000
    with pytest.raises(ValueError):
        detector.feed(b"\x00")


def test_short_replies_after_long_idle_keep_reaching_endpoint_detector():
    class RecurrentModel:
        # Reproduce a model that suppresses short replies after earlier history.
        suppress = False

        def detect(self, pcm):
            return not self.suppress and pcm != b"\x00" * len(pcm)

        def reset(self):
            self.suppress = False

    model = RecurrentModel()
    detector = EndpointDetector(ResettingVAD(model.detect, model.reset))
    quiet, voiced = b"\x00" * 512, b"\x01\x00" * 256
    for pause in (20, 37, 250, 511):
        model.suppress = True
        for _ in range(pause):
            detector.feed(quiet)
        for _ in range(6):
            assert not detector.feed(voiced)[0]
        complete = []
        for _ in range(20):
            complete.extend(detector.feed(quiet)[0])
        assert len(complete) == 1
        assert voiced * 6 in complete[0]


def test_recurrent_vad_context_survives_speech_and_short_pauses():
    resets = []
    vad = ResettingVAD(lambda pcm: pcm != b"\x00" * len(pcm), lambda: resets.append(True))
    for _ in range(3):
        assert vad(b"\x01\x00" * 256)
        for _ in range(19):
            assert not vad(b"\x00" * 512)
    assert not resets
    for _ in range(41):
        vad(b"\x00" * 512)
    assert len(resets) == 3
