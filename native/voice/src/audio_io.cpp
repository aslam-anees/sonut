#define MINIAUDIO_IMPLEMENTATION
#include "miniaudio.h"

#include "audio_io.hpp"

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <sstream>

namespace atcode::voice {

std::vector<float> resample_to_16k(const std::vector<float> & input, int sample_rate) {
    if (sample_rate <= 0 || input.empty()) return {};
    if (sample_rate == 16000) return input;
    const auto output_size = static_cast<size_t>(std::ceil(static_cast<double>(input.size()) * 16000.0 / sample_rate));
    std::vector<float> output(output_size);
    for (size_t index = 0; index < output.size(); ++index) {
        const double source = static_cast<double>(index) * sample_rate / 16000.0;
        const auto left = static_cast<size_t>(source);
        const auto right = std::min(left + 1, input.size() - 1);
        const float weight = static_cast<float>(source - left);
        output[index] = input[left] * (1.0F - weight) + input[right] * weight;
    }
    return output;
}

AudioIO::AudioIO() = default;

AudioIO::~AudioIO() {
    stop_input();
    cancel_playback();
	std::lock_guard lock(context_mutex_);
	if (context_ != nullptr) {
		ma_context_uninit(context_);
		delete context_;
		context_ = nullptr;
	}
}

bool AudioIO::ensure_context(std::string & error) const {
	std::lock_guard lock(context_mutex_);
	if (context_ != nullptr) return true;
	context_ = new ma_context;
	if (ma_context_init(nullptr, 0, nullptr, context_) != MA_SUCCESS) {
		delete context_;
		context_ = nullptr;
		error = "audio backend could not be initialized; check the terminal audio permissions";
		return false;
	}
	return true;
}

bool AudioIO::resolve_device(const std::string & device_id, bool capture, void * device_id_out, std::string & error) const {
	if (device_id.empty() || device_id == "default") return true;
	const std::string prefix = capture ? "input-" : "output-";
	if (device_id.rfind(prefix, 0) != 0) {
		error = "unknown audio device " + device_id + "; use /voice input or /voice output to list devices";
		return false;
	}
	int index = -1;
	try { index = std::stoi(device_id.substr(prefix.size())); } catch (...) {}
	if (index < 0) {
		error = "unknown audio device " + device_id + "; use /voice input or /voice output to list devices";
		return false;
	}
	if (!ensure_context(error)) return false;
	ma_device_info * playback = nullptr;
	ma_device_info * capture_infos = nullptr;
	ma_uint32 playback_count = 0;
	ma_uint32 capture_count = 0;
	if (ma_context_get_devices(context_, &playback, &playback_count, &capture_infos, &capture_count) != MA_SUCCESS) {
		error = "audio devices could not be enumerated";
		return false;
	}
	const auto count = capture ? capture_count : playback_count;
	if (static_cast<ma_uint32>(index) >= count) {
		error = "unknown audio device " + device_id + "; use /voice input or /voice output to list devices";
		return false;
	}
	const auto & info = capture ? capture_infos[index] : playback[index];
	*static_cast<ma_device_id *>(device_id_out) = info.id;
	return true;
}

bool AudioIO::start_input(const std::string & device_id, std::string & error) {
	ma_device_id selected_id{};
	if (!resolve_device(device_id, true, &selected_id, error)) return false;
	if (!ensure_context(error)) return false;
    stop_input();
    auto * device = new ma_device;
    auto config = ma_device_config_init(ma_device_type_capture);
    config.capture.format = ma_format_f32;
    config.capture.channels = 1;
    config.sampleRate = 16000;
	if (!device_id.empty() && device_id != "default") config.capture.pDeviceID = &selected_id;
    config.dataCallback = input_callback;
    config.pUserData = this;
    if (ma_device_init(context_, &config, device) != MA_SUCCESS) {
        delete device;
        error = "microphone could not be opened; check OS microphone permission and /voice input";
        return false;
    }
    if (ma_device_start(device) != MA_SUCCESS) {
        ma_device_uninit(device);
        delete device;
        error = "microphone could not be started; check that the input device is available";
        return false;
    }
    {
        std::lock_guard lock(input_mutex_);
        input_device_ = device;
        input_running_ = true;
        input_queue_.clear();
    }
    return true;
}

void AudioIO::stop_input() {
    ma_device * device = nullptr;
    {
        std::lock_guard lock(input_mutex_);
        input_running_ = false;
        device = input_device_;
        input_device_ = nullptr;
        input_queue_.clear();
    }
    input_cv_.notify_all();
    if (device != nullptr) {
        ma_device_uninit(device);
        delete device;
    }
}

bool AudioIO::take_input(std::vector<float> & samples, std::chrono::milliseconds timeout) {
    std::unique_lock lock(input_mutex_);
    input_cv_.wait_for(lock, timeout, [&] { return !input_queue_.empty() || !input_running_; });
    if (input_queue_.empty()) return false;
    const size_t count = std::min<size_t>(input_queue_.size(), 1600);
    samples.assign(input_queue_.begin(), input_queue_.begin() + static_cast<std::ptrdiff_t>(count));
    input_queue_.erase(input_queue_.begin(), input_queue_.begin() + static_cast<std::ptrdiff_t>(count));
    return true;
}

bool AudioIO::enqueue_playback(const std::vector<float> & samples, int sample_rate, const std::string & device_id, std::string & error) {
    if (samples.empty() || sample_rate <= 0) return true;
    ma_device * old_device = nullptr;
    {
        std::lock_guard lock(output_mutex_);
	    if (output_device_ != nullptr && (output_device_id_ != device_id || output_sample_rate_ != sample_rate)) {
		    old_device = output_device_;
		    output_device_ = nullptr;
		    output_queue_.clear();
	    }
    }
    // Do not wait for miniaudio's callback while holding output_mutex_: the
    // callback takes the same mutex to drain the queue.
    if (old_device != nullptr) {
		ma_device_uninit(old_device);
		delete old_device;
	}
    std::lock_guard lock(output_mutex_);
    if (output_device_ == nullptr) {
		ma_device_id selected_id{};
		if (!resolve_device(device_id, false, &selected_id, error)) return false;
		if (!ensure_context(error)) return false;
        auto * device = new ma_device;
        auto config = ma_device_config_init(ma_device_type_playback);
        config.playback.format = ma_format_f32;
        config.playback.channels = 1;
        config.sampleRate = static_cast<ma_uint32>(sample_rate);
		if (!device_id.empty() && device_id != "default") config.playback.pDeviceID = &selected_id;
        config.dataCallback = output_callback;
        config.pUserData = this;
		const auto init_result = ma_device_init(context_, &config, device);
		if (init_result != MA_SUCCESS) {
			delete device;
			error = "speaker could not be opened; check OS audio permission and /voice output";
			return false;
		}
		if (ma_device_start(device) != MA_SUCCESS) {
			ma_device_uninit(device);
			delete device;
			error = "speaker could not be opened; check OS audio permission and /voice output";
            return false;
        }
        output_device_ = device;
        output_sample_rate_ = sample_rate;
		output_device_id_ = device_id;
    }
    output_queue_.insert(output_queue_.end(), samples.begin(), samples.end());
    return true;
}

void AudioIO::cancel_playback() {
    ma_device * device = nullptr;
    {
        std::lock_guard lock(output_mutex_);
        output_queue_.clear();
        device = output_device_;
        output_device_ = nullptr;
        output_device_id_.clear();
    }
    // miniaudio may wait for output_callback, which also takes output_mutex_.
    if (device != nullptr) {
        ma_device_uninit(device);
        delete device;
    }
}

std::vector<AudioDeviceInfo> AudioIO::devices() const {
    std::vector<AudioDeviceInfo> result{{"default", "System default microphone/speaker", "input,output"}};
	std::string error;
	if (!ensure_context(error)) return result;
	ma_device_info * playback = nullptr;
	ma_device_info * capture = nullptr;
	ma_uint32 playback_count = 0;
	ma_uint32 capture_count = 0;
	if (ma_context_get_devices(context_, &playback, &playback_count, &capture, &capture_count) != MA_SUCCESS) return result;
	for (ma_uint32 index = 0; index < capture_count; ++index) {
		result.push_back({"input-" + std::to_string(index), capture[index].name, "input"});
	}
	for (ma_uint32 index = 0; index < playback_count; ++index) {
		result.push_back({"output-" + std::to_string(index), playback[index].name, "output"});
	}
	return result;
}

void AudioIO::input_callback(ma_device * device, void *, const void * input, unsigned frame_count) {
    auto * self = static_cast<AudioIO *>(device->pUserData);
    if (self == nullptr || input == nullptr) return;
    const auto * samples = static_cast<const float *>(input);
    {
        std::lock_guard lock(self->input_mutex_);
        if (!self->input_running_) return;
        self->input_queue_.insert(self->input_queue_.end(), samples, samples + frame_count);
        const size_t cap = 16000 * 2;
        while (self->input_queue_.size() > cap) self->input_queue_.pop_front();
    }
    self->input_cv_.notify_one();
}

void AudioIO::output_callback(ma_device * device, void * output, const void *, unsigned frame_count) {
    auto * self = static_cast<AudioIO *>(device->pUserData);
    auto * samples = static_cast<float *>(output);
    std::lock_guard lock(self->output_mutex_);
    for (unsigned index = 0; index < frame_count; ++index) {
        samples[index] = self->output_queue_.empty() ? 0.0F : self->output_queue_.front();
        if (!self->output_queue_.empty()) self->output_queue_.pop_front();
    }
}

}  // namespace atcode::voice
