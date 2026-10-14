#!/usr/bin/env bash
set -e

# ==============================================================================
# Скрипт создания Asterisk Call File для инициирования звонка
# ==============================================================================

EXTENSION=${1:-"200"}
CALL_FILE="/tmp/call_${EXTENSION}_$(date +%s).call"

cat << EOF > "$CALL_FILE"
Channel: PJSIP/${EXTENSION}
CallerID: "Поликлиника № 7" <7000707>
MaxRetries: 1
RetryTime: 30
WaitTime: 30
Context: from-internal
Extension: 7000707
Priority: 1
EOF

echo "[ASTERISK] Сформирован Call-файл для абонента $EXTENSION: $CALL_FILE"

# Если Asterisk запущен в Docker контейнере tochka-asterisk:
if docker ps | grep -q tochka-asterisk; then
    docker cp "$CALL_FILE" tochka-asterisk:/var/spool/asterisk/outgoing/
    echo "[ASTERISK] Call-файл успешно передан в /var/spool/asterisk/outgoing/ в контейнер."
    echo "[ASTERISK] Asterisk звонит на номер $EXTENSION..."
else
    echo "[INFO] Контейнер Asterisk не запущен. Для запуска выполните: docker compose up -d"
fi
