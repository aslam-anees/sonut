#pragma once

#include <cstdint>
#include <iosfwd>
#include <string>
#include <vector>

namespace atcode::voice {

constexpr int kProtocolVersion = 1;

struct Message {
    int version = kProtocolVersion;
    std::string type;
    std::string request_id;
    std::string utterance_id;
    std::string text;
    std::string language;
    std::string voice;
    double speed = 0.0;
    std::string profile;
    std::string input_device;
    std::string output_device;
    std::string state;
    std::string error;
    std::string devices_json;
    int silence_ms = 0;
};

bool decode(const std::string & line, Message & message, std::string & error);
void encode(std::ostream & out, const Message & message);
std::string json_escape(const std::string & value);

}  // namespace atcode::voice
