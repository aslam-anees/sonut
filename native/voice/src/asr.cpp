#include "asr.hpp"

#include <exception>

namespace atcode::voice {

bool ASRSession::load(const std::string & model_path, const std::string & vad_model_path, engine::core::BackendType backend, std::string & error) {
    try {
        engine::runtime::ModelLoadRequest request;
        request.model_path = model_path;
        request.family_hint = "qwen3_asr";
        auto registry = engine::runtime::make_default_registry();
        model_ = registry.load(request);
        engine::runtime::TaskSpec task{engine::runtime::VoiceTaskKind::Asr, engine::runtime::RunMode::Streaming};
        engine::runtime::SessionOptions options;
        options.backend.type = backend;
        options.backend.threads = 4;
        options.options["qwen3_asr.vad_model_path"] = vad_model_path;
        session_base_ = model_->create_task_session(task, options);
        session_ = dynamic_cast<engine::runtime::IStreamingVoiceTaskSession *>(session_base_.get());
        if (session_ == nullptr) throw std::runtime_error("Qwen3-ASR did not create a streaming session");
        engine::runtime::SessionPreparationRequest preparation;
        preparation.audio = engine::runtime::AudioPreparationContract{16000, 1, 120 * 16000};
        session_->prepare(preparation);
        return true;
    } catch (const std::exception & exception) {
        error = exception.what();
        model_.reset(); session_base_.reset(); session_ = nullptr;
        return false;
    }
}

bool ASRSession::start(const std::string & language, std::string & error) {
    try {
        if (session_ == nullptr) throw std::runtime_error("ASR model is not loaded");
        engine::runtime::TaskRequest request;
        request.text_input = engine::runtime::Transcript{"", language == "auto" ? "" : language};
        session_->start_stream(request);
        started_ = true;
        return true;
    } catch (const std::exception & exception) { error = exception.what(); return false; }
}

bool ASRSession::process(const std::vector<float> & samples, std::string & partial, std::string & language, std::string & error) {
    try {
        if (!started_ || session_ == nullptr) throw std::runtime_error("ASR stream is not active");
        engine::runtime::AudioChunk chunk;
        chunk.sample_rate = 16000;
        chunk.channels = 1;
        chunk.samples = samples;
        auto event = session_->process_audio_chunk(chunk);
        if (event.partial_text.has_value()) {
            partial = event.partial_text->text;
            language = event.partial_text->language;
            return true;
        }
        return false;
    } catch (const std::exception & exception) { error = exception.what(); return false; }
}

bool ASRSession::finish(std::string & text, std::string & language, std::string & error) {
    try {
        if (!started_ || session_ == nullptr) throw std::runtime_error("ASR stream is not active");
        auto result = session_->finish_stream();
        started_ = false;
        if (result.text_output.has_value()) {
            text = result.text_output->text;
            language = result.text_output->language;
        }
        return true;
    } catch (const std::exception & exception) { error = exception.what(); return false; }
}

void ASRSession::cancel() {
    if (session_ != nullptr) {
        try { session_->reset(); } catch (...) {}
    }
    started_ = false;
}

}  // namespace atcode::voice
