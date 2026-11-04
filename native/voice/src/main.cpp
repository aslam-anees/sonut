#include "asr.hpp"
#include "audio_io.hpp"
#include "protocol.hpp"
#include "tts.hpp"

#include <condition_variable>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdlib>
#include <deque>
#include <filesystem>
#include <iostream>
#include <memory>
#include <mutex>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

namespace atcode::voice {
namespace {

std::string env_or(const char * name, const std::string & fallback) {
    const char * value = std::getenv(name);
    return value == nullptr || *value == '\0' ? fallback : value;
}

double asr_chunk_seconds() {
    const auto value = env_or("ATCODE_VOICE_ASR_CHUNK_SECONDS", "2");
    try {
        const auto seconds = std::stod(value);
        if (seconds == 1.5 || seconds == 2.0 || seconds == 3.0) return seconds;
    } catch (...) {}
    std::cerr << "atcode-voice: invalid ATCODE_VOICE_ASR_CHUNK_SECONDS=" << value << ", using 2\n";
    return 2.0;
}

engine::core::BackendType backend_from_env() {
    const auto name = env_or("ATCODE_VOICE_BACKEND", "best");
    if (name == "cpu") return engine::core::BackendType::Cpu;
    if (name == "cuda") return engine::core::BackendType::Cuda;
    if (name == "vulkan") return engine::core::BackendType::Vulkan;
    if (name == "metal") return engine::core::BackendType::Metal;
    return engine::core::BackendType::BestAvailable;
}

class Service {
public:
    Service()
        : model_root_(env_or("ATCODE_VOICE_MODEL_ROOT", default_model_root())), backend_(backend_from_env()) {
        load_models();
        tts_thread_ = std::thread([this] { tts_loop(); });
    }

    ~Service() { shutdown(); }

    void handle(const Message & request) {
        try {
            if (request.type == "hello") {
                if (asr_ == nullptr) {
                    emit_error(request.request_id, {}, "voice models are not installed; run /voice setup");
                } else {
                    emit(Message{1, "ready", request.request_id, {}, {}, {}, {}, 0, "light", {}, {}, "", {}});
                }
            } else if (request.type == "configure") {
                std::lock_guard config_lock(config_mutex_);
                configured_language_ = request.language.empty() ? "auto" : request.language;
                configured_voice_ = request.voice.empty() ? "F1" : request.voice;
                configured_speed_ = request.speed <= 0.0 ? 1.05 : request.speed;
                configured_input_device_ = request.input_device.empty() ? "default" : request.input_device;
                configured_output_device_ = request.output_device.empty() ? "default" : request.output_device;
				if (request.silence_ms >= 500 && request.silence_ms <= 30000) configured_silence_ms_ = request.silence_ms;
                emit_state(request.request_id, "idle", {});
            } else if (request.type == "devices.list") {
                std::ostringstream devices;
                devices << "[";
                bool first = true;
                for (const auto & device : audio_.devices()) {
                    if (!first) devices << ",";
                    first = false;
                    devices << "{\"id\":\"" << json_escape(device.id) << "\",\"name\":\"" << json_escape(device.name)
                            << "\",\"direction\":\"" << json_escape(device.direction) << "\"}";
                }
                devices << "]";
                Message response{1, "devices", request.request_id}; response.devices_json = devices.str(); emit(response);
            } else if (request.type == "models.reload") {
                cancel_listening(utterance_id_);
                load_models();
                if (asr_ == nullptr) emit_error(request.request_id, {}, "voice models are still unavailable; run /voice setup");
                else emit(Message{1, "ready", request.request_id, {}, {}, {}, {}, 0, "light", {}, {}, "", {}});
            } else if (request.type == "listen.start") {
                start_listening(request);
            } else if (request.type == "listen.cancel") {
                cancel_listening(request.utterance_id, request.request_id);
            } else if (request.type == "tts.enqueue") {
                enqueue_tts(request);
            } else if (request.type == "tts.cancel") {
                cancel_tts(request);
            } else if (request.type == "ping") {
                Message response{1, "state", request.request_id}; response.state = listening_.load() ? "listening" : "idle"; emit(response);
            } else if (request.type == "shutdown") {
                shutdown_requested_.store(true);
            } else {
                emit_error(request.request_id, request.utterance_id, "unknown helper command: " + request.type);
            }
        } catch (const std::exception & exception) {
            emit_error(request.request_id, request.utterance_id, exception.what());
        }
    }

