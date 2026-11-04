#include "tts.hpp"

#include <algorithm>
#include <exception>
#include <functional>

namespace atcode::voice {

bool TTSSession::load(const std::string & model_path, engine::core::BackendType backend, std::string & error) {
    try {
        engine::runtime::ModelLoadRequest request;
        request.model_path = model_path;
        request.family_hint = "supertonic";
        auto registry = engine::runtime::make_default_registry();
        model_ = registry.load(request);
        engine::runtime::TaskSpec task{engine::runtime::VoiceTaskKind::Tts, engine::runtime::RunMode::Streaming};
        engine::runtime::SessionOptions options;
        options.backend.type = backend;
        options.backend.threads = 4;
        session_base_ = model_->create_task_session(task, options);
        session_ = dynamic_cast<engine::runtime::IStreamingVoiceTaskSession *>(session_base_.get());
        if (session_ == nullptr) throw std::runtime_error("Supertonic did not create a streaming session");
        session_->prepare(engine::runtime::SessionPreparationRequest{});
        return true;
    } catch (const std::exception & exception) {
        error = exception.what(); model_.reset(); session_base_.reset(); session_ = nullptr; return false;
    }
}

bool TTSSession::supported_language(const std::string & language) const {
    static const char * languages[] = {"en", "ko", "ja", "ar", "bg", "cs", "da", "de", "el", "es", "et", "fi", "fr", "hi", "hr", "hu", "id", "it", "lt", "lv", "nl", "pl", "pt", "ro", "ru", "sk", "sl", "sv", "tr", "uk", "vi"};
	if (language.empty() || language == "auto") return false;
    const auto dash = language.find('-');
    const auto base = language.substr(0, dash);
    for (const auto * supported : languages) if (base == supported) return true;
    return false;
}

bool TTSSession::synthesize(const std::string & text, const std::string & language, const std::string & voice, double speed,
                            const std::function<bool(const std::vector<float> &, int)> & on_audio, std::string & error) {
    try {
        if (session_ == nullptr) throw std::runtime_error("TTS model is not loaded");
		if (!supported_language(language)) {
			error = language.empty() || language == "auto"
				? "TTS language was not detected; response remains available as text"
				: "TTS not available for this language";
			return false;
		}
        engine::runtime::TaskRequest request;
		request.text_input = engine::runtime::Transcript{text, language};
        engine::runtime::VoiceReference reference;
        reference.cached_voice_id = voice.empty() ? "F1" : voice;
        engine::runtime::VoiceCondition condition;
        condition.speaker = reference;
        request.voice = condition;
        request.options["speaking_rate"] = std::to_string(speed <= 0.0 ? 1.05 : speed);
        session_->start_stream(request);
        while (true) {
            auto event = session_->next_stream_event();
            if (!event.has_value()) break;
            for (const auto & named : event->named_audio_outputs) {
                if (named.audio.samples.empty()) continue;
                if (!on_audio(named.audio.samples, named.audio.sample_rate)) {
                    session_->reset();
                    return false;
                }
            }
        }
        (void) session_->finish_stream();
        return true;
    } catch (const std::exception & exception) { error = exception.what(); return false; }
}

}  // namespace atcode::voice
