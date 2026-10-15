"""Each call owns its ONNX recurrent state; concurrent callers never share it."""

import numpy as np
import torch
from silero_vad import load_silero_vad

from src.audio import ResettingVAD


class SileroVADService:
    def new_stream(self):
        model = load_silero_vad(onnx=True)

        def is_speech(pcm_8k: bytes) -> bool:
            samples = np.frombuffer(pcm_8k, dtype="<i2").astype(np.float32) / 32768
            if len(samples) != 256:
                raise ValueError("VAD requires 256 samples at 8 kHz")
            return float(model(torch.from_numpy(samples), 8000)) >= 0.5

        return ResettingVAD(is_speech, model.reset_states)
