"""Each call owns its ONNX recurrent state; concurrent callers never share it."""

import numpy as np
import torch
from silero_vad import load_silero_vad

from src.audio import ResettingVAD


class SileroVADStream:
    """Stream-specific recurrent state wrapper around a shared ONNX session."""

    def __init__(self, session):
        self.session = session
        self.reset_states()

    def reset_states(self, batch_size: int = 1) -> None:
        self._state = torch.zeros((2, batch_size, 128)).float()
        self._context = torch.zeros(0)
        self._last_sr = 0
        self._last_batch_size = 0

    def __call__(self, x: torch.Tensor, sr: int = 8000) -> float:
        if x.dim() == 1:
            x = x.unsqueeze(0)
        num_samples = 512 if sr == 16000 else 256
        if x.shape[-1] != num_samples:
            raise ValueError(f"Provided number of samples is {x.shape[-1]}")
        batch_size = x.shape[0]
        context_size = 64 if sr == 16000 else 32
        if not self._last_batch_size or self._last_sr != sr or self._last_batch_size != batch_size:
            self.reset_states(batch_size)
        if len(self._context) == 0:
            self._context = torch.zeros(batch_size, context_size)
        x = torch.cat([self._context, x], dim=1)
        ort_inputs = {
            "input": x.numpy(),
            "state": self._state.numpy(),
            "sr": np.array(sr, dtype="int64"),
        }
        ort_outs = self.session.run(None, ort_inputs)
        out, state = ort_outs
        self._state = torch.from_numpy(state)
        self._context = x[..., -context_size:]
        self._last_sr = sr
        self._last_batch_size = batch_size
        return float(out[0][0])


class SileroVADService:
    def __init__(self):
        base_model = load_silero_vad(onnx=True)
        self.session = base_model.session

    def new_stream(self):
        stream = SileroVADStream(self.session)

        def is_speech(pcm_8k: bytes) -> bool:
            samples = np.frombuffer(pcm_8k, dtype="<i2").astype(np.float32) / 32768
            if len(samples) != 256:
                raise ValueError("VAD requires 256 samples at 8 kHz")
            return stream(torch.from_numpy(samples), 8000) >= 0.5

        return ResettingVAD(is_speech, stream.reset_states)
