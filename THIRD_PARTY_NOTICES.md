# Third-party notices and reference boundaries

The repository's original source is distributed under [MIT](LICENSE). Retain
the existing copyright notice. Third-party portions keep their own notices
and terms; the root MIT license does not replace them.

## Source included in this repository

| Origin | Included surface | Notice |
| --- | --- | --- |
| [Zero / Gitlawb](https://github.com/Gitlawb/zero) | Inherited Go terminal-agent foundation; the private pre-release history records the Zero-to-Sonut migration | MIT attribution retained in the root [LICENSE](LICENSE), Copyright 2026 Gitlawb |
| [Tencent WeKnora](https://github.com/Tencent/WeKnora) | Adaptive chunking in `internal/know/chunker`; local changes described in `internal/know/WIRING.md` | [Upstream license and notices](third_party/licenses/WeKnora-LICENSE.txt), copied from the locally inspected upstream checkout |
| [Kun Chen / no-mistakes](https://github.com/kunchenguid/no-mistakes) | Git safety logic in `internal/no`; adapted scope in `internal/no/WIRING.md` | [MIT license](third_party/licenses/no-mistakes-LICENSE.txt), Copyright 2026 Kun Chen |
| [DEEIX-AI / DEEIX-Chat](https://github.com/DEEIX-AI/DEEIX-Chat) | Framework-independent security primitives in `internal/enterprise`; modifications in `internal/enterprise/WIRING.md` | [Apache-2.0 license](third_party/licenses/DEEIX-Chat-LICENSE.txt), Copyright 2026 DEEIX-AI; modified for @code |

Go module dependencies remain identified in `go.mod` and `go.sum`, under their
respective upstream licenses. Native audio dependencies and model terms are
documented separately in [native/voice/LICENSES.md](native/voice/LICENSES.md).

## Design references

The checked-out `ref/` repositories are not shipped as part of this release.
Existing code and documentation identify Witr (process insight), GoDoxy (route
inventory), MiniScira (Knowledge Atlas), and Page Assist (bounded HTML
extraction) as design references. See `docs/REFERENCE_INTEGRATION.md`,
`docs/SEC_PROCESS_INSIGHT.md`, and `docs/SEC_ROUTE_INSIGHT.md` for the documented
implementation boundaries. Memory notes describe an Engram-inspired store/
recall contract reimplemented with local files; the mobile view documents an
OpenCode Manager-inspired interaction concept.

This inventory records evidence found in the source, not a claim that every
reference repository was used or that every file has been independently
compared with upstream. Do not attribute unverified code to a reference.

## Models and runtimes

No model weights, llama.cpp runtime binaries, native voice dependencies, or
private reference checkouts are included in this source release. Downloaded
models and runtimes keep their original authorship and licenses. The
`atcode-v1.gguf` filename is a product-facing name; source comments identify
the profile as Bonsai-derived. Do not describe it as a foundation model
trained from scratch by AISTATINE.
