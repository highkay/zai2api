FROM mcr.microsoft.com/playwright/python:v1.59.0-noble

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright

WORKDIR /app

COPY browser_helper/requirements.txt /tmp/browser-helper-requirements.txt
RUN pip install --no-cache-dir -r /tmp/browser-helper-requirements.txt \
    && patchright install chromium

COPY browser_helper /app/browser_helper

EXPOSE 39090

CMD ["python", "-m", "uvicorn", "browser_helper.app:app", "--host", "0.0.0.0", "--port", "39090"]
