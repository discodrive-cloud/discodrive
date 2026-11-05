"""DiscoDrive face service: the HTTP contract of the Immich machine-learning container, so the
DiscoDrive server can use either.

    GET  /ping     -> "pong"
    POST /predict  multipart: entries (JSON), image (JPEG/PNG)
        entries = {"facial-recognition": {"detection": {"modelName": "auraface",
                    "options": {"minScore": 0.7}}, "recognition": {"modelName": "auraface"}}}
        -> {"imageWidth": w, "imageHeight": h,
            "facial-recognition": [{"boundingBox": {"x1","y1","x2","y2"}, "score", "embedding"}]}

Another model name is 404 with "code": "unknown_model" (this is not the service the caller
wants: the DiscoDrive server pauses instead of failing photos), a picture that does not decode
is 422, a malformed request is 400, a body past MAX_BODY is 413.
"""
import email.parser
import email.policy
import json
import logging
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from faces import Models  # sets the decoder's pixel limit before cv2 loads

import cv2
import numpy as np

MODEL_NAME = 'auraface'
MAX_BODY = 16 << 20  # the DiscoDrive server sends a 2048 px preview: a few hundred kB
# A bigger picture is looked at scaled to this long side; boxes are given in its own pixels.
WORK_SIDE = 2560
READ_TIMEOUT = 60

log = logging.getLogger('discodrive-ml')


class HTTPError(Exception):
    def __init__(self, status, detail, code=None):
        super().__init__(detail)
        self.status, self.detail, self.code = status, detail, code

    def body(self):
        return {'detail': self.detail, **({'code': self.code} if self.code else {})}


def parse_multipart(content_type, body):
    """The parts of a multipart/form-data body by field name (bytes)."""
    if not content_type.startswith('multipart/form-data'):
        raise HTTPError(400, 'multipart/form-data expected')
    msg = email.parser.BytesParser(policy=email.policy.HTTP).parsebytes(
        b'Content-Type: ' + content_type.encode() + b'\r\n\r\n' + body)
    if not msg.is_multipart():
        raise HTTPError(400, 'bad multipart body')
    parts = {}
    for part in msg.iter_parts():
        name = part.get_param('name', header='content-disposition')
        if name:
            parts[name] = part.get_payload(decode=True) or b''
    return parts


def predict(models, entries_raw, image):
    try:
        task = json.loads(entries_raw)['facial-recognition']
        steps = {s: task.get(s) or {} for s in ('detection', 'recognition')}
        names = {s: steps[s].get('modelName', MODEL_NAME) for s in steps}
        min_score = float((steps['detection'].get('options') or {}).get('minScore', 0.7))
    except (ValueError, KeyError, TypeError, AttributeError):
        raise HTTPError(400, 'entries must ask for facial-recognition')
    for name in names.values():
        if name != MODEL_NAME:
            raise HTTPError(404, f'model {name!r} is not served here (only {MODEL_NAME!r})', 'unknown_model')
    if not 0.3 <= min_score <= 1:  # below that, thousands of "faces" and an embedding each
        raise HTTPError(400, 'minScore must be within 0.3..1')
    img = cv2.imdecode(np.frombuffer(image, np.uint8), cv2.IMREAD_COLOR)
    if img is None:
        raise HTTPError(422, 'the image does not decode')
    h, w = img.shape[:2]
    scale = min(1.0, WORK_SIDE / max(w, h))
    work = img if scale == 1.0 else cv2.resize(img, (round(w * scale), round(h * scale)), interpolation=cv2.INTER_AREA)
    try:
        found = [(box, score, models.embed(work, landmarks)) for box, score, landmarks in models.detect(work, min_score)]
    except cv2.error as e:
        raise HTTPError(422, f'the image cannot be looked at: {e.err}')
    out = []
    for box, score, embedding in found:
        box = [v / scale for v in box]
        out.append({
            'boundingBox': {'x1': round(box[0], 1), 'y1': round(box[1], 1), 'x2': round(box[2], 1), 'y2': round(box[3], 1)},
            'score': round(score, 4),
            'embedding': [round(v, 6) for v in embedding],
        })
    return {'imageWidth': w, 'imageHeight': h, 'facial-recognition': out}


def make_handler(models, slots):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = 'HTTP/1.1'
        timeout = READ_TIMEOUT  # a client that stops sending does not hold a thread for good

        def log_message(self, fmt, *args):
            log.debug('%s %s', self.address_string(), fmt % args)

        def reply(self, status, body, content_type='application/json', close=False):
            data = body if isinstance(body, bytes) else json.dumps(body).encode()
            self.send_response(status)
            self.send_header('Content-Type', content_type)
            self.send_header('Content-Length', str(len(data)))
            if close:  # the body was not read: what is left of it must not pass for a request
                self.send_header('Connection', 'close')
                self.close_connection = True
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            if self.path == '/ping':
                return self.reply(200, b'pong', 'text/plain')
            self.reply(404, {'detail': 'not found'})

        def do_POST(self):
            try:
                if self.path != '/predict':
                    return self.reply(404, {'detail': 'not found'}, close=True)
                try:
                    length = int(self.headers.get('Content-Length') or 0)
                except ValueError:
                    length = -1
                if length <= 0 or length > MAX_BODY:
                    return self.reply(413 if length > MAX_BODY else 400, {'detail': 'bad body size'}, close=True)
                # Bodies are read inside the slots too: a burst of requests cannot hold many of
                # them in memory at once.
                with slots:
                    body = self.rfile.read(length)
                    if len(body) != length:
                        return self.reply(400, {'detail': 'short body'}, close=True)
                    parts = parse_multipart(self.headers.get('Content-Type', ''), body)
                    del body
                    if 'entries' not in parts or 'image' not in parts:
                        raise HTTPError(400, 'entries and image are required')
                    result = predict(models, parts['entries'], parts['image'])
                self.reply(200, result)
            except HTTPError as e:
                self.reply(e.status, e.body())
            except Exception:  # noqa: BLE001 — one bad request must not take the service down
                log.exception('predict failed')
                self.reply(500, {'detail': 'internal error'})

    return Handler


def main():
    logging.basicConfig(level=os.environ.get('ML_LOG', 'INFO'), format='%(asctime)s %(levelname)s %(message)s')
    threads = max(1, int(os.environ.get('ML_THREADS', '2')))
    concurrency = max(1, int(os.environ.get('ML_CONCURRENCY', '2')))
    cv2.setNumThreads(threads)
    models = Models(os.environ.get('ML_MODELS', '/models'), threads=threads)
    server = ThreadingHTTPServer((os.environ.get('ML_HOST', '0.0.0.0'), int(os.environ.get('ML_PORT', '3003'))),
                                 make_handler(models, threading.BoundedSemaphore(concurrency)))
    log.info('discodrive-ml: model %s, %d threads, %d at once, listening on %s:%d',
             MODEL_NAME, threads, concurrency, *server.server_address)
    server.serve_forever()


if __name__ == '__main__':
    main()
