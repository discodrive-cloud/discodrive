"""Face detection (YuNet) and face embeddings (AuraFace) for the DiscoDrive gallery."""
import os
import threading

# A picture past 50 Mpx is refused by the decoder instead of taking gigabytes (set before cv2).
os.environ.setdefault('OPENCV_IO_MAX_IMAGE_PIXELS', str(50_000_000))

import cv2  # noqa: E402
import numpy as np
import onnxruntime as ort

# The 5-point template of ArcFace-style recognisers on a 112×112 crop: the person's right eye,
# left eye, nose tip, right and left mouth corners (as YuNet orders its landmarks).
ARCFACE_112 = np.array([
    [38.2946, 51.6963], [73.5318, 51.5014], [56.0252, 71.7366],
    [41.5493, 92.3655], [70.7299, 92.2041]], dtype=np.float64)

# Long side of the second detection pass: YuNet finds faces of about 10–300 px, so a selfie's
# face is found on the scaled-down copy and a far one on the full picture.
SMALL_SIDE = 640
NMS_IOU = 0.4


def similarity(src, dst):
    """The similarity transform (rotation, uniform scale, shift) taking src points onto dst in
    the least-squares sense (Umeyama 1991), as a 2×3 matrix."""
    n = src.shape[0]
    mu_s, mu_d = src.mean(0), dst.mean(0)
    s, d = src - mu_s, dst - mu_d
    cov = d.T @ s / n
    u, sig, vt = np.linalg.svd(cov)
    e = np.eye(2)
    if np.linalg.det(u) * np.linalg.det(vt) < 0:
        e[1, 1] = -1
    r = u @ e @ vt
    scale = (sig * np.diag(e)).sum() / (s ** 2).sum() * n
    t = mu_d - scale * r @ mu_s
    return np.hstack([scale * r, t[:, None]])


def align(img, landmarks):
    """The 112×112 crop of a face, its 5 landmarks put onto the ArcFace template."""
    m = similarity(np.asarray(landmarks, dtype=np.float64).reshape(5, 2), ARCFACE_112)
    return cv2.warpAffine(img, m, (112, 112), borderValue=0.0)


def iou(a, b):
    ix = min(a[2], b[2]) - max(a[0], b[0])
    iy = min(a[3], b[3]) - max(a[1], b[1])
    if ix <= 0 or iy <= 0:
        return 0.0
    inter = ix * iy
    return inter / ((a[2] - a[0]) * (a[3] - a[1]) + (b[2] - b[0]) * (b[3] - b[1]) - inter)


class Models:
    """YuNet + AuraFace. Safe for concurrent use: OpenCV's detector is one per thread."""

    def __init__(self, model_dir, threads=2):
        self.yunet_path = os.path.join(model_dir, 'yunet.onnx')
        if not os.path.exists(self.yunet_path):
            raise FileNotFoundError(self.yunet_path)
        so = ort.SessionOptions()
        so.intra_op_num_threads = threads
        so.inter_op_num_threads = 1
        self.aura = ort.InferenceSession(os.path.join(model_dir, 'auraface.onnx'), so,
                                         providers=['CPUExecutionProvider'])
        self.aura_in = self.aura.get_inputs()[0].name
        self.local = threading.local()

    def _detector(self):
        d = getattr(self.local, 'det', None)
        if d is None:
            d = cv2.FaceDetectorYN.create(self.yunet_path, '', (320, 320), 0.5, 0.3, 5000)
            self.local.det = d
        return d

    def detect(self, img, min_score):
        """Faces of a BGR picture: [(x1, y1, x2, y2), score, landmarks(5×2)] in pixels."""
        h, w = img.shape[:2]
        det = self._detector()
        det.setScoreThreshold(float(min_score))
        found = []
        scales = [1.0]
        if max(w, h) > SMALL_SIDE * 1.25:
            scales.append(SMALL_SIDE / max(w, h))
        for scale in scales:
            im = img if scale == 1.0 else cv2.resize(img, (round(w * scale), round(h * scale)))  # as measured (research-face-models.md)
            det.setInputSize((im.shape[1], im.shape[0]))
            _, out = det.detect(im)
            for r in out if out is not None else []:
                r = r.astype(np.float64) / np.array([scale] * 14 + [1.0])
                box = (r[0], r[1], r[0] + r[2], r[1] + r[3])
                found.append((box, float(r[14]), r[4:14].reshape(5, 2)))
        found.sort(key=lambda f: -f[1])
        kept = []
        for f in found:
            if all(iou(f[0], k[0]) < NMS_IOU for k in kept):
                kept.append(f)
        return [(tuple(max(0.0, v) for v in (b[0], b[1])) + (min(float(w), b[2]), min(float(h), b[3])), s, lm)
                for b, s, lm in kept]

    def embed(self, img, landmarks):
        """The normalised 512-number embedding of the face with these landmarks."""
        crop = align(img, landmarks)
        x = cv2.cvtColor(crop, cv2.COLOR_BGR2RGB).astype(np.float32)
        x = ((x - 127.5) / 127.5).transpose(2, 0, 1)[None]
        v = self.aura.run(None, {self.aura_in: x})[0].reshape(-1).astype(np.float64)
        n = np.linalg.norm(v)
        return (v / n if n > 0 else v).tolist()
