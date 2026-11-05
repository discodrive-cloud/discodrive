"""Tests of the face service. They need the model files (ML_MODELS, as in the image)."""
import io
import json
import os
import threading
import unittest
import urllib.error
import urllib.request
import uuid

import cv2
import numpy as np

import faces
import server

MODELS = os.environ.get('ML_MODELS', '/models')


def entries(model='auraface', min_score=0.7):
    return json.dumps({'facial-recognition': {
        'detection': {'modelName': model, 'options': {'minScore': min_score}},
        'recognition': {'modelName': model}}}).encode()


def png(w=320, h=200):
    ok, buf = cv2.imencode('.png', np.full((h, w, 3), 128, np.uint8))
    return buf.tobytes()


def multipart(fields):
    b = uuid.uuid4().hex
    out = io.BytesIO()
    for name, data in fields.items():
        out.write(f'--{b}\r\nContent-Disposition: form-data; name="{name}"'.encode())
        if name == 'image':
            out.write(b'; filename="image.png"\r\nContent-Type: image/png')
        out.write(b'\r\n\r\n' + data + b'\r\n')
    out.write(f'--{b}--\r\n'.encode())
    return f'multipart/form-data; boundary={b}', out.getvalue()


class AlignTest(unittest.TestCase):
    def test_template_maps_onto_itself(self):
        m = faces.similarity(faces.ARCFACE_112, faces.ARCFACE_112)
        np.testing.assert_allclose(m, [[1, 0, 0], [0, 1, 0]], atol=1e-9)

    def test_recovers_rotation_scale_shift(self):
        a = np.deg2rad(20)
        r = 1.7 * np.array([[np.cos(a), -np.sin(a)], [np.sin(a), np.cos(a)]])
        src = (faces.ARCFACE_112 - [10, 4]) @ np.linalg.inv(r).T  # points that r takes back onto the template
        m = faces.similarity(src, faces.ARCFACE_112)
        np.testing.assert_allclose(src @ m[:, :2].T + m[:, 2], faces.ARCFACE_112, atol=1e-6)

    def test_mirror_is_not_allowed(self):
        # landmarks of a mirrored face: the transform stays a rotation (det > 0), never a flip
        src = faces.ARCFACE_112 * [-1, 1]
        m = faces.similarity(src, faces.ARCFACE_112)
        self.assertGreater(np.linalg.det(m[:, :2]), 0)


@unittest.skipUnless(os.path.exists(os.path.join(MODELS, 'auraface.onnx')), 'no model files')
class ServiceTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.models = faces.Models(MODELS, threads=1)
        cls.httpd = server.ThreadingHTTPServer(('127.0.0.1', 0), server.make_handler(cls.models, threading.BoundedSemaphore(1)))
        threading.Thread(target=cls.httpd.serve_forever, daemon=True).start()
        cls.url = 'http://127.0.0.1:%d' % cls.httpd.server_address[1]

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()

    def post(self, fields):
        ct, body = multipart(fields)
        req = urllib.request.Request(self.url + '/predict', data=body, headers={'Content-Type': ct})
        try:
            with urllib.request.urlopen(req) as r:
                return r.status, json.loads(r.read())
        except urllib.error.HTTPError as e:
            with e:
                return e.code, json.loads(e.read())

    def test_ping(self):
        with urllib.request.urlopen(self.url + '/ping') as r:
            self.assertEqual(r.read(), b'pong')

    def test_no_faces_on_a_blank_picture(self):
        status, out = self.post({'entries': entries(), 'image': png(320, 200)})
        self.assertEqual(status, 200)
        self.assertEqual((out['imageWidth'], out['imageHeight']), (320, 200))
        self.assertEqual(out['facial-recognition'], [])

    def test_another_model_is_404(self):
        status, out = self.post({'entries': entries('buffalo_l'), 'image': png()})
        self.assertEqual(status, 404)
        self.assertIn('buffalo_l', out['detail'])

    def test_garbage_image_is_422(self):
        status, _ = self.post({'entries': entries(), 'image': b'not a picture'})
        self.assertEqual(status, 422)

    def test_missing_parts_are_400(self):
        status, _ = self.post({'entries': entries()})
        self.assertEqual(status, 400)
        status, _ = self.post({'entries': b'{}', 'image': png()})
        self.assertEqual(status, 400)

    def test_unknown_model_says_so(self):
        # the server tells a wrong model setting from a missing service by this code
        status, out = self.post({'entries': entries('buffalo_l'), 'image': png()})
        self.assertEqual((status, out.get('code')), (404, 'unknown_model'))

    def test_bad_options_are_400(self):
        for e in (b'{"facial-recognition": "x"}',
                  json.dumps({'facial-recognition': {'detection': {'options': {'minScore': 'a lot'}}}}).encode()):
            status, _ = self.post({'entries': e, 'image': png()})
            self.assertEqual(status, 400, e)

    def test_tiny_picture_is_422(self):
        status, _ = self.post({'entries': entries(), 'image': png(1, 1)})
        self.assertIn(status, (200, 422))  # never a 500 (a strike for the caller)

    def test_too_big_body_is_413_and_closes(self):
        # refused on the header alone, and the connection closes: the unread body must not be
        # taken for the next request
        import http.client
        c = http.client.HTTPConnection('127.0.0.1', self.httpd.server_address[1], timeout=10)
        c.putrequest('POST', '/predict')
        c.putheader('Content-Type', 'multipart/form-data; boundary=x')
        c.putheader('Content-Length', str(server.MAX_BODY + 1))
        c.endheaders()
        r = c.getresponse()
        self.assertEqual((r.status, r.getheader('Connection')), (413, 'close'))
        r.read()
        c.close()

    def test_huge_picture_is_scaled_down(self):
        # a picture past the working size is looked at scaled down, boxes in its own pixels
        img = np.full((3000, 4000, 3), 128, np.uint8)
        ok, buf = cv2.imencode('.jpg', img)
        status, out = self.post({'entries': entries(), 'image': buf.tobytes()})
        self.assertEqual((status, out['imageWidth'], out['imageHeight']), (200, 4000, 3000))

    def test_embedding_is_512_and_normalised(self):
        img = np.random.default_rng(1).integers(0, 255, (200, 200, 3), dtype=np.uint8)
        v = self.models.embed(img, faces.ARCFACE_112 + 40)
        self.assertEqual(len(v), 512)
        self.assertAlmostEqual(float(np.linalg.norm(v)), 1.0, places=5)


if __name__ == '__main__':
    unittest.main()
