#!/usr/bin/env python3
"""create-buckets.py —— minio/S3 建 langfuse 两桶（langfuse + langfuse-events）。

SigV4 手工签名 PUT，零依赖（不装 boto3/mc；minio/mc 镜像 2026-09 起已从
Docker Hub 下架）。幂等：409=已存在视为成功。

用法：python3 create-buckets.py [host:port] （缺省 localhost:19000；凭据
minio/miniosecret 与 compose 一致——内网占位，生产改 compose 后同步改这里）
"""
import hashlib, hmac, datetime, sys, urllib.request, urllib.error

ACCESS, SECRET, REGION = "minio", "miniosecret", "us-east-1"


def put_bucket(host: str, bucket: str) -> None:
    now = datetime.datetime.now(datetime.timezone.utc)
    date, amz = now.strftime("%Y%m%d"), now.strftime("%Y%m%dT%H%M%SZ")
    payload = hashlib.sha256(b"").hexdigest()
    canon = (f"PUT\n/{bucket}\n\nhost:{host}\nx-amz-content-sha256:{payload}\n"
             f"x-amz-date:{amz}\n\nhost;x-amz-content-sha256;x-amz-date\n{payload}")
    scope = f"{date}/{REGION}/s3/aws4_request"
    sts = f"AWS4-HMAC-SHA256\n{amz}\n{scope}\n{hashlib.sha256(canon.encode()).hexdigest()}"

    def k(sk: bytes, msg: str) -> bytes:
        return hmac.new(sk, msg.encode(), hashlib.sha256).digest()

    key = k(k(k(k(("AWS4" + SECRET).encode(), date), REGION), "s3"), "aws4_request")
    sig = hmac.new(key, sts.encode(), hashlib.sha256).hexdigest()
    req = urllib.request.Request(f"http://{host}/{bucket}", data=b"", method="PUT", headers={
        "Host": host,
        "Authorization": (f"AWS4-HMAC-SHA256 Credential={ACCESS}/{scope}, "
                          f"SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature={sig}"),
        "x-amz-content-sha256": payload, "x-amz-date": amz})
    try:
        with urllib.request.urlopen(req) as r:
            print(f"{bucket} → {r.status}")
    except urllib.error.HTTPError as e:
        if e.code == 409:
            print(f"{bucket} → 已存在（幂等成功）")
        else:
            raise SystemExit(f"{bucket} 建桶失败: {e.code} {e.read()[:120]}")


if __name__ == "__main__":
    host = sys.argv[1] if len(sys.argv) > 1 else "localhost:19000"
    for b in ("langfuse", "langfuse-events"):
        put_bucket(host, b)
