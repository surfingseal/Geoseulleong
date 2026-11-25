"""Optimized 5-Minute x Weekday(0..6) x 24h Batch Precompute -> Cloudflare R2.

Reuses existing 280 supported routes from seat_prediction_per_route_v1/model_registry.json
and exact preprocessing/models from seat_v2_pipeline, multiroute_pipeline, and per_route_pipeline.

Format (schema: "weekday-5m-v2"):
  One compact binary-gzip file per route covering all 7 weekdays x 288 5-min slots:
    predictions/seat-v1/runs/<run_id>/routes/<route_id>.bin.gz
  Payload inside gzip:
    Line 1: UTF-8 JSON header + '\n'
    Body:   seats uint8[7, n_visits, 288] + (prob_bp uint16_le[7, n_visits, 288] if classifier_verified)

Uploads manifest.json and atomically updates predictions/seat-v1/latest.json
ONLY after all 280 route files succeed.
Never modifies models/seat-per-route-v1/, 운행기록표_2026/, CSVs, or trained models.
"""

from __future__ import annotations

import concurrent.futures
import gc
import gzip
import hashlib
import json
import os
import sys
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import boto3
import joblib
import numpy as np
import pandas as pd
from botocore.config import Config

WORK = Path(os.getenv("SEAT_WORK_ROOT", "/work"))
PR_ROOT = WORK / "seat_prediction_per_route_v1"
MR_ROOT = WORK / "seat_prediction_multiroute_v1"
V2_ROOT = WORK / "seat_prediction_m5121_v2"
R2_BASE_PREFIX = os.getenv("SEAT_PREDICT_R2_PREFIX", "predictions/seat-v1").strip().strip("/")
KST = timezone(timedelta(hours=9))

STEP_MINUTES = 5
SLOTS_PER_DAY = 1440 // STEP_MINUTES  # 288
WEEKDAYS = list(range(7))  # 0=Mon .. 6=Sun
SLOT_MINUTES = np.arange(0, 1440, STEP_MINUTES, dtype=np.int16)

for p in (V2_ROOT, MR_ROOT, PR_ROOT):
    if str(p) not in sys.path:
        sys.path.insert(0, str(p))
import seat_v2_pipeline as v2  # noqa: E402
if "multiroute_pipeline" not in sys.modules:
    mr_py = MR_ROOT / "multiroute_pipeline.py"
    if not mr_py.is_file():
        import types
        sys.modules["multiroute_pipeline"] = types.ModuleType("multiroute_pipeline")
import per_route_pipeline as pr  # noqa: E402


def _r2_client() -> tuple[Any, str]:
    env = os.environ
    endpoint = (
        env.get("R2_ENDPOINT_URL")
        or env.get("R2_ENDPOINT")
        or env.get("AWS_ENDPOINT_URL")
        or (f"https://{env['R2_ACCOUNT_ID']}.r2.cloudflarestorage.com" if env.get("R2_ACCOUNT_ID") else "")
    )
    access_key = env.get("R2_ACCESS_KEY_ID") or env.get("AWS_ACCESS_KEY_ID") or ""
    secret_key = env.get("R2_SECRET_ACCESS_KEY") or env.get("AWS_SECRET_ACCESS_KEY") or ""
    bucket = env.get("R2_BUCKET_NAME") or env.get("R2_BUCKET") or "geoseulleong"
    if not (endpoint and access_key and secret_key):
        raise RuntimeError("R2 credentials/endpoint env vars are required")
    client = boto3.client(
        "s3",
        endpoint_url=endpoint,
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        region_name="auto",
        config=Config(signature_version="s3v4", max_pool_connections=32),
    )
    return client, bucket


