# syntax=docker/dockerfile:1
# DiscoDrive face service: YuNet (faces) + AuraFace (embeddings), CPU only. The weights are
# fetched at build time from pinned revisions and checked against their SHA-256; see NOTICE.md
# for their licences.
FROM scratch AS models
ADD --chmod=0444 --checksum=sha256:ebafce4e3c118d6554634be5c27ab333b4c047a9a8c3faf1d7cf93101c22f0f0 \
    https://github.com/opencv/opencv_zoo/raw/26cc381e4d2594bb9f47a26eb8fd96c94a13660d/models/face_detection_yunet/face_detection_yunet_2026may.onnx \
    /models/yunet.onnx
# Only glintr100.onnx of fal/AuraFace-v1 is fal's own (Apache-2.0); the other files of that
# repository are InsightFace's non-commercial models and are not taken.
ADD --chmod=0444 --checksum=sha256:a7933ea5330113b01c9b60351d8f4c33003f145d8470ac5f0e52ee2effe25c60 \
    https://huggingface.co/fal/AuraFace-v1/resolve/af6d057c9b0ec4071d4c49c80e3539258798b609/glintr100.onnx \
    /models/auraface.onnx
# Their licence texts go with them (MIT and Apache-2.0 ask for it).
ADD --chmod=0444 --checksum=sha256:c83b8120c50ccbd4c4f96edf53141bdd566ebb8f8e9227e415326aa1b1aba958 \
    https://raw.githubusercontent.com/opencv/opencv_zoo/26cc381e4d2594bb9f47a26eb8fd96c94a13660d/models/face_detection_yunet/LICENSE \
    /models/LICENSE-yunet.txt
ADD --chmod=0444 --checksum=sha256:609e2cb599f84aaa41d8ef29d8fdb04d164fab22e8d9292ca34a599d0f56a338 \
    https://huggingface.co/fal/AuraFace-v1/resolve/af6d057c9b0ec4071d4c49c80e3539258798b609/LICENSE.md \
    /models/LICENSE-auraface.md

FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 ML_MODELS=/models OPENCV_IO_MAX_IMAGE_PIXELS=50000000
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir --root-user-action=ignore -r requirements.txt
COPY --from=models /models /models
COPY faces.py server.py test_server.py NOTICE.md ./
RUN python -m unittest -q test_server && rm test_server.py && \
    useradd --system --no-create-home --uid 10001 ml
USER ml
EXPOSE 3003
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:3003/ping', timeout=4)"
ENTRYPOINT ["python", "server.py"]
