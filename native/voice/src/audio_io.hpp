#pragma once

#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <deque>
#include <functional>
#include <mutex>
#include <string>
#include <vector>

struct ma_device;
struct ma_context;

namespace atcode::voice {

struct AudioDeviceInfo {
    std::string id;
    std::string name;
    std::string direction;
};

std::vector<float> resample_to_16k(const std::vector<float> & input, int sample_rate);

class AudioIO {
public:
    AudioIO();
    ~AudioIO();

    bool start_input(const std::string & device_id, std::string & error);
    void stop_input();
    bool take_input(std::vector<float> & samples, std::chrono::milliseconds timeout);

    bool enqueue_playback(const std::vector<float> & samples, int sample_rate, const std::string & device_id, std::string & error);
    void cancel_playback();
    std::vector<AudioDeviceInfo> devices() const;

private:
    static void input_callback(ma_device * device, void * output, const void * input, unsigned frame_count);
    static void output_callback(ma_device * device, void * output, const void * input, unsigned frame_count);
	bool ensure_context(std::string & error) const;
	bool resolve_device(const std::string & device_id, bool capture, void * device_id_out, std::string & error) const;

    mutable std::mutex input_mutex_;
    std::condition_variable input_cv_;
    std::deque<float> input_queue_;
    bool input_running_ = false;
    ma_device * input_device_ = nullptr;
	mutable ma_context * context_ = nullptr;
	mutable std::mutex context_mutex_;

    mutable std::mutex output_mutex_;
    std::deque<float> output_queue_;
    ma_device * output_device_ = nullptr;
    int output_sample_rate_ = 48000;
	std::string output_device_id_;
};

}  // namespace atcode::voice
