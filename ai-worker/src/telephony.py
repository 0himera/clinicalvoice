"""Asterisk FastAGI call metadata and paced, interruptible AudioSocket media.

The listeners bind to loopback. An AudioSocket UUID must have been registered by
FastAGI; arbitrary TCP clients cannot create dialogue sessions or select patients.
"""

import asyncio
import concurrent.futures
import contextlib
import logging
import re
import struct
import time
import uuid
from dataclasses import dataclass, field
from typing import Any

import httpx

from src.audio import EndpointDetector, frame, pcm_metrics, pcm_to_wav, wav_to_pcm
from src.cleanup import cancel_tasks
from src.diagnostics import CallDiagnostics
from src.services import Services
from src.settings import Settings

logger = logging.getLogger(__name__)


def agi_variable(name: str, value: str) -> bytes:
    if not re.fullmatch(r"[A-Z_][A-Z0-9_]{0,63}", name) or "\r" in value or "\n" in value:
        raise ValueError("invalid AGI variable")
    escaped = value.replace("\\", "\\\\").replace('"', '\\"')
    return f'SET VARIABLE {name} "{escaped}"\n'.encode("ascii")


@dataclass
class Call:
    call_id: str
    initial: dict[str, Any]
    created: float = field(default_factory=time.monotonic)
    result: dict[str, Any] = field(default_factory=dict)
    connected: bool = False
    finished: bool = False
    interrupted: bool = False


