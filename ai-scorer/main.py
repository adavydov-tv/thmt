"""AI-скорер сообщений Slack: оценка «содержательности» от 0 до 10.

Устройство:
  - текст → признаки: эмбеддинг (sentence-transformers, мультиязычная MiniLM)
    ⊕ инженерные сигналы (лог-длина, код, ссылка, вопрос, многострочность,
    число слов);
  - оценка — StandardScaler + RidgeCV (alpha подбирается кросс-валидацией)
    поверх признаков, обученная на размеченных человеком парах «текст → оценка»;
  - на каждом обучении считаются честные out-of-fold метрики (MAE, R²,
    Pearson) и отдаются в /health и /train;
  - пока примеров меньше MIN_LABELS, работает эвристика: длина, код, ссылки,
    вопросы поднимают оценку; односложные «ок/спасибо/+1» опускают.

Хранилище примеров — SQLite в /data (docker-volume). Модель дообучается
мгновенно, поэтому /train можно дёргать после каждой правки разметки.
"""

import math
import os
import re
import sqlite3
import threading
from contextlib import closing

import numpy as np
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field
from sentence_transformers import SentenceTransformer
from sklearn.linear_model import RidgeCV
from sklearn.metrics import mean_absolute_error, r2_score
from sklearn.model_selection import cross_val_predict
from sklearn.pipeline import Pipeline
from sklearn.preprocessing import StandardScaler

DB_PATH = os.environ.get("DB_PATH", "/data/labels.db")
MODEL_NAME = os.environ.get("MODEL_NAME", "paraphrase-multilingual-MiniLM-L12-v2")
MIN_LABELS = int(os.environ.get("MIN_LABELS", "8"))

# Правила-автоответы: такие сообщения получают 0 сразу, без прогона через
# модель. Паттерн — точное совпадение всего сообщения (без учёта регистра и
# крайней пунктуации). Настраиваются через PUT /rules и хранятся в SQLite.
DEFAULT_RULES = {
    "min_length": 15,
    "patterns": [
        "+1", "ок", "ok", "окей", "да", "нет", "спасибо", "thanks", "thx",
        "ага", "угу", "привет", "hi", "hello", "lol", "))", "👍", "🙏",
    ],
    "drop_emoji_only": True,
}

app = FastAPI(title="activity-ai-scorer")

_model: SentenceTransformer | None = None
_model_lock = threading.Lock()
_reg_lock = threading.Lock()
_regressor: Pipeline | None = None
# _metrics — качество последнего обучения (кросс-валидация), отдаётся в /health
# и /train. None у полей, пока примеров мало для честной оценки.
_metrics: dict = {"mae": None, "r2": None, "pearson": None, "cv_folds": None}

# MIN_EVAL — с какого числа примеров считаем кросс-валидационные метрики.
MIN_EVAL = int(os.environ.get("MIN_EVAL", "15"))


def model() -> SentenceTransformer:
    global _model
    with _model_lock:
        if _model is None:
            _model = SentenceTransformer(MODEL_NAME)
        return _model


def db() -> sqlite3.Connection:
    os.makedirs(os.path.dirname(DB_PATH), exist_ok=True)
    conn = sqlite3.connect(DB_PATH)
    conn.execute(
        """CREATE TABLE IF NOT EXISTS labels (
               id INTEGER PRIMARY KEY AUTOINCREMENT,
               text TEXT NOT NULL,
               score REAL NOT NULL,
               created_at TEXT NOT NULL DEFAULT (datetime('now'))
           )"""
    )
    conn.execute(
        "CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)"
    )
    return conn


def load_rules() -> dict:
    import json

    with closing(db()) as conn:
        row = conn.execute("SELECT value FROM settings WHERE key = 'rules'").fetchone()
    if not row:
        return dict(DEFAULT_RULES)
    try:
        stored = json.loads(row[0])
    except ValueError:
        return dict(DEFAULT_RULES)
    rules = dict(DEFAULT_RULES)
    rules.update({k: v for k, v in stored.items() if k in rules})
    return rules


def save_rules(rules: dict) -> None:
    import json

    with closing(db()) as conn:
        conn.execute(
            "INSERT INTO settings (key, value) VALUES ('rules', ?) "
            "ON CONFLICT (key) DO UPDATE SET value = excluded.value",
            (json.dumps(rules, ensure_ascii=False),),
        )
        conn.commit()


