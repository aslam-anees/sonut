# Voice runtime and model notices

The native helper links audio.cpp at the pinned revision
`238ab6a9e321c17de8e120559f57efeedaeb1345` under Apache License 2.0. Preserve
the audio.cpp Apache-2.0 notice and license when distributing the helper.

The helper uses miniaudio 0.11.22, distributed under the MIT-0 / MIT-style
license in its upstream header. Preserve the upstream notice.

The default models require separate review of their model-card terms:

- Qwen3-ASR 0.6B: Apache-2.0, from `Qwen/Qwen3-ASR-0.6B`.
- Supertonic 3: OpenRAIL, from `Supertone/supertonic-3`.
- Silero VAD 16 kHz weights: MIT, from the audio.cpp bundled asset.

Model licenses are not interchangeable with the helper's source license. A
release job must ship the corresponding upstream notices and must not imply
that OpenRAIL model terms are Apache-2.0.