    bool shutdown_requested() const { return shutdown_requested_.load(); }

private:
    static std::string default_model_root() {
        const char * home = std::getenv("HOME");
#ifdef _WIN32
        if (home == nullptr) home = std::getenv("USERPROFILE");
#endif
        return std::string(home == nullptr ? "." : home) + "/.atcode/voice/models";
    }

    void load_models() {
        const auto install_root = std::filesystem::path(model_root_).parent_path();
        const auto qwen_path = (std::filesystem::path(model_root_) / "qwen3-asr-0.6b-q8_0.gguf").string();
        const auto supertonic_path = (std::filesystem::path(model_root_) / "supertonic-3-f16.gguf").string();
        const auto vad_path = (install_root / "assets/framework/models/silero_vad").string();
        std::string error;
        if (std::filesystem::exists(qwen_path)) {
            asr_ = std::make_unique<ASRSession>();
            if (!asr_->load(qwen_path, vad_path, backend_, error)) { std::cerr << "atcode-voice ASR load: " << error << "\n"; asr_.reset(); }
        }
        error.clear();
        if (std::filesystem::exists(supertonic_path)) {
            tts_ = std::make_unique<TTSSession>();
            if (!tts_->load(supertonic_path, backend_, error)) { std::cerr << "atcode-voice TTS load: " << error << "\n"; tts_.reset(); }
        }
        if (asr_ == nullptr || tts_ == nullptr) {
            std::cerr << "atcode-voice models unavailable; text mode remains usable and /voice setup can repair the pack\n";
        }
    }

    void start_listening(const Message & request) {
        cancel_listening(utterance_id_);
        if (asr_ == nullptr) { emit_error(request.request_id, request.utterance_id, "ASR model is not installed; run /voice setup"); return; }
        std::string error;
        std::string language;
        std::string input_device;
        {
            std::lock_guard config_lock(config_mutex_);
            language = request.language.empty() ? configured_language_ : request.language;
            input_device = configured_input_device_;
        }
        if (!asr_->start(language, error)) { emit_error(request.request_id, request.utterance_id, error); return; }
        utterance_id_ = request.utterance_id;
        request_id_ = request.request_id;
        listening_.store(true);
        listen_stop_.store(false);
        if (!audio_.start_input(input_device, error)) { listening_.store(false); asr_->cancel(); emit_error(request.request_id, request.utterance_id, error); return; }
        listen_thread_ = std::thread([this] { listen_loop(); });
        emit_state(request.request_id, "listening", utterance_id_);
    }

    void cancel_listening(const std::string & id, const std::string & cancel_request_id = {}) {
        if (!listening_.load() && !listen_thread_.joinable()) return;
        listen_stop_.store(true);
        audio_.stop_input();
        if (listen_thread_.joinable()) listen_thread_.join();
        if (asr_ != nullptr) asr_->cancel();
        listening_.store(false);
        if (!id.empty()) emit_state(cancel_request_id.empty() ? request_id_ : cancel_request_id, "idle", id);
    }

