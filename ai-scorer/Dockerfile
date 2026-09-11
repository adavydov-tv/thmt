# AI-скорер сообщений: локальная модель эмбеддингов + калибруемый регрессор.
FROM python:3.12-slim

ENV PIP_NO_CACHE_DIR=1 \
    HF_HOME=/opt/hf \
    DB_PATH=/data/labels.db

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Модель запекается в образ, чтобы контейнер стартовал без интернета.
RUN python -c "from sentence_transformers import SentenceTransformer; \
    SentenceTransformer('paraphrase-multilingual-MiniLM-L12-v2')"

COPY main.py .

VOLUME /data
EXPOSE 8090
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8090"]