def load_labels() -> list[tuple[int, str, float, str]]:
    with closing(db()) as conn:
        rows = conn.execute(
            "SELECT id, text, score, created_at FROM labels ORDER BY id DESC"
        ).fetchall()
    return rows


def engineered(text: str) -> list[float]:
    """Дешёвые инженерные признаки поверх эмбеддинга: длина, код, ссылка,
    вопрос, многострочность, число слов. Дают модели сигнал, которого нет в
    семантике (например, короткая, но содержательная команда/ссылка)."""
    t = text.strip()
    words = t.split()
    return [
        math.log10(len(t) + 1),
        1.0 if _CODE_RE.search(t) else 0.0,
        1.0 if _URL_RE.search(t) else 0.0,
        1.0 if "?" in t else 0.0,
        1.0 if "\n" in t else 0.0,
        math.log10(len(words) + 1),
    ]


def featurize(texts: list[str]) -> np.ndarray:
    """Вектор признаков = эмбеддинг MiniLM ⊕ инженерные признаки."""
    emb = model().encode(texts, normalize_embeddings=True)
    eng = np.array([engineered(t) for t in texts], dtype=float)
    return np.hstack([np.asarray(emb, dtype=float), eng])


def build_pipeline() -> Pipeline:
    """Стандартизация признаков + RidgeCV (alpha подбирается кросс-валидацией)."""
    return Pipeline([
        ("scale", StandardScaler()),
        ("reg", RidgeCV(alphas=np.logspace(-2, 3, 24))),
    ])


def _evaluate(X: np.ndarray, y: np.ndarray) -> dict:
    """Честные метрики: предсказания out-of-fold через кросс-валидацию."""
    n = len(y)
    if n < MIN_EVAL:
        return {"mae": None, "r2": None, "pearson": None, "cv_folds": None}
    folds = min(5, n)
    pred = cross_val_predict(build_pipeline(), X, y, cv=folds)
    pearson = None
    if np.std(pred) > 1e-9 and np.std(y) > 1e-9:
        pearson = float(np.corrcoef(pred, y)[0, 1])
    return {
        "mae": round(float(mean_absolute_error(y, pred)), 3),
        "r2": round(float(r2_score(y, pred)), 3),
        "pearson": round(pearson, 3) if pearson is not None else None,
        "cv_folds": folds,
    }


def train() -> bool:
    """Переобучает регрессор; False — примеров ещё мало, работает эвристика."""
    global _regressor, _metrics
    rows = load_labels()
    if len(rows) < MIN_LABELS:
        with _reg_lock:
            _regressor = None
        _metrics = {"mae": None, "r2": None, "pearson": None, "cv_folds": None}
        return False
    texts = [r[1] for r in rows]
    scores = np.array([r[2] for r in rows], dtype=float)
    X = featurize(texts)
    _metrics = _evaluate(X, scores)
    pipe = build_pipeline()
    pipe.fit(X, scores)
    with _reg_lock:
        _regressor = pipe
    return True


# ---- правила-автоответы: 0 без прогона через модель ----

# Пунктуация и пробелы по краям не должны спасать «+1!!» от правила.
_EDGE_PUNCT = " \t\n\r.,!?;:()[]{}«»\"'—-…"
_EMOJI_RE = re.compile(
    "[\U0001F000-\U0001FAFF☀-➿⬀-⯿️‍❤]+"
)


def _norm(text: str) -> str:
    return text.strip(_EDGE_PUNCT).lower()


def rule_zero(text: str, rules: dict) -> bool:
    """True — сообщение попало под автоправило и получает 0."""
    t = text.strip()
    if len(t) < int(rules.get("min_length", 0)):
        return True
    if _norm(t) in {_norm(p) for p in rules.get("patterns", []) if p.strip()}:
        return True
    if rules.get("drop_emoji_only"):
        without = _EMOJI_RE.sub("", t).strip(_EDGE_PUNCT)
        if without == "":
            return True
    return False


# ---- эвристика до калибровки ----

_URL_RE = re.compile(r"https?://\S+")
_CODE_RE = re.compile(r"```|`[^`]+`")


def heuristic(text: str) -> float:
    t = text.strip()
    score = 2.0 + 3.0 * min(1.0, math.log10(max(len(t), 1)) / 3.0) * 2
    if _CODE_RE.search(t):
        score += 2.0
    if _URL_RE.search(t):
        score += 1.0
    if "?" in t:
        score += 0.5
    return float(max(0.0, min(10.0, score)))