def build_grid(route_id: str, known_visits: list[tuple[str, int]], minute_slots: np.ndarray) -> pd.DataFrame:
    n_days = len(WEEKDAYS)
    n_visits = len(known_visits)
    n_slots = len(minute_slots)
    per_day = n_visits * n_slots
    total = n_days * per_day

    hours = (minute_slots // 60).astype(np.int16)
    mins = (minute_slots % 60).astype(np.int16)
    mod = minute_slots.astype(np.float32)
    tsin = np.sin(2.0 * np.pi * mod / 1440.0).astype(np.float32)
    tcos = np.cos(2.0 * np.pi * mod / 1440.0).astype(np.float32)

    sids = np.array([str(s) for s, _ in known_visits], dtype=object)
    seqs = np.array([int(q) for _, q in known_visits], dtype=np.int32)
    wk_arr = np.repeat(np.array(WEEKDAYS, dtype=np.int16), per_day)
    dow_f = wk_arr.astype(np.float32)

    return pd.DataFrame(
        {
            "route_id": np.full(total, route_id, dtype=object),
            "station_id": np.tile(np.repeat(sids, n_slots), n_days),
            "sequence": np.tile(np.repeat(seqs, n_slots), n_days),
            "weekday": wk_arr,
            "hour": np.tile(np.tile(hours, n_visits), n_days),
            "minute": np.tile(np.tile(mins, n_visits), n_days),
            "minute_of_day": np.tile(np.tile(mod, n_visits), n_days),
            "is_weekend": (wk_arr >= 5).astype(np.int8),
            "time_sin": np.tile(np.tile(tsin, n_visits), n_days),
            "time_cos": np.tile(np.tile(tcos, n_visits), n_days),
            "dow_sin": np.sin(2.0 * np.pi * dow_f / 7.0).astype(np.float32),
            "dow_cos": np.cos(2.0 * np.pi * dow_f / 7.0).astype(np.float32),
        }
    )


def predict_multiroute_vec(
    grid: pd.DataFrame,
    route_id: str,
    known_visits: list[tuple[str, int]],
    minute_slots: np.ndarray,
    mr_reg_b: dict[str, Any],
    mr_clf_b: dict[str, Any],
    need_reg: bool,
    need_clf: bool,
) -> tuple[np.ndarray | None, np.ndarray | None]:
    n_days, n_visits, n_slots = len(WEEKDAYS), len(known_visits), len(minute_slots)
    total = len(grid)
    route_ord_val = float(mr_reg_b["route_encoder"].transform(pd.DataFrame({"route_id": [route_id]})).ravel()[0])
    st_ords = (
        mr_reg_b["station_encoder"]
        .transform(pd.DataFrame({"station_id": [str(s) for s, _ in known_visits]}))
        .ravel()
        .astype(np.float32)
    )
    g_mean, g_low = mr_reg_b["global_priors"]
    r_mean, r_low = mr_reg_b["route_priors"].get(route_id, (g_mean, g_low))
    vp, vhp = mr_reg_b["visit_priors"], mr_reg_b["vh_priors"]
    slot_hours = (minute_slots // 60).astype(int)

    vm_arr = np.empty(n_visits, dtype=np.float32)
    vl_arr = np.empty(n_visits, dtype=np.float32)
    hm_day = [np.empty((n_visits, n_slots), dtype=np.float32) for _ in (0, 1)]
    hl_day = [np.empty((n_visits, n_slots), dtype=np.float32) for _ in (0, 1)]
    for vi, (sid, seq) in enumerate(known_visits):
        vm, vl = vp.get((route_id, str(sid), int(seq)), (r_mean, r_low))
        vm_arr[vi], vl_arr[vi] = vm, vl
        for w in (0, 1):
            h_m = np.array([vhp.get((route_id, str(sid), int(seq), w, hr), (vm, vl))[0] for hr in range(24)], dtype=np.float32)
            h_l = np.array([vhp.get((route_id, str(sid), int(seq), w, hr), (vm, vl))[1] for hr in range(24)], dtype=np.float32)
            hm_day[w][vi] = h_m[slot_hours]
            hl_day[w][vi] = h_l[slot_hours]

    feat_df = grid.copy()
    feat_df["route_ord"] = np.full(total, route_ord_val, dtype=np.float32)
    feat_df["station_ord"] = np.tile(np.repeat(st_ords, n_slots), n_days)
    feat_df["prior_visit_mean"] = np.tile(np.repeat(vm_arr, n_slots), n_days)
    feat_df["prior_visit_low_rate"] = np.tile(np.repeat(vl_arr, n_slots), n_days)
    feat_df["prior_vh_mean"] = np.concatenate([hm_day[1 if wd >= 5 else 0].reshape(-1) for wd in WEEKDAYS])
    feat_df["prior_vh_low_rate"] = np.concatenate([hl_day[1 if wd >= 5 else 0].reshape(-1) for wd in WEEKDAYS])

    rp = np.clip(mr_reg_b["model"].predict(feat_df[mr_reg_b["features"]]), 0.0, None) if need_reg else None
    cp = np.clip(mr_clf_b["model"].predict_proba(feat_df[mr_clf_b["features"]])[:, 1], 0.0, 1.0) if need_clf else None
    return rp, cp


def infer_route_slots(
    rid: str,
    entry: dict[str, Any],
    minute_slots: np.ndarray,
    mr_reg_b: dict[str, Any],
    mr_clf_b: dict[str, Any],
    v2_reg_b: dict[str, Any],
    v2_clf_b: dict[str, Any],
    pr_bundle: dict[str, Any] | None = None,
) -> tuple[list[tuple[str, int]], np.ndarray, np.ndarray | None]:
    if pr_bundle is None and entry.get("artifact"):
        pr_bundle = joblib.load(PR_ROOT / entry["artifact"])
    kv = [(str(s), int(q)) for s, q in (pr_bundle["known_visits"] if pr_bundle is not None else entry["known_visits"])]
    grid = build_grid(rid, kv, minute_slots)

    sel_reg = entry["selected_regressor"]
    sel_clf = entry["selected_classifier"]
    clf_v = bool(entry.get("classifier_verified"))

    need_mr_reg = sel_reg == "multiroute"
    need_mr_clf = clf_v and sel_clf == "multiroute"
    mr_r, mr_c = (
        predict_multiroute_vec(grid, rid, kv, minute_slots, mr_reg_b, mr_clf_b, need_mr_reg, need_mr_clf)
        if (need_mr_reg or need_mr_clf)
        else (None, None)
    )

    if sel_reg.startswith("per_route_") and pr_bundle is not None:
        est = pr_bundle["regressor"].predict(grid[pr_bundle["features"]])
    elif sel_reg == "m5121_v2":
        est = v2_reg_b["model"].predict(grid[v2_reg_b["features"]])
    elif sel_reg == "multiroute":
        est = mr_r
    else:
        raise RuntimeError(f"Unknown regressor {sel_reg} for route {rid}")

    seats_u8 = np.clip(np.rint(est), 0, 255).astype(np.uint8)

    prob_u16: np.ndarray | None = None
    if clf_v:
        if sel_clf == "per_route_hgb" and pr_bundle is not None and pr_bundle.get("classifier") is not None:
            prob = pr_bundle["classifier"].predict_proba(grid[pr_bundle["features"]])[:, 1]
        elif sel_clf == "m5121_v2":
            prob = v2_clf_b["model"].predict_proba(grid[v2_clf_b["features"]])[:, 1]
        elif sel_clf == "multiroute":
            prob = mr_c
        else:
            prob = None
        if prob is not None:
            prob_u16 = np.rint(np.clip(prob, 0.0, 1.0) * 10000.0).astype("<u2")

    return kv, seats_u8, prob_u16


def pack_route_bin_gz(
    rid: str,
    entry: dict[str, Any],
    kv: list[tuple[str, int]],
    seats_u8: np.ndarray,
    prob_u16: np.ndarray | None,
) -> bytes:
    header = {
        "v": 2,
        "route_id": rid,
        "model_type": entry["model_type"],
        "model_version": pr.MODEL_VERSION,
        "classifier_verified": prob_u16 is not None,
        "classifier_threshold": entry.get("classifier_threshold"),
        "weekdays": 7,
        "step_minutes": STEP_MINUTES,
        "slots_per_day": SLOTS_PER_DAY,
        "visits": kv,
    }
    raw = (
        json.dumps(header, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
        + b"\n"
        + seats_u8.tobytes()
        + (prob_u16.tobytes() if prob_u16 is not None else b"")
    )
    return gzip.compress(raw, compresslevel=6)


def verify_1m_vs_5m_parity(
    routes: dict[str, Any],
    mr_reg_b: dict[str, Any],
    mr_clf_b: dict[str, Any],
    v2_reg_b: dict[str, Any],
    v2_clf_b: dict[str, Any],
) -> dict[str, Any]:
    all_1m = np.arange(0, 1440, dtype=np.int16)
    raw_slot = (all_1m + 2) // 5
    wrap = raw_slot >= 288
    slot_idx = np.where(wrap, 0, raw_slot)

    report = {}
    for label, rid in [("M5121", "234001317"), ("1200", "216000070"), ("7000", "200000112")]:
        e = routes[rid]
        pr_b = joblib.load(PR_ROOT / e["artifact"]) if e.get("artifact") else None
        kv, s1, p1 = infer_route_slots(rid, e, all_1m, mr_reg_b, mr_clf_b, v2_reg_b, v2_clf_b, pr_bundle=pr_b)
        _, s5, p5 = infer_route_slots(rid, e, SLOT_MINUTES, mr_reg_b, mr_clf_b, v2_reg_b, v2_clf_b, pr_bundle=pr_b)

        n_v = len(kv)
        s1_3d = s1.reshape(7, n_v, 1440).astype(int)
        s5_3d = s5.reshape(7, n_v, 288).astype(int)

        mapped_s5 = np.empty_like(s1_3d)
        for wd in range(7):
            next_wd = (wd + 1) % 7
            mapped_s5[wd][:, ~wrap] = s5_3d[wd][:, slot_idx[~wrap]]
            mapped_s5[wd][:, wrap] = s5_3d[next_wd][:, slot_idx[wrap]]

        seat_diff = np.abs(s1_3d - mapped_s5)
        entry_rep: dict[str, Any] = {
            "route_id": rid,
            "model_type": e["model_type"],
            "seat_exact_match_pct": round(float((seat_diff == 0).mean() * 100), 2),
            "seat_within_1_pct": round(float((seat_diff <= 1).mean() * 100), 2),
            "seat_mae_diff": round(float(seat_diff.mean()), 4),
        }

        if p1 is not None and p5 is not None:
            p1_3d = p1.reshape(7, n_v, 1440).astype(np.float64) / 10000.0
            p5_3d = p5.reshape(7, n_v, 288).astype(np.float64) / 10000.0
            mapped_p5 = np.empty_like(p1_3d)
            for wd in range(7):
                next_wd = (wd + 1) % 7
                mapped_p5[wd][:, ~wrap] = p5_3d[wd][:, slot_idx[~wrap]]
                mapped_p5[wd][:, wrap] = p5_3d[next_wd][:, slot_idx[wrap]]

            prob_diff = np.abs(p1_3d - mapped_p5)
            thresh = float(e.get("classifier_threshold") or 0.20)
            risk_1m = p1_3d >= thresh
            risk_5m = mapped_p5 >= thresh
            entry_rep.update(
                {
                    "classifier_threshold": thresh,
                    "prob_mae_diff": round(float(prob_diff.mean()), 5),
                    "prob_p95_diff": round(float(np.quantile(prob_diff, 0.95)), 5),
                    "risk_class_agreement_pct": round(float((risk_1m == risk_5m).mean() * 100), 2),
                }
            )

        v0_sid, v0_seq = ("203000294", 6) if rid == "234001317" else kv[0]
        vi0 = kv.index((v0_sid, v0_seq))
        pr.OUT_ROOT = PR_ROOT
        pr.MR_ROOT = MR_ROOT
        pr.V2_ROOT = V2_ROOT
        live = pr.predict_seats(rid, v0_sid, "2026-11-25T08:20:00+09:00", stop_sequence=v0_seq, artifact_dir=PR_ROOT)
        wd_wed = 2
        sl_0820 = (8 * 60 + 20) // 5
        pre_s = int(s5_3d[wd_wed, vi0, sl_0820])
        pre_p = round(int(p5.reshape(7, n_v, 288)[wd_wed, vi0, sl_0820]) / 10000.0, 4) if p5 is not None else None
        assert pre_s == live["estimated_remaining_seats"] and pre_p == live["low_seat_probability"], (
            f"Exact 5m slot parity mismatch on {label}: pre=({pre_s},{pre_p}) vs live={live}"
        )
        entry_rep["exact_0820_live_match"] = True
        entry_rep["sample_0820"] = {"seats": pre_s, "low_prob": pre_p}
        report[label] = entry_rep
    return report


def run_precompute_v2(upload: bool = True, verify_parity: bool = True) -> dict[str, Any]:
    t0 = time.monotonic()
    now_kst = datetime.now(KST)
    run_id = now_kst.strftime("%Y%m%dT%H%M%SZ")
    run_prefix = f"{R2_BASE_PREFIX}/runs/{run_id}"

    reg_doc = json.loads((PR_ROOT / "model_registry.json").read_text(encoding="utf-8"))
    routes = reg_doc["routes"]
    model_version = reg_doc.get("model_version", pr.MODEL_VERSION)
    avail_routes = sorted(rid for rid, e in routes.items() if e.get("status") == "available")

    mr_reg_b = joblib.load(MR_ROOT / "multiroute_regressor.joblib")
    mr_clf_b = joblib.load(MR_ROOT / "multiroute_classifier.joblib")
    v2_reg_b = joblib.load(V2_ROOT / "selected_regressor.joblib")
    v2_clf_b = joblib.load(V2_ROOT / "low_seat_classifier.joblib")

    parity = verify_1m_vs_5m_parity(routes, mr_reg_b, mr_clf_b, v2_reg_b, v2_clf_b) if verify_parity else None

    t_gen0 = time.monotonic()
    file_hashes: dict[str, str] = {}
    upload_items: list[tuple[str, bytes]] = []
    total_bytes = 0

    for idx, rid in enumerate(avail_routes, 1):
        entry = routes[rid]
        kv, seats_u8, prob_u16 = infer_route_slots(rid, entry, SLOT_MINUTES, mr_reg_b, mr_clf_b, v2_reg_b, v2_clf_b)
        gz = pack_route_bin_gz(rid, entry, kv, seats_u8, prob_u16)
        rel_key = f"routes/{rid}.bin.gz"
        file_hashes[rel_key] = hashlib.sha256(gz).hexdigest()
        total_bytes += len(gz)
        if upload:
            upload_items.append((f"{run_prefix}/{rel_key}", gz))
        if idx % 70 == 0:
            gc.collect()

    gen_seconds = round(time.monotonic() - t_gen0, 2)

    agg_h = hashlib.sha256()
    for k in sorted(file_hashes):
        agg_h.update(f"{k}:{file_hashes[k]}\n".encode("utf-8"))
    checksum = agg_h.hexdigest()

    manifest = {
        "schema": "weekday-5m-v2",
        "run_id": run_id,
        "model_version": model_version,
        "generated_at": now_kst.isoformat(),
        "weekdays": 7,
        "step_minutes": STEP_MINUTES,
        "slots_per_day": SLOTS_PER_DAY,
        "route_count": len(avail_routes),
        "file_count": len(file_hashes),
        "total_compressed_bytes": total_bytes,
        "checksum_sha256": checksum,
        "supported_routes": avail_routes,
    }

    upload_seconds = 0.0
    if upload:
        t_up0 = time.monotonic()
        client, bucket = _r2_client()

        def _put(item: tuple[str, bytes]) -> None:
            key, body = item
            client.put_object(
                Bucket=bucket,
                Key=key,
                Body=body,
                ContentType="application/octet-stream",
                ContentEncoding="gzip",
            )

        with concurrent.futures.ThreadPoolExecutor(max_workers=16) as ex:
            list(ex.map(_put, upload_items))

        manifest_key = f"{run_prefix}/manifest.json"
        client.put_object(
            Bucket=bucket,
            Key=manifest_key,
            Body=(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n").encode("utf-8"),
            ContentType="application/json",
        )

        latest = {
            "schema": "weekday-5m-v2",
            "run_id": run_id,
            "model_version": model_version,
            "generated_at": manifest["generated_at"],
            "weekdays": 7,
            "step_minutes": STEP_MINUTES,
            "slots_per_day": SLOTS_PER_DAY,
            "prefix": run_prefix,
            "manifest_key": manifest_key,
            "checksum_sha256": checksum,
            "route_count": len(avail_routes),
            "supported_routes": avail_routes,
        }
        client.put_object(
            Bucket=bucket,
            Key=f"{R2_BASE_PREFIX}/latest.json",
            Body=(json.dumps(latest, ensure_ascii=False, indent=2) + "\n").encode("utf-8"),
            ContentType="application/json",
            CacheControl="no-cache",
        )
        upload_seconds = round(time.monotonic() - t_up0, 2)

    summary = {
        "schema": "weekday-5m-v2",
        "run_id": run_id,
        "model_version": model_version,
        "route_count": len(avail_routes),
        "file_count": len(file_hashes),
        "generation_seconds": gen_seconds,
        "upload_seconds": upload_seconds,
        "total_seconds": round(time.monotonic() - t0, 2),
        "total_compressed_mb": round(total_bytes / (1024 * 1024), 2),
        "avg_file_kb": round((total_bytes / max(1, len(file_hashes))) / 1024.0, 2),
        "r2_uploaded": upload,
        "r2_prefix": run_prefix,
        "latest_key": f"{R2_BASE_PREFIX}/latest.json",
        "checksum_sha256": checksum,
        "parity_1m_vs_5m": parity,
    }
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return summary


if __name__ == "__main__":
    run_precompute_v2(
        upload=os.getenv("SEAT_PRECOMPUTE_UPLOAD", "true").lower() == "true",
        verify_parity=os.getenv("SEAT_PRECOMPUTE_VERIFY_PARITY", "true").lower() == "true",
    )
