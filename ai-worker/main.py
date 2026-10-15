import logging
from pathlib import Path

import uvicorn
from dotenv import load_dotenv

from src.settings import Settings

if __name__ == "__main__":
    load_dotenv(Path(__file__).resolve().parent.parent / ".env", override=False)
    settings = Settings.from_env()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    if settings.debug_dialogue:
        logging.warning("Local dialogue transcript debugging enabled: %s", settings.debug_log_dir)
    uvicorn.run("src.voice_bridge:app", host=settings.host, port=settings.port, access_log=False)