class Bridge:
    def __init__(self, settings: Settings, services: Services):
        self.settings = settings
        self.services = services
        self.trace = CallDiagnostics(settings)
        self.calls: dict[str, Call] = {}
        self.tasks: set[asyncio.Task] = set()
        self.servers: list[asyncio.Server] = []
        self.vad_executor = concurrent.futures.ThreadPoolExecutor(
            max_workers=max(4, settings.max_calls),
            thread_name_prefix="vad",
        )
        self.pending = 0
        self.http = httpx.AsyncClient(
            base_url=settings.core_url,
            headers={"Authorization": "Bearer " + settings.core_token}
            if settings.core_token
            else {},
            timeout=httpx.Timeout(40, connect=2),
            trust_env=False,
        )

    async def start(self):
        try:
            for handler, port in (
                (self.agi, self.settings.agi_port),
                (self.audio, self.settings.audio_port),
            ):
                self.servers.append(
                    await asyncio.start_server(handler, "127.0.0.1", port, limit=32768)
                )
        except BaseException:
            await self.close()
            raise
        logger.info("telephone transports ready")

    async def close(self):
        try:
            for server in self.servers:
                server.close()
            try:
                for server in self.servers:
                    await server.wait_closed()
            finally:
                await cancel_tasks(self.tasks)
        finally:
            self.vad_executor.shutdown(wait=False, cancel_futures=True)
            self.calls.clear()
            try:
                await self.http.aclose()
            finally:
                self.trace.close()

    async def post(self, path: str, data: dict | None = None) -> dict:
        started = time.monotonic()
        operation = path.rsplit("/", 1)[-1]
        call_id = (data or {}).get("call_id")
        if not call_id and len(path.split("/")) == 5:
            call_id = path.split("/")[-2]
        try:
            response = await self.http.post(path, json=data)
            response.raise_for_status()
            self.trace(
                "core_request",
                call_id=call_id,
                operation=operation,
                status=response.status_code,
                latency_ms=round((time.monotonic() - started) * 1000),
            )
            payload = response.json()
            if not isinstance(payload, dict):
                raise ValueError("invalid core response shape")
            return payload
        except httpx.HTTPError as exc:
            self.trace(
                "core_request_error",
                path=path,
                call_id=call_id,
                operation=operation,
                error=type(exc).__name__,
                latency_ms=round((time.monotonic() - started) * 1000),
                status=exc.response.status_code if isinstance(exc, httpx.HTTPStatusError) else None,
            )
            raise

    async def agi(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
        task = asyncio.current_task()
        if task:
            self.tasks.add(task)
            task.add_done_callback(self.tasks.discard)
        try:
            async with asyncio.timeout(45):
                environment: dict[str, str] = {}
                total = 0
                while True:
                    line = await reader.readline()
                    total += len(line)
                    if total > 16384:
                        raise ValueError("AGI environment is too large")
                    if not line.strip():
                        break
                    key, sep, value = line.decode("utf-8", "replace").partition(":")
                    if sep:
                        environment[key] = value.strip()

                async def command(name: str, value: str):
                    writer.write(agi_variable(name, value))
                    await asyncio.wait_for(writer.drain(), 2)
                    answer = await reader.readline()
                    if not answer.startswith(b"200 result="):
                        raise ConnectionError("AGI command failed")

                script = environment.get("agi_network_script", "").rsplit("/", 1)[-1]
                if script == "start":
                    now = time.monotonic()
                    for key, call in list(self.calls.items()):
                        if (not call.connected or call.finished) and now - call.created > 60:
                            del self.calls[key]
                    if len(self.calls) + self.pending >= self.settings.max_calls:
                        await command("VOICE_TRANSFER", "101")
                        return
                    self.pending += 1
                    try:
                        mode = environment.get("agi_arg_1", "inbound")
                        visit_id = (
                            int(environment.get("agi_arg_2", "0") or "0")
                            if mode == "outbound"
                            else 0
                        )
                        result = await self.post(
                            "/api/dialogue/start",
                            {
                                "caller_number": environment.get("agi_callerid", ""),
                                "channel_id": environment.get("agi_channel", ""),
                                "outbound_visit_id": visit_id,
                            },
                        )
                        call_id = str(uuid.UUID(result["call_id"]))
                        self.calls[call_id] = Call(call_id, initial=result)
                        self.trace(
                            "call_started",
                            call_id=call_id,
                            mode=mode,
                            state=result.get("state"),
                            bot_speech=result.get("bot_speech"),
                        )
                        await command("VOICE_ID", call_id)
                    finally:
                        self.pending -= 1
                elif script == "finish":
                    call_id = environment.get("agi_arg_1", "")
                    call = self.calls.pop(call_id, None)
                    target = str(call.result.get("transfer_target", "")) if call else "101"
                    await command(
                        "VOICE_TRANSFER", target if target in {"101", "102", "104", "105"} else ""
                    )
                    if call:
                        await self.post(f"/api/dialogue/{call_id}/end")
        except (
            ValueError,
            KeyError,
            TimeoutError,
            ConnectionError,
            httpx.HTTPError,
            OSError,
        ) as exc:
            logger.warning("FastAGI failed (%s)", type(exc).__name__)
        finally:
            writer.close()
            with contextlib.suppress(OSError, TimeoutError):
                await asyncio.wait_for(writer.wait_closed(), 2)
            self.tasks.discard(task)

    async def audio(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
        task = asyncio.current_task()
        if task:
            self.tasks.add(task)
            task.add_done_callback(self.tasks.discard)
        call = None
        worker = None
        vad_task = None
        playback = None
        recognitions: set[asyncio.Task] = set()
        end_reason = "peer_hangup"
        try:
            async with asyncio.timeout(180):
                kind, size = struct.unpack("!BH", await asyncio.wait_for(reader.readexactly(3), 5))
                if kind != 1 or size != 16:
                    raise ValueError("AudioSocket UUID frame is required")
                call_id = str(uuid.UUID(bytes=await asyncio.wait_for(reader.readexactly(16), 5)))
                call = self.calls.get(call_id)
                if call is None or call.connected or call.finished:
                    raise ValueError("unknown or already connected call")
                call.connected = True
                self.trace("audio_connected", call_id=call_id)
                detector = EndpointDetector(self.services.vad.new_stream())
                utterances: asyncio.Queue[asyncio.Task | None] = asyncio.Queue(maxsize=2)
                vad_queue: asyncio.Queue[bytes | None] = asyncio.Queue(maxsize=100)
                speech_lock = asyncio.Lock()
                last_activity = time.monotonic()
                silence_sent = 0
                revision = 0
                busy = False

                async def speak(text: str):
                    if not text:
                        return
                    started_tts = time.monotonic()
                    wav = await self.services.synthesize(text, 8000)
                    pcm = wav_to_pcm(wav)
                    self.trace(
                        "tts_ready",
                        call_id=call_id,
                        latency_ms=round((time.monotonic() - started_tts) * 1000),
                        audio_ms=round(len(pcm) / 16),
                    )
                    started = time.monotonic()
                    try:
                        for index in range(0, len(pcm), 320):
                            await asyncio.sleep(max(0, started + index / 16000 - time.monotonic()))
                            writer.write(frame(0x10, pcm[index : index + 320]))
                            await asyncio.wait_for(writer.drain(), 2)
                    except asyncio.CancelledError:
                        self.trace(
                            "playback_cancelled",
                            call_id=call_id,
                            played_ms=round((time.monotonic() - started) * 1000),
                        )
                        raise
                    self.trace("playback_finished", call_id=call_id)

                async def respond(result: dict, expected_revision: int, force: bool = False):
                    nonlocal playback, last_activity
                    if not force and (expected_revision != revision or call.interrupted):
                        self.trace(
                            "response_skipped",
                            call_id=call_id,
                            revision=expected_revision,
                            current_revision=revision,
                            interrupted=call.interrupted,
                        )
                        return
                    if playback and not playback.done():
                        playback.cancel()
                        await asyncio.gather(playback, return_exceptions=True)
                    async with speech_lock:
                        if not force and call.interrupted:
                            return
                        call.result = result
                        self.trace(
                            "bot_response",
                            call_id=call_id,
                            revision=expected_revision,
                            state=result.get("state"),
                            bot_speech=result.get("bot_speech"),
                            reason_code=result.get("reason_code"),
                            transfer_target=result.get("transfer_target"),
                            outcome=result.get("outcome"),
                        )
                        playback = asyncio.create_task(speak(result.get("bot_speech", "")))
                        try:
                            await playback
                        except asyncio.CancelledError:
                            if asyncio.current_task().cancelling():
                                raise
                    if (call.interrupted and not force) or expected_revision != revision:
                        return
                    last_activity = time.monotonic()
                    if result.get("transfer_target"):
                        # Redirect interrupts the dialplan application while its
                        # channel still exists. The persisted outbox is the fallback.
                        try:
                            await self.post(f"/api/dialogue/{call_id}/transfer")
                            self.trace(
                                "transfer_requested",
                                call_id=call_id,
                                target=result["transfer_target"],
                            )
                        except httpx.HTTPError:
                            logger.warning(
                                "operator redirect unavailable; escalation remains queued"
                            )
                        writer.close()
                    elif result.get("should_hangup"):
                        writer.close()

                async def recognize(pcm: bytes, version: int):
                    started_asr = time.monotonic()
                    recognition = await self.services.recognize(pcm_to_wav(pcm))
                    text = recognition.text
                    audio_fields = {}
                    if self.settings.debug_dialogue or self.settings.technical_logs:
                        audio_fields = await asyncio.to_thread(pcm_metrics, pcm)
                    self.trace(
                        "asr_result",
                        needs_repeat=recognition.needs_repeat,
                        avg_logprob=recognition.avg_logprob,
                        call_id=call_id,
                        revision=version,
                        text=text,
                        text_chars=len(text),
                        audio_ms=round(len(pcm) / 16),
                        latency_ms=round((time.monotonic() - started_asr) * 1000),
                        **audio_fields,
                    )
                    if text:
                        safety = await self.post(
                            f"/api/dialogue/{call_id}/interrupt", {"text": text}
                        )
                        if safety.get("transfer_target"):
                            call.interrupted = True
                            await respond(safety, version, force=True)
                            return None
                    return text, version, recognition.needs_repeat

                def recognized(done):
                    recognitions.discard(done)
                    if not done.cancelled():
                        done.exception()

                async def process():
                    nonlocal busy
                    busy = True
                    await respond(call.initial, revision)
                    busy = False
                    while True:
                        recognition = await utterances.get()
                        busy = True
                        try:
                            recognized_text = (
                                await recognition if recognition else ("", revision, False)
                            )
                        except asyncio.CancelledError:
                            if asyncio.current_task().cancelling():
                                raise
                            busy = False
                            continue
                        except (
                            httpx.HTTPError,
                            TimeoutError,
                            OSError,
                            ValueError,
                            RuntimeError,
                        ) as exc:
                            logger.warning("recognition failed (%s)", type(exc).__name__)
                            call.result["transfer_target"] = "101"
                            await respond(
                                {
                                    "bot_speech": "Не удалось распознать речь. Соединяю с регистратором.",
                                    "transfer_target": "101",
                                },
                                revision,
                            )
                            return
                        if recognized_text is None or call.interrupted:
                            return
                        text, version, needs_repeat = recognized_text
                        if version != revision:
                            busy = False
                            continue
                        started_core = time.monotonic()
                        try:
                            result = await self.post(
                                "/api/dialogue/turn",
                                {
                                    "call_id": call_id,
                                    "turn_id": str(uuid.uuid4()),
                                    "text": text,
                                    "asr_uncertain": needs_repeat,
                                },
                            )
                        except (httpx.HTTPError, TimeoutError, OSError, ValueError) as exc:
                            logger.error(
                                "Core dialogue turn failed for %s (%s)", call_id, type(exc).__name__
                            )
                            call.result["transfer_target"] = "101"
                            await respond(
                                {
                                    "bot_speech": "Произошла техническая ошибка. Перевожу вас на оператора.",
                                    "transfer_target": "101",
                                },
                                version,
                            )
                            return
                        self.trace(
                            "dialogue_result",
                            call_id=call_id,
                            revision=version,
                            latency_ms=round((time.monotonic() - started_core) * 1000),
                            state=result.get("state"),
                            reason_code=result.get("reason_code"),
                        )
                        await respond(result, version)
                        if call.interrupted:
                            return
                        busy = False

                async def vad_loop():
                    nonlocal revision, last_activity, silence_sent
                    loop = asyncio.get_running_loop()
                    try:
                        while True:
                            chunk = await vad_queue.get()
                            if chunk is None:
                                break
                            complete, interrupted = await loop.run_in_executor(
                                self.vad_executor, detector.feed, chunk
                            )
                            if interrupted:
                                revision += 1
                                self.trace(
                                    "speech_started",
                                    call_id=call_id,
                                    revision=revision,
                                    barge_in=bool(playback and not playback.done()),
                                )
                                last_activity = time.monotonic()
                                silence_sent = 0
                                if playback and not playback.done():
                                    playback.cancel()
                                while not utterances.empty():
                                    try:
                                        queued = utterances.get_nowait()
                                        if queued and not queued.done():
                                            queued.cancel()
                                    except (asyncio.QueueEmpty, ValueError):
                                        break
                                for rec_task in list(recognitions):
                                    if not rec_task.done():
                                        rec_task.cancel()
                                recognitions.clear()
                            for pcm in complete:
                                if utterances.full():
                                    try:
                                        oldest = utterances.get_nowait()
                                        if oldest and not oldest.done():
                                            oldest.cancel()
                                    except (asyncio.QueueEmpty, ValueError):
                                        pass
                                recognition = asyncio.create_task(recognize(pcm, revision))
                                recognitions.add(recognition)
                                recognition.add_done_callback(recognized)
                                try:
                                    utterances.put_nowait(recognition)
                                except asyncio.QueueFull:
                                    try:
                                        oldest = utterances.get_nowait()
                                        if oldest and not oldest.done():
                                            oldest.cancel()
                                    except Exception:
                                        pass
                                    utterances.put_nowait(recognition)
                                last_activity = time.monotonic()
                            if (
                                not busy
                                and (not playback or playback.done())
                                and utterances.empty()
                                and not detector.speech
                            ):
                                idle = time.monotonic() - last_activity
                                if idle >= 5 * (silence_sent + 1) and silence_sent < 2:
                                    silence_sent += 1
                                    self.trace(
                                        "silence_prompt",
                                        call_id=call_id,
                                        count=silence_sent,
                                        idle_ms=round(idle * 1000),
                                    )
                                    try:
                                        utterances.put_nowait(None)
                                    except asyncio.QueueFull:
                                        pass
                    except asyncio.CancelledError:
                        pass

                worker = asyncio.create_task(process())
                vad_task = asyncio.create_task(vad_loop())
                while not writer.is_closing():
                    packet = await asyncio.wait_for(reader.readexactly(3), 15)
                    kind, size = struct.unpack("!BH", packet)
                    if size > 6400:
                        raise ValueError("AudioSocket frame exceeds 400 ms")
                    data = await asyncio.wait_for(reader.readexactly(size), 5)
                    if kind == 0:
                        break
                    if kind == 0xFF:
                        raise ConnectionError("Asterisk reported an audio error")
                    if kind != 0x10:
                        if kind == 0x03:
                            continue
                        raise ValueError("8 kHz PCM16 frames are required")
                    await vad_queue.put(data)
                    if worker.done():
                        await worker  # surface errors rather than silently losing the call
                    if vad_task.done():
                        await vad_task
                await vad_queue.put(None)
                await vad_task
        except (
            asyncio.IncompleteReadError,
            TimeoutError,
            ConnectionError,
            ValueError,
            httpx.HTTPError,
            OSError,
            RuntimeError,
        ) as exc:
            end_reason = type(exc).__name__
            logger.info("audio connection ended (%s)", type(exc).__name__)
        finally:
            await cancel_tasks((worker, vad_task, playback, *recognitions))
            writer.close()
            with contextlib.suppress(OSError, TimeoutError):
                await asyncio.wait_for(writer.wait_closed(), 2)
            try:
                if call:
                    self.trace("call_ended", call_id=call.call_id, reason=end_reason)
                    # Keep the handoff result until FastAGI finish consumes it.
                    call.finished = True
                    call.created = time.monotonic()
                    with contextlib.suppress(httpx.HTTPError, TimeoutError):
                        async with asyncio.timeout(3):
                            await self.post(f"/api/dialogue/{call.call_id}/end")
            finally:
                self.tasks.discard(task)
