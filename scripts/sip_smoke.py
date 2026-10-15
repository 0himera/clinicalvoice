#!/usr/bin/env python3
"""Loopback SIP/RTP diagnostic: an actual call through Asterisk, with synthetic speech.

Requires the local test endpoint 200 and ffmpeg. It never dials a public number.
This is a diagnostic phone, not a reusable SIP implementation.
"""

import asyncio
import hashlib
import re
import socket
import struct
import subprocess
import time
import uuid

from client import VoiceClient


def parse(message: bytes):
    headers, _, body = message.decode("utf-8", "replace").partition("\r\n\r\n")
    lines = headers.split("\r\n")
    fields = {}
    for line in lines[1:]:
        key, _, value = line.partition(":")
        fields[key.lower()] = value.strip()
    return lines[0], fields, body


class Phone:
    def __init__(self, number="200", password="testpass"):
        if number not in {"200", "101", "104", "105"}:
            raise ValueError("only loopback diagnostic accounts are allowed")
        self.number, self.password = number, password
        self.sip = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sip.bind(("127.0.0.1", 0))
        self.sip.setblocking(False)
        self.rtp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.rtp.bind(("127.0.0.1", 0))
        self.rtp.setblocking(False)
        self.responses = {}
        self.target = None
        self.dialog = None
        self.queue = asyncio.Queue()
        self.last_voice = 0.0
        self.voice_seen = asyncio.Event()
        self.connected = asyncio.Event()
        self.tasks = []

    @property
    def contact(self):
        return f"<sip:{self.number}@127.0.0.1:{self.sip.getsockname()[1]}>"

    def sdp(self):
        return (
            "v=0\r\no=test 1 1 IN IP4 127.0.0.1\r\ns=Tochka diagnostic\r\n"
            f"c=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio {self.rtp.getsockname()[1]} RTP/AVP 0\r\n"
            "a=rtpmap:0 PCMU/8000\r\na=sendrecv\r\n"
        )

    def media(self, body):
        address = re.search(r"c=IN IP4 ([\d.]+)", body)
        port = re.search(r"m=audio (\d+)", body)
        if not address or not port or address[1] != "127.0.0.1":
            raise RuntimeError("diagnostic only accepts loopback RTP")
        self.target = (address[1], int(port[1]))
        self.last_voice = 0
        self.voice_seen.clear()
        self.connected.set()

    async def start(self):
        self.tasks = [
            asyncio.create_task(f()) for f in (self.listen, self.send_rtp, self.receive_rtp)
        ]
        await self.request("REGISTER", "sip:127.0.0.1", extras="Expires: 600\r\n")

    def packet(self, method, uri, call_id, tag, seq, body="", extra="", to=None):
        branch = "z9hG4bK" + uuid.uuid4().hex
        text = (
            f"{method} {uri} SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:{self.sip.getsockname()[1]};branch={branch};rport\r\n"
            f"Max-Forwards: 70\r\nFrom: <sip:{self.number}@127.0.0.1>;tag={tag}\r\n"
            f"To: {to or f'<sip:{self.number}@127.0.0.1>'}\r\nCall-ID: {call_id}\r\nCSeq: {seq} {method}\r\n"
            f"Contact: {self.contact}\r\n{extra}"
        )
        if body:
            text += "Content-Type: application/sdp\r\n"
        return (text + f"Content-Length: {len(body.encode())}\r\n\r\n" + body).encode()

    async def request(self, method, uri, body="", extras=""):
        call_id, tag = uuid.uuid4().hex, uuid.uuid4().hex
        queue = self.responses[call_id] = asyncio.Queue()
        authorization = ""
        to = "<sip:700@127.0.0.1>" if method == "INVITE" else None
        try:
            for sequence in (1, 2):
                await asyncio.get_running_loop().sock_sendto(
                    self.sip,
                    self.packet(
                        method, uri, call_id, tag, sequence, body, extras + authorization, to
                    ),
                    ("127.0.0.1", 5060),
                )
                async with asyncio.timeout(10):
                    while True:
                        first, fields, content = await queue.get()
                        status = int(first.split()[1])
                        if status >= 200:
                            break
                if status in {401, 407}:
                    challenge = fields.get("www-authenticate", fields.get("proxy-authenticate", ""))
                    values = dict(re.findall(r'(\w+)="([^"]*)"', challenge))
                    realm, nonce = values["realm"], values["nonce"]

                    def md5(value):
                        return hashlib.md5(
                            value.encode()
                        ).hexdigest()  # Required by the loopback SIP fixture.

                    ha1, ha2 = md5(f"{self.number}:{realm}:{self.password}"), md5(f"{method}:{uri}")
                    response = md5(f"{ha1}:{nonce}:{ha2}")
                    suffix = ""
                    if "auth" in values.get("qop", ""):
                        cnonce = uuid.uuid4().hex
                        response = md5(f"{ha1}:{nonce}:00000001:{cnonce}:auth:{ha2}")
                        suffix = f', qop=auth, nc=00000001, cnonce="{cnonce}"'
                    header = "Authorization" if status == 401 else "Proxy-Authorization"
                    authorization = f'{header}: Digest username="{self.number}", realm="{realm}", nonce="{nonce}", uri="{uri}", response="{response}"{suffix}\r\n'
                    if method == "INVITE":
                        await asyncio.get_running_loop().sock_sendto(
                            self.sip,
                            self.packet("ACK", uri, call_id, tag, sequence, to=fields["to"]),
                            ("127.0.0.1", 5060),
                        )
                    continue
                if status != 200:
                    raise RuntimeError(f"SIP {method} failed: {status}")
                if method == "INVITE":
                    remote_uri = fields.get("contact", "<sip:700@127.0.0.1>").strip("<>")
                    await asyncio.get_running_loop().sock_sendto(
                        self.sip,
                        self.packet("ACK", remote_uri, call_id, tag, sequence, to=fields["to"]),
                        ("127.0.0.1", 5060),
                    )
                    self.dialog = (call_id, tag, sequence, fields["to"], remote_uri)
                    self.media(content)
                return
            raise RuntimeError("SIP authentication failed")
        finally:
            self.responses.pop(call_id, None)

    async def listen(self):
        loop = asyncio.get_running_loop()
        while True:
            packet, peer = await loop.sock_recvfrom(self.sip, 65535)
            first, fields, body = parse(packet)
            if first.startswith("SIP/2.0"):
                queue = self.responses.get(fields.get("call-id"))
                if queue:
                    queue.put_nowait((first, fields, body))
            elif first.startswith("BYE") or first.startswith("OPTIONS"):
                await loop.sock_sendto(self.sip, self.reply(fields, "200 OK"), peer)
                if first.startswith("BYE"):
                    self.target = None
            elif first.startswith("INVITE"):
                self.media(body)
                await loop.sock_sendto(self.sip, self.reply(fields, "200 OK", self.sdp()), peer)

    def reply(self, fields, status, body=""):
        text = f"SIP/2.0 {status}\r\n"
        for name in ("via", "from", "to", "call-id", "cseq"):
            value = fields[name]
            if name == "to" and ";tag=" not in value:
                value += ";tag=diagnostic"
            text += f"{name}: {value}\r\n"
        text += f"Contact: {self.contact}\r\n"
        if body:
            text += "Content-Type: application/sdp\r\n"
        return (text + f"Content-Length: {len(body)}\r\n\r\n" + body).encode()

    async def send_rtp(self):
        sequence, stamp = 0, 0
        loop = asyncio.get_running_loop()
        while True:
            started = loop.time()
            if self.target:
                queued = not self.queue.empty()
                chunk = self.queue.get_nowait() if queued else b"\xff" * 160
                packet = struct.pack("!BBHII", 0x80, 0, sequence, stamp, 12345) + chunk
                await loop.sock_sendto(self.rtp, packet, self.target)
                if queued:
                    self.queue.task_done()
                sequence = (sequence + 1) & 65535
                stamp = (stamp + 160) & 0xFFFFFFFF
            await asyncio.sleep(max(0, 0.02 - (loop.time() - started)))

    async def receive_rtp(self):
        loop = asyncio.get_running_loop()
        levels = []
        for value in range(256):
            u = (~value) & 255
            sample = (((u & 15) << 3) + 132) << ((u >> 4) & 7)
            levels.append((sample - 132) ** 2)
        while True:
            packet, _ = await loop.sock_recvfrom(self.rtp, 4096)
            if (
                len(packet) > 12
                and sum(levels[v] for v in packet[12:]) / (len(packet) - 12) > 10000
            ):
                self.last_voice = time.monotonic()
                self.voice_seen.set()

    async def wait_bot(self):
        async with asyncio.timeout(40):
            await self.voice_seen.wait()
            while True:
                remaining = 0.9 - (time.monotonic() - self.last_voice)
                if remaining <= 0:
                    break
                await asyncio.sleep(remaining)
        self.voice_seen.clear()

    async def say(self, audio):
        encoded = await asyncio.to_thread(
            subprocess.run,
            [
                "ffmpeg",
                "-v",
                "error",
                "-i",
                "pipe:0",
                "-f",
                "mulaw",
                "-ar",
                "8000",
                "-ac",
                "1",
                "pipe:1",
            ],
            input=audio,
            capture_output=True,
            check=True,
        )
        for i in range(0, len(encoded.stdout), 160):
            self.queue.put_nowait(encoded.stdout[i : i + 160].ljust(160, b"\xff"))
        await self.queue.join()

    async def close(self):
        with_context = self.target is not None
        if with_context and self.dialog:
            call_id, tag, sequence, to, uri = self.dialog
            await asyncio.get_running_loop().sock_sendto(
                self.sip,
                self.packet("BYE", uri, call_id, tag, sequence + 1, to=to),
                ("127.0.0.1", 5060),
            )
        if self.tasks:
            try:
                async with asyncio.timeout(2):
                    await self.request("REGISTER", "sip:127.0.0.1", extras="Expires: 0\r\n")
            except (TimeoutError, RuntimeError, OSError):
                pass
        for task in self.tasks:
            task.cancel()
        await asyncio.gather(*self.tasks, return_exceptions=True)
        self.sip.close()
        self.rtp.close()


async def main():
    client = VoiceClient()
    phone = Phone()
    try:
        await asyncio.to_thread(client.wait_ready)
        await phone.start()
        await phone.request("INVITE", "sip:700@127.0.0.1", phone.sdp())
        await phone.wait_bot()
        for phrase in [
            "Запишите меня к терапевту",
            "Иванов Иван Иванович двенадцатое марта тысяча девятьсот восемьдесят пятого года",
            "Первый вариант",
            "Да, подтверждаю",
            "Нет",
            "Отмените запись",
            "Да, отменяйте",
        ]:
            audio = await asyncio.to_thread(client.speak, phrase, "baya")
            await phone.say(audio)
            await phone.wait_bot()
            print("SIP turn completed")
        visits = await asyncio.to_thread(client.visits)
        assert any(v["status"] == "Отменена" for v in visits), (
            "SIP dialogue did not cancel its visit"
        )
        print("SIP/RTP smoke passed through Asterisk, local speech models and the MIS mock.")
    finally:
        await phone.close()
        client.close()


if __name__ == "__main__":
    asyncio.run(main())