    void listen_loop() {
        int silence_ms = 3000;
        {
            std::lock_guard config_lock(config_mutex_);
            silence_ms = configured_silence_ms_;
        }
        const auto silence_limit = std::chrono::milliseconds(silence_ms);
        constexpr auto minimum_speech = std::chrono::milliseconds(250);
        constexpr auto pre_roll_samples = static_cast<size_t>(16000 * 300 / 1000);
        constexpr auto no_speech_limit = std::chrono::seconds(10);
        constexpr auto maximum = std::chrono::seconds(120);
        const auto started = std::chrono::steady_clock::now();
        auto last_speech = started;
        auto speech_started = started;
        bool speech_seen = false;
        std::string last_partial;
        std::vector<float> samples;
		std::vector<float> asr_buffer;
		std::deque<float> pre_roll;
		const auto chunk_samples = static_cast<size_t>(asr_chunk_seconds() * 16000.0);
		const auto publish_asr = [&](const std::vector<float> & chunk) {
			if (chunk.empty()) return;
			std::string partial, language, error;
			if (asr_->process(chunk, partial, language, error)) {
				if (!partial.empty() && partial != last_partial) {
					last_partial = partial;
					Message event{1, "asr.partial", request_id_, utterance_id_, partial, language};
					emit(event);
				}
			} else if (!error.empty()) {
				emit_error(request_id_, utterance_id_, error);
				listen_stop_.store(true);
			}
		};
        while (!listen_stop_.load()) {
            if (!audio_.take_input(samples, std::chrono::milliseconds(100))) {
                const auto now = std::chrono::steady_clock::now();
                if (!speech_seen && now - started >= no_speech_limit) break;
                if (speech_seen && now - last_speech >= silence_limit) break;
                if (now - started >= maximum) break;
                continue;
            }
			for (const auto sample : samples) {
				pre_roll.push_back(sample);
				if (pre_roll.size() > pre_roll_samples) pre_roll.pop_front();
			}
            double energy = 0.0;
            for (const auto sample : samples) energy += sample * sample;
            energy = samples.empty() ? 0.0 : std::sqrt(energy / samples.size());
            const bool speech = energy > 0.012;
            const auto now = std::chrono::steady_clock::now();
            const bool first_speech = speech && !speech_seen;
            if (speech) {
				if (!speech_seen) {
					speech_seen = true;
					speech_started = now;
					last_speech = now;
					emit_state(request_id_, "vad.speech_start", utterance_id_);
					asr_buffer.insert(asr_buffer.end(), pre_roll.begin(), pre_roll.end());
				} else {
					last_speech = now;
				}
			}
			if (speech_seen) {
				if (!first_speech) asr_buffer.insert(asr_buffer.end(), samples.begin(), samples.end());
				if (asr_buffer.size() >= chunk_samples) {
					publish_asr(asr_buffer);
					asr_buffer.clear();
				}
			}
            if (!speech_seen && now - started >= no_speech_limit) break;
			if (speech_seen && now - last_speech >= silence_limit && now - speech_started >= minimum_speech) break;
            if (now - started >= maximum) break;
        }
        audio_.stop_input();
        if (listen_stop_.load() || !speech_seen) { asr_->cancel(); listening_.store(false); return; }
		Message speech_end{1, "vad.speech_end", request_id_, utterance_id_};
		emit(speech_end);
		if (!asr_buffer.empty()) publish_asr(asr_buffer);
        std::string text, language, error;
        if (!asr_->finish(text, language, error)) { emit_error(request_id_, utterance_id_, error); }
        else { Message event{1, "asr.final", request_id_, utterance_id_, text, language}; emit(event); }
        listening_.store(false);
    }

    struct TTSItem { Message request; uint64_t generation; };

    void enqueue_tts(const Message & request) {
		std::lock_guard lock(tts_mutex_);
		if (tts_queue_.size() >= kMaxTTSQueue) {
			emit_error(request.request_id, request.utterance_id, "TTS queue is full; cancel speech or wait for playback");
			return;
		}
        tts_queue_.push_back({request, tts_generation_});
        tts_cv_.notify_one();
    }

