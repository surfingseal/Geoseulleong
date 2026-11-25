"""Download and verify versioned seat-prediction model bundle from Cloudflare R2.

Uses environment variables only; never hardcodes or prints credentials.
Supports optional SEAT_MODEL_EXPECTED_SHA256 pinning and skips redundant downloads
when the local bundle manifest already matches the verified SHA256.
"""

from __future__ import annotations

import hashlib
import json
import os
import tarfile
from pathlib import Path

import boto3
from botocore.config import Config


def _r2_endpoint() -> str:
    endpoint = os.getenv("R2_ENDPOINT_URL") or os.getenv("R2_ENDPOINT") or ""
    if endpoint:
        return endpoint.strip()
    account_id = os.getenv("R2_ACCOUNT_ID", "").strip()
    if account_id:
        return f"https://{account_id}.r2.cloudflarestorage.com"
    raise RuntimeError("R2_ENDPOINT_URL or R2_ACCOUNT_ID must be set")


def fetch_models(target_dir: Path | None = None) -> dict:
    dest = (target_dir or Path(os.getenv("SEAT_MODEL_ROOT", str(Path(__file__).parent / "artifacts")))).resolve()
    dest.mkdir(parents=True, exist_ok=True)

    access_key = os.getenv("R2_ACCESS_KEY_ID", "").strip()
    secret_key = os.getenv("R2_SECRET_ACCESS_KEY", "").strip()
    bucket = os.getenv("R2_BUCKET_NAME", "geoseulleong").strip()
    prefix = os.getenv("SEAT_MODEL_R2_PREFIX", "models/seat-per-route-v1").strip().strip("/")
    pinned_sha = os.getenv("SEAT_MODEL_EXPECTED_SHA256", "").strip().lower()
    if not access_key or not secret_key:
        raise RuntimeError("R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY must be set")

    local_manifest = dest / "manifest.json"
    local_registry = dest / "seat_prediction_per_route_v1" / "model_registry.json"
    if pinned_sha and local_manifest.is_file() and local_registry.is_file():
        cached = json.loads(local_manifest.read_text(encoding="utf-8"))
        if str(cached.get("bundle_sha256", "")).lower() == pinned_sha:
            return cached

    client = boto3.client(
        "s3",
        endpoint_url=_r2_endpoint(),
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        region_name="auto",
        config=Config(signature_version="s3v4"),
    )

    manifest_obj = client.get_object(Bucket=bucket, Key=f"{prefix}/manifest.json")
    manifest = json.loads(manifest_obj["Body"].read().decode("utf-8"))
    manifest_sha = str(manifest.get("bundle_sha256", "")).strip().lower()
    if not manifest_sha:
        raise RuntimeError("Remote manifest.json missing bundle_sha256")
    if pinned_sha and manifest_sha != pinned_sha:
        raise RuntimeError(f"Remote manifest SHA256 mismatch: pinned {pinned_sha}, remote {manifest_sha}")

    if local_manifest.is_file() and local_registry.is_file():
        cached = json.loads(local_manifest.read_text(encoding="utf-8"))
        if str(cached.get("bundle_sha256", "")).lower() == manifest_sha:
            return cached

    bundle_path = dest / "bundle.tar.gz"
    client.download_file(bucket, f"{prefix}/bundle.tar.gz", str(bundle_path))

    h = hashlib.sha256()
    with bundle_path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    actual_sha = h.hexdigest().lower()
    if actual_sha != manifest_sha:
        bundle_path.unlink(missing_ok=True)
        raise RuntimeError(f"Bundle SHA256 mismatch: expected {manifest_sha}, got {actual_sha}")

    with tarfile.open(bundle_path, "r:gz") as tar:
        tar.extractall(path=dest, filter="data")
    bundle_path.unlink(missing_ok=True)
    local_manifest.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return manifest


if __name__ == "__main__":
    m = fetch_models()
    print(json.dumps({"status": "ready", "model_version": m.get("model_version"), "available_routes": m.get("available_routes"), "bundle_sha256": m.get("bundle_sha256")}))
