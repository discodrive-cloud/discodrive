# Models in this image

DiscoDrive's own code here is under the licence of the DiscoDrive repository. The image also
carries two model files made by others.

## YuNet — face detection

- File: `/models/yunet.onnx` = `face_detection_yunet_2026may.onnx` from OpenCV Zoo,
  commit `26cc381e4d2594bb9f47a26eb8fd96c94a13660d`, SHA-256
  `ebafce4e3c118d6554634be5c27ab333b4c047a9a8c3faf1d7cf93101c22f0f0`.
- Source: https://github.com/opencv/opencv_zoo/tree/26cc381e4d2594bb9f47a26eb8fd96c94a13660d/models/face_detection_yunet
- Licence: MIT, Copyright (c) 2020 Shiqi Yu; full text in `/models/LICENSE-yunet.txt`.
- Note: according to its authors YuNet was trained on WIDER FACE, a dataset published under
  CC BY-NC-ND 4.0. Whether that dataset licence reaches the trained weights is not settled.

## AuraFace-v1 — face embeddings

- File: `/models/auraface.onnx` = `glintr100.onnx` from https://huggingface.co/fal/AuraFace-v1,
  revision `af6d057c9b0ec4071d4c49c80e3539258798b609`, SHA-256
  `a7933ea5330113b01c9b60351d8f4c33003f145d8470ac5f0e52ee2effe25c60`.
- Licence: Apache License 2.0, by fal.ai; full text in `/models/LICENSE-auraface.md`.
- Note: fal describes the training data only as "commercially available"; it does not name it.
  The other files of that repository (`scrfd_10g_bnkps.onnx`, `2d106det.onnx`, `1k3d68.onnx`,
  `genderage.onnx`) are InsightFace models under a non-commercial licence and are NOT included.
