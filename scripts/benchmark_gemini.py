#!/usr/bin/env python3
import json
import time
import urllib.request
import urllib.error
import os
import sys

API_KEY = os.environ.get("LLM_API_KEY", "")
if not API_KEY and len(sys.argv) > 1:
    API_KEY = sys.argv[1]

if not API_KEY:
    print("[ERROR] Не задан API ключ! Укажите его в переменной окружения LLM_API_KEY или аргументом:")
    print("  export LLM_API_KEY='your_api_key'")
    print("  python3 scripts/benchmark_gemini.py <api_key>")
    sys.exit(1)

MODELS = ["gemini-3.5-flash-lite", "gemini-3.1-flash-lite"]

PROMPTS = [
    {
        "name": "FAQ Краткий ответ (до 20 слов)",
        "prompt": "Ты голосовой ассистент поликлиники № 7. Пациент спрашивает: 'Со скольки работает поликлиника и когда можно сдать кровь?'. Ответь строго до 20 слов."
    },
    {
        "name": "NLU Извлечение слотов (JSON)",
        "prompt": """Ты NLU-парсер. Извлеки слоты из фразы пациента: "Здравствуйте, запишите меня пожалуйста к терапевту, я Сергеев Петр Иванович, родился 15 апреля 1985 года, лучше на утро".
Верни строго JSON:
{"intent": "...", "specialty": "...", "last_name": "...", "first_name": "...", "middle_name": "...", "birth_date": "...", "pref_time": "..."}"""
    }
]

def benchmark_stream(model: str, prompt: str):
    """Измеряет TTFT и общее время через streaming SSE (Native Gemini API)"""
    url = f"https://generativelanguage.googleapis.com/v1beta/models/{model}:streamGenerateContent?alt=sse&key={API_KEY}"
    payload = json.dumps({
        "contents": [{"parts": [{"text": prompt}]}],
        "generationConfig": {"temperature": 0.1}
    }).encode("utf-8")

    req = urllib.request.Request(
        url,
        data=payload,
        headers={"Content-Type": "application/json"}
    )

    t0 = time.perf_counter()
    first_token_time = None
    chunks_count = 0
    full_text = []

    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            for line in resp:
                line_str = line.decode("utf-8").strip()
                if line_str.startswith("data: "):
                    data_json = line_str[6:]
                    try:
                        chunk_obj = json.loads(data_json)
                        candidates = chunk_obj.get("candidates", [])
                        if candidates:
                            parts = candidates[0].get("content", {}).get("parts", [])
                            text_parts = [p.get("text", "") for p in parts if "text" in p]
                            chunk_text = "".join(text_parts)
                            if chunk_text:
                                if first_token_time is None:
                                    first_token_time = time.perf_counter()
                                full_text.append(chunk_text)
                                chunks_count += 1
                    except Exception:
                        pass
        t_end = time.perf_counter()

        ttft_ms = (first_token_time - t0) * 1000 if first_token_time else None
        total_ms = (t_end - t0) * 1000
        generated = "".join(full_text).strip()

        return {
            "success": True,
            "ttft_ms": ttft_ms,
            "total_ms": total_ms,
            "chunks": chunks_count,
            "text": generated
        }
    except Exception as e:
        return {
            "success": False,
            "error": str(e)
        }

def benchmark_non_stream(model: str, prompt: str):
    """Измеряет время полного E2E ответа без стриминга"""
    url = f"https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent?key={API_KEY}"
    payload = json.dumps({
        "contents": [{"parts": [{"text": prompt}]}],
        "generationConfig": {"temperature": 0.1}
    }).encode("utf-8")

    req = urllib.request.Request(
        url,
        data=payload,
        headers={"Content-Type": "application/json"}
    )

    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            t_end = time.perf_counter()
            total_ms = (t_end - t0) * 1000
            
            text = ""
            candidates = data.get("candidates", [])
            if candidates:
                parts = candidates[0].get("content", {}).get("parts", [])
                text = "".join([p.get("text", "") for p in parts if "text" in p]).strip()
            
            tokens = data.get("usageMetadata", {}).get("candidatesTokenCount", 0)
            return {
                "success": True,
                "total_ms": total_ms,
                "tokens": tokens,
                "text": text
            }
    except Exception as e:
        return {
            "success": False,
            "error": str(e)
        }

def main():
    print("=" * 70)
    print(" БЕНЧМАРК GEMINI 3.5 FLASH LITE vs GEMINI 3.1 FLASH LITE ")
    print("=" * 70)

    results = {m: [] for m in MODELS}

    for p_idx, p_info in enumerate(PROMPTS):
        print(f"\n--- Тест {p_idx+1}: {p_info['name']} ---")
        for model in MODELS:
            print(f"\nМодель: {model}")
            stream_ttfts = []
            stream_totals = []
            non_stream_totals = []

            # Делаем 3 прогона для сбора стабильной статистики
            for i in range(3):
                # Стриминг (TTFT)
                s_res = benchmark_stream(model, p_info["prompt"])
                # Без стриминга (полный E2E)
                ns_res = benchmark_non_stream(model, p_info["prompt"])

                if s_res["success"] and ns_res["success"]:
                    stream_ttfts.append(s_res["ttft_ms"])
                    stream_totals.append(s_res["total_ms"])
                    non_stream_totals.append(ns_res["total_ms"])
                    print(f"  Прогон {i+1}: TTFT = {s_res['ttft_ms']:.1f} мс | Stream E2E = {s_res['total_ms']:.1f} мс | Non-Stream = {ns_res['total_ms']:.1f} мс | Выход: {ns_res['tokens']} токенов")
                    if i == 0:
                        sample_text = ns_res['text'].replace('\n', ' ')
                        print(f"    Ответ: \"{sample_text[:100]}...\"")
                else:
                    print(f"  Ошибка прогона {i+1}: {s_res.get('error') or ns_res.get('error')}")
                
                time.sleep(0.3)

            if stream_ttfts:
                avg_ttft = sum(stream_ttfts) / len(stream_ttfts)
                avg_stream_tot = sum(stream_totals) / len(stream_totals)
                avg_non_stream = sum(non_stream_totals) / len(non_stream_totals)
                print(f"  >> СРЕДНЕЕ {model}: TTFT: {avg_ttft:.1f} мс | Stream E2E: {avg_stream_tot:.1f} мс | Non-stream: {avg_non_stream:.1f} мс")

if __name__ == "__main__":
    main()