def score_texts(texts: list[str]) -> list[float]:
    # Сначала автоправила: мусорные сообщения получают 0 и в модель не идут.
    rules = load_rules()
    scores: list[float | None] = [0.0 if rule_zero(t, rules) else None for t in texts]
    rest_idx = [i for i, s in enumerate(scores) if s is None]
    if not rest_idx:
        return [s or 0.0 for s in scores]

    rest = [texts[i] for i in rest_idx]
    with _reg_lock:
        reg = _regressor
    if reg is None:
        predicted = [heuristic(t) for t in rest]
    else:
        X = featurize(rest)
        predicted = [float(max(0.0, min(10.0, p))) for p in reg.predict(X)]
    for i, p in zip(rest_idx, predicted):
        scores[i] = p
    return [s if s is not None else 0.0 for s in scores]


# ---- API ----


class ScoreRequest(BaseModel):
    texts: list[str] = Field(max_length=2000)


class LabelRequest(BaseModel):
    text: str
    score: float = Field(ge=0, le=10)


class RulesRequest(BaseModel):
    min_length: int = Field(ge=0, le=1000)
    patterns: list[str] = Field(max_length=500)
    drop_emoji_only: bool = True


@app.get("/health")
def health():
    rows = load_labels()
    with _reg_lock:
        trained = _regressor is not None
    return {
        "status": "ok",
        "model": MODEL_NAME,
        "labels": len(rows),
        "min_labels": MIN_LABELS,
        "trained": trained,
        "mode": "calibrated" if trained else "heuristic",
        "metrics": _metrics,
    }


@app.post("/score")
def score(req: ScoreRequest):
    if not req.texts:
        return {"scores": []}
    return {"scores": [round(s, 2) for s in score_texts(req.texts)]}


@app.get("/rules")
def rules():
    return load_rules()


@app.put("/rules")
def put_rules(req: RulesRequest):
    rules = {
        "min_length": req.min_length,
        "patterns": [p.strip() for p in req.patterns if p.strip()],
        "drop_emoji_only": req.drop_emoji_only,
    }
    save_rules(rules)
    return rules


@app.get("/labels")
def labels():
    return {
        "labels": [
            {"id": r[0], "text": r[1], "score": r[2], "created_at": r[3]}
            for r in load_labels()
        ]
    }


@app.post("/labels")
def add_label(req: LabelRequest):
    text = req.text.strip()
    if not text:
        raise HTTPException(400, "пустой текст")
    with closing(db()) as conn:
        cur = conn.execute("INSERT INTO labels (text, score) VALUES (?, ?)", (text, req.score))
        conn.commit()
        label_id = cur.lastrowid
    trained = train()
    return {"id": label_id, "trained": trained}


class BulkLabelItem(BaseModel):
    text: str
    score: float = Field(ge=0, le=10)


class BulkLabelRequest(BaseModel):
    labels: list[BulkLabelItem] = Field(max_length=50000)
    replace: bool = False  # True — очистить прежние примеры перед вставкой


@app.post("/labels/bulk")
def add_labels_bulk(req: BulkLabelRequest):
    """Массовая загрузка размеченных примеров: одна вставка + одно обучение.
    Пустые тексты пропускаются. replace=true очищает прежнюю разметку."""
    items = [(i.text.strip(), i.score) for i in req.labels if i.text.strip()]
    with closing(db()) as conn:
        if req.replace:
            conn.execute("DELETE FROM labels")
        conn.executemany("INSERT INTO labels (text, score) VALUES (?, ?)", items)
        conn.commit()
    trained = train()
    rows = load_labels()
    return {"inserted": len(items), "labels": len(rows), "trained": trained, "metrics": _metrics}


@app.delete("/labels/{label_id}")
def delete_label(label_id: int):
    with closing(db()) as conn:
        cur = conn.execute("DELETE FROM labels WHERE id = ?", (label_id,))
        conn.commit()
        if cur.rowcount == 0:
            raise HTTPException(404, "пример не найден")
    trained = train()
    return {"deleted": label_id, "trained": trained}


@app.post("/train")
def retrain():
    trained = train()
    rows = load_labels()
    return {"trained": trained, "labels": len(rows), "min_labels": MIN_LABELS, "metrics": _metrics}


# Тёплый старт: модель и регрессор поднимаются сразу, а не на первом запросе.
@app.on_event("startup")
def startup():
    model()
    train()
