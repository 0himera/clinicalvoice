"""Drain cancelled tasks and report unexpected failures without exception text."""

import asyncio
import logging

logger = logging.getLogger(__name__)


async def cancel_tasks(tasks):
    tasks = tuple(dict.fromkeys(task for task in tasks if task is not None))
    for task in tasks:
        task.cancel()
    results = await asyncio.gather(*tasks, return_exceptions=True)
    for result in results:
        if isinstance(result, BaseException) and not isinstance(result, asyncio.CancelledError):
            logger.warning("background task failed during cleanup (%s)", type(result).__name__)