    void cancel_tts(const Message & request) {
        std::lock_guard lock(tts_mutex_);
        ++tts_generation_;
        tts_queue_.clear();
        audio_.cancel_playback();
        if (!request.utterance_id.empty()) emit_state(request.request_id, "idle", request.utterance_id);
    }

    void tts_loop() {
        while (!shutdown_requested_.load()) {
            TTSItem item;
            {
                std::unique_lock lock(tts_mutex_);
                tts_cv_.wait_for(lock, std::chrono::milliseconds(100), [&] { return !tts_queue_.empty() || shutdown_requested_.load(); });
                if (shutdown_requested_.load()) break;
                if (tts_queue_.empty()) continue;
                item = tts_queue_.front(); tts_queue_.pop_front();
            }
            if (tts_ == nullptr) { emit_error(item.request.request_id, item.request.utterance_id, "TTS not available for this language or model"); continue; }
            Message started{1, "tts.started", item.request.request_id, item.request.utterance_id}; emit(started);
            std::string error;
            std::string output_device;
            {
                std::lock_guard config_lock(config_mutex_);
                output_device = configured_output_device_;
            }
            const auto ok = tts_->synthesize(item.request.text, item.request.language, item.request.voice, item.request.speed,
                [&](const std::vector<float> & samples, int rate) {
                    std::lock_guard lock(tts_mutex_);
                    if (item.generation != tts_generation_) return false;
                    std::string audio_error;
                    return audio_.enqueue_playback(samples, rate, output_device, audio_error);
                }, error);
            if (!ok && !error.empty()) emit_error(item.request.request_id, item.request.utterance_id, error);
            else { Message done{1, "tts.done", item.request.request_id, item.request.utterance_id}; emit(done); }
        }
    }

    void emit_state(const std::string & request_id, const std::string & state, const std::string & utterance_id) {
        Message message{1, "state", request_id, utterance_id}; message.state = state; emit(message);
    }

    void emit_error(const std::string & request_id, const std::string & utterance_id, const std::string & error) {
        Message message{1, "error", request_id, utterance_id}; message.error = error; emit(message);
    }

    void emit(const Message & message) {
        std::lock_guard lock(output_mutex_);
        encode(std::cout, message);
    }

    void shutdown() {
        if (shutdown_requested_.exchange(true)) return;
        cancel_listening(utterance_id_);
        tts_cv_.notify_all();
        if (tts_thread_.joinable()) tts_thread_.join();
        audio_.cancel_playback();
    }

    std::string model_root_;
    engine::core::BackendType backend_;
    std::unique_ptr<ASRSession> asr_;
    std::unique_ptr<TTSSession> tts_;
    AudioIO audio_;
    std::string configured_language_ = "auto";
    std::string configured_voice_ = "F1";
    double configured_speed_ = 1.05;
    int configured_silence_ms_ = 3000;
    std::string configured_input_device_ = "default";
    std::string configured_output_device_ = "default";
    std::mutex config_mutex_;
    std::string request_id_;
    std::string utterance_id_;
    std::atomic<bool> listening_{false};
    std::atomic<bool> listen_stop_{false};
    std::atomic<bool> shutdown_requested_{false};
    std::thread listen_thread_;
    std::thread tts_thread_;
    std::mutex output_mutex_;
    std::mutex tts_mutex_;
    std::condition_variable tts_cv_;
    std::deque<TTSItem> tts_queue_;
    uint64_t tts_generation_ = 0;
    static constexpr size_t kMaxTTSQueue = 32;
};

}  // namespace
}  // namespace atcode::voice

int main() {
    using namespace atcode::voice;
    Service service;
    std::string line;
    while (std::getline(std::cin, line)) {
        Message message;
        std::string error;
        if (!decode(line, message, error)) {
            Message response{1, "error"}; response.error = error;
            encode(std::cout, response);
            continue;
        }
        service.handle(message);
        if (service.shutdown_requested()) break;
    }
    return 0;
}
