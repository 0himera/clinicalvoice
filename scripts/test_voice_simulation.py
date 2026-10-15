#!/usr/bin/env python3
"""Assert real 8 kHz TTS → ASR → dialogue → MIS booking, rescheduling and cancellation.
All patient data here is fictional. Audio is kept in memory and never saved.
"""

from client import VoiceClient


def main():
    client = VoiceClient()
    try:
        client.wait_ready()
        result = client.start()
        assert client.speak(result["bot_speech"]).startswith(b"RIFF")

        def turn(phrase):
            recognized = client.transcribe(client.speak(phrase, "baya"))
            result = client.turn(recognized)
            assert client.speak(result["bot_speech"]).startswith(b"RIFF")
            print(result["state"], result.get("outcome", ""))
            return result

        assert turn("Запишите меня к терапевту")["state"] == "STATE_AWAIT_IDENTITY"
        assert (
            turn(
                "Иванов Иван Иванович, двенадцатое марта тысяча девятьсот восемьдесят пятого года"
            )["state"]
            == "STATE_CHOOSE_SLOT"
        )
        assert turn("Первый вариант")["state"] == "STATE_CONFIRM_BOOKING"
        before = client.visits()
        assert turn("Повторите время")["state"] == "STATE_CONFIRM_BOOKING"
        assert client.visits() == before
        assert turn("Да, подтверждаю")["outcome"] == "BOOKED"
        original = next(v for v in client.visits() if v["status"] == "Запланирована")
        assert turn("Перенесите запись")["state"] == "STATE_CHOOSE_SLOT"
        assert turn("Первый вариант")["state"] == "STATE_CONFIRM_BOOKING"
        assert turn("Да, подтверждаю")["outcome"] == "RESCHEDULED"
        active = [v for v in client.visits() if v["status"] == "Запланирована"]
        assert len(active) == 1 and active[0]["id"] == original["id"]
        assert (active[0]["scheduled_at"], active[0]["doctor"]["id"]) != (
            original["scheduled_at"],
            original["doctor"]["id"],
        )
        assert turn("Отмените запись")["state"] == "STATE_CONFIRM_CANCEL"
        assert turn("Да, отменяйте")["outcome"] == "CANCELLED"
        assert not [v for v in client.visits() if v["status"] == "Запланирована"]
        print("Voice smoke passed: one visit created, moved with the same ID and cancelled.")
    finally:
        client.close()


if __name__ == "__main__":
    main()
