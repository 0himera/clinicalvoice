#!/usr/bin/env python3
"""Run against an isolated local mock; optionally include real local TTS/ASR.

Uses fictional patients. Checks MIS records after each turn, not just FSM states.
No audio files are created. --require-nlu additionally checks remote NLU routing.
"""

import argparse

from client import VoiceClient


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--voice", action="store_true", help="Use real local TTS and ASR")
    parser.add_argument("--require-nlu", action="store_true", help="Require enabled NLU")
    args = parser.parse_args()
    client = VoiceClient()
    try:
        health = client.checked(client.core.get("/healthz")).json()
        assert health["mode"] == "mock", "This check requires the local mock"
        assert not client.visits(), "Use a fresh, isolated DATABASE_PATH"
        client.start()

        def turn(text, state, *, outcome="", unchanged=True, fragment="", nlu=False):
            before = client.visits()
            recognized = client.transcribe(client.speak(text, "baya")) if args.voice else text
            result = client.turn(recognized)
            assert result["state"] == state, (recognized, result)
            assert result.get("outcome", "") == outcome, (recognized, result)
            assert fragment in result["bot_speech"], (recognized, result)
            if nlu:
                assert result["llm_used"], (recognized, result)
            if unchanged:
                assert client.visits() == before, f"Unexpected MIS write: {recognized}"
            if args.voice:
                assert client.speak(result["bot_speech"]).startswith(b"RIFF")
            print(recognized, "→", result["state"], outcome, flush=True)
            return result

        if args.require_nlu:
            turn(
                "Сориентируйте, к кому вообще можно обратиться?",
                "STATE_DIRECTORY",
                nlu=True,
            )
        turn(
            "У меня иногда болит голова, мне к кому записаться?",
            "STATE_OFFER_BOOKING",
            fragment="терапевта",
        )
        turn("Да", "STATE_AWAIT_IDENTITY")
        if args.require_nlu:
            turn(
                "Объясните назначение запрошенных сведений",
                "STATE_AWAIT_IDENTITY",
                fragment="не перепутать пациентов",
                nlu=True,
            )
        turn("Зачем?", "STATE_AWAIT_IDENTITY", fragment="не перепутать пациентов")
        turn("Зачем?", "STATE_AWAIT_IDENTITY", fragment="не перепутать пациентов")
        turn("Иванов Иван Иванович", "STATE_AWAIT_IDENTITY", fragment="дату рождения")
        turn("Что вы спросили?", "STATE_AWAIT_IDENTITY", fragment="дату рождения")
        turn(
            "Двенадцатое марта тысяча девятьсот восемьдесят пятого года",
            "STATE_CHOOSE_SLOT",
        )
        turn("Первый вариант", "STATE_CONFIRM_BOOKING")
        turn("Какие документы взять?", "STATE_CONFIRM_BOOKING", fragment="полис")
        turn("Да, зачем подтверждать?", "STATE_CONFIRM_BOOKING")
        turn("Повторите время", "STATE_CONFIRM_BOOKING", fragment="кабинет")
        turn("Да, подтверждаю", "STATE_FINAL", outcome="BOOKED", unchanged=False)
        original = client.visits()
        assert len(original) == 1 and original[0]["status"] == "Запланирована"

        turn("Перенесите запись", "STATE_CHOOSE_SLOT")
        turn("Первый вариант", "STATE_CONFIRM_BOOKING")
        turn("Какой у вас адрес?", "STATE_CONFIRM_BOOKING", fragment="Центральная")
        turn("Повторите время", "STATE_CONFIRM_BOOKING", fragment="кабинет")
        turn("Да, подтверждаю", "STATE_FINAL", outcome="RESCHEDULED", unchanged=False)
        moved = client.visits()
        assert len(moved) == 1 and moved[0]["id"] == original[0]["id"]
        assert (moved[0]["scheduled_at"], moved[0]["doctor"]["id"]) != (
            original[0]["scheduled_at"],
            original[0]["doctor"]["id"],
        )

        turn("Отмените запись", "STATE_CONFIRM_CANCEL")
        turn("Зачем?", "STATE_CONFIRM_CANCEL", fragment="До вашего явного подтверждения")
        turn("Повторите", "STATE_CONFIRM_CANCEL")
        turn("Да, отменяйте", "STATE_FINAL", outcome="CANCELLED", unchanged=False)
        cancelled = client.visits()
        assert len(cancelled) == 1 and cancelled[0]["id"] == original[0]["id"]
        assert cancelled[0]["status"] == "Отменена"
        print("Complex dialogue passed: clarification, FAQ, booking, same-ID move, cancel.")
    finally:
        client.close()


if __name__ == "__main__":
    main()
