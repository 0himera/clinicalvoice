"""Enforce request limits before multipart parsing can spool an unbounded file."""

from starlette.exceptions import HTTPException
from starlette.responses import JSONResponse


class BodyLimit:
    def __init__(self, app):
        self.app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            await self.app(scope, receive, send)
            return
        limit = (4 << 20) + 65536 if scope["path"] == "/asr" else 16384
        length = dict(scope.get("headers", [])).get(b"content-length")
        if length is not None:
            try:
                size = int(length)
            except ValueError:
                size = -1
            if size < 0 or size > limit:
                await JSONResponse({"detail": "request body is too large"}, status_code=413)(
                    scope, receive, send
                )
                return
        received = 0

        async def bounded_receive():
            nonlocal received
            message = await receive()
            if message["type"] == "http.request":
                received += len(message.get("body", b""))
                if received > limit:
                    raise HTTPException(413, "request body is too large")
            return message

        await self.app(scope, bounded_receive, send)
