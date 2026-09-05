FROM python:3.14.7-alpine3.24

WORKDIR /app
COPY reporter.py /app/reporter.py
COPY models.py severity.py rendering.py storage.py notifications.py delivery.py /app/

RUN addgroup -S reporter && adduser -S -G reporter reporter && \
    mkdir -p /data && chown reporter:reporter /data

USER reporter
EXPOSE 8091
HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8091/healthz', timeout=2)"

ENTRYPOINT ["python", "/app/reporter.py"]
