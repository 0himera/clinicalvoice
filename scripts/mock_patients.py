"""Explicit fictional existing cards for isolated acceptance runs only."""

import json
import os
import sqlite3
from pathlib import Path


def seed_mock_database(path: Path, visits: list | None = None):
    patients = json.loads(
        (Path(__file__).resolve().parent.parent / "data/mock_patients.json").read_text()
    )
    fixture = {
        "Clients": {str(c["id"]): c for c in patients},
        "Visits": {str(v["id"]): v for v in visits or []},
        "Escalations": {},
        "NextClientID": max(100, max(c["id"] for c in patients)),
        "NextVisitID": max([5000] + [v["id"] for v in visits or []]),
        "NextEscalationID": 0,
    }
    # Exclusive creation prevents overwriting a running core's database.
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    os.close(fd)
    with sqlite3.connect(path) as connection:
        connection.execute("CREATE TABLE metadata(key TEXT PRIMARY KEY,value BLOB NOT NULL)")
        connection.execute("INSERT INTO metadata VALUES('mock_mis',?)", (json.dumps(fixture),))
